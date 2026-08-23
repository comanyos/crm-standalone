// Package google orchestrates the CRM's Google connection: it owns the
// authorization flow, the durable connection lifecycle, and the audit trail,
// and it is the only place allowed to hold a plaintext Google credential —
// briefly, in memory, between the provider and the sealing key.
//
// The split is deliberate. internal/integration/googleoauth knows the
// provider's cryptography and HTTP and nothing about tenants; internal/domain
// knows tenants and persistence and nothing about Google; this package is the
// seam, and it is where the security properties of the flow are actually
// enforced:
//
//   - The browser never receives a state value it did not just generate, a
//     PKCE verifier, a token, or the client secret.
//   - The callback is unauthenticated, so it derives its tenant and actor
//     from the one-time state record rather than from anything in the
//     request.
//   - Every failure the callback reports back to the browser is a bounded
//     reason code from this file, never a provider message.
package google

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/googleoauth"
	"github.com/santoshguru/crm/internal/metrics"
)

// ErrNotConfigured is returned when the deployment has no Google credentials.
// It is separate from a failure: a CRM without GOOGLE_OAUTH_* set is a
// correctly running CRM that simply cannot offer this feature, and the UI
// needs to tell those two apart.
var ErrNotConfigured = errors.New("google: integration is not configured")

// Service is the Google connection's orchestration layer.
type Service struct {
	store domain.Store
	oauth *googleoauth.Client
	// webBaseURL is where the callback sends the browser when it is done.
	// Empty means "render a self-contained result page instead", which is the
	// correct behaviour for a deployment that has not told us where its web
	// application lives — guessing would be an open redirect waiting to
	// happen.
	webBaseURL string
	now        func() time.Time
}

// New wires the service. A nil oauth client is legal and means the deployment
// has not configured Google; every entry point then returns ErrNotConfigured
// rather than panicking somewhere deeper.
func New(store domain.Store, oauth *googleoauth.Client, webBaseURL string) *Service {
	return &Service{store: store, oauth: oauth, webBaseURL: strings.TrimRight(webBaseURL, "/"), now: func() time.Time { return time.Now().UTC() }}
}

// Configured reports whether this deployment can run the Google flow.
func (s *Service) Configured() bool { return s != nil && s.oauth != nil }

// RequestedScopes is what the connection UI shows a user before they consent.
func (s *Service) RequestedScopes() []string { return googleoauth.Scopes() }

// Start begins an authorization for one authenticated actor and returns the
// URL the browser must be sent to.
//
// The state and PKCE verifier are written durably *before* the URL is
// returned. Doing it the other way round — return first, persist later —
// leaves a window where a callback arrives for a state nothing knows about,
// and the only safe response to that is to reject a legitimate user's
// authorization.
func (s *Service) Start(
	ctx context.Context, scope domain.Scope, actorID, returnPath string,
) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	if strings.TrimSpace(actorID) == "" {
		return "", fmt.Errorf("%w: an authorization must name the actor it belongs to", domain.ErrInvalid)
	}
	if err := domain.ValidateGoogleReturnPath(returnPath); err != nil {
		return "", err
	}

	now := s.now()
	authURL, pending, err := s.oauth.Begin(now)
	if err != nil {
		return "", fmt.Errorf("google: begin authorization: %w", err)
	}

	sealed, err := s.oauth.Encrypt(
		googleoauth.TokenSet{AccessToken: pending.CodeVerifier},
		googleoauth.StateAssociatedData(scope.TenantID, pending.State))
	if err != nil {
		return "", fmt.Errorf("google: seal pkce verifier: %w", err)
	}

	if _, err := s.store.CreateGoogleOAuthState(ctx, scope, &domain.GoogleOAuthState{
		State:              pending.State,
		ActorID:            actorID,
		VerifierCiphertext: sealed,
		ReturnPath:         returnPath,
		CreatedAt:          now,
		ExpiresAt:          pending.ExpiresAt,
	}); err != nil {
		return "", err
	}

	// Best-effort housekeeping. A failure here must not stop a user
	// connecting: expired states are inert, they are merely untidy.
	if _, err := s.store.PurgeExpiredGoogleOAuthStates(ctx, scope, now); err != nil {
		slog.Warn("could not purge expired google oauth states", "error", err)
	}

	metrics.GoogleOAuthFlowsTotal.WithLabelValues("started").Inc()
	return authURL, nil
}

// The bounded reason codes the callback may report. They exist so the browser
// — and the log line, and the stored LastError — can never carry a provider
// message, which may contain authorization material.
const (
	ReasonAccessDenied   = "access_denied"
	ReasonProviderError  = "provider_error"
	ReasonInvalidRequest = "invalid_request"
	ReasonInvalidState   = "invalid_state"
	ReasonExchangeFailed = "exchange_failed"
	ReasonStorageFailed  = "storage_failed"
	ReasonNotConfigured  = "not_configured"
)

// CallbackResult is what the HTTP callback handler needs in order to finish
// the browser's journey. It deliberately carries no provider data.
type CallbackResult struct {
	// Connected is the outcome. When false, Reason names it.
	Connected bool
	// Reason is one of the bounded codes above, empty on success.
	Reason string
	// ReturnPath is the validated same-origin path recorded when the flow
	// started, or empty if none was.
	ReturnPath string
	// Email is the Google account that was connected, on success. It is the
	// user's own address, shown back to them so a wrong-account authorization
	// is visible.
	Email string
}

// Complete finishes an OAuth callback.
//
// It never returns an error for a rejected callback: a browser redirect has
// no good way to render one, and distinguishing "bad state" from "expired
// state" for an unauthenticated caller tells them whether a state was ever
// real. Errors are reserved for the cases where the service itself is broken;
// everything a caller can cause comes back as a CallbackResult with a reason.
func (s *Service) Complete(ctx context.Context, state, code, providerError string) CallbackResult {
	if !s.Configured() {
		return CallbackResult{Reason: ReasonNotConfigured}
	}

	// Resolve and consume first, even when Google reported an error. A state
	// left unconsumed after a failed authorization is a state someone can
	// still replay.
	pending, scope, ok := s.consume(ctx, state)
	if !ok {
		metrics.GoogleOAuthFlowsTotal.WithLabelValues("invalid_state").Inc()
		return CallbackResult{Reason: ReasonInvalidState}
	}

	if providerError != "" {
		reason := ReasonProviderError
		if providerError == "access_denied" {
			reason = ReasonAccessDenied
		}
		metrics.GoogleOAuthFlowsTotal.WithLabelValues(reason).Inc()
		return CallbackResult{Reason: reason, ReturnPath: pending.ReturnPath}
	}
	if strings.TrimSpace(code) == "" {
		metrics.GoogleOAuthFlowsTotal.WithLabelValues(ReasonInvalidRequest).Inc()
		return CallbackResult{Reason: ReasonInvalidRequest, ReturnPath: pending.ReturnPath}
	}

	verifier, err := s.oauth.Decrypt(pending.VerifierCiphertext,
		googleoauth.StateAssociatedData(scope.TenantID, pending.State))
	if err != nil {
		// The sealed verifier failing to open means the stored row does not
		// belong to this tenant, or the sealing key changed. Neither is the
		// browser's fault and neither is safe to continue from.
		slog.Error("google oauth state verifier failed authentication", "tenant_present", scope.TenantID != "")
		metrics.GoogleOAuthFlowsTotal.WithLabelValues(ReasonInvalidState).Inc()
		return CallbackResult{Reason: ReasonInvalidState, ReturnPath: pending.ReturnPath}
	}

	tokens, err := s.oauth.Exchange(ctx, code, verifier.AccessToken)
	if err != nil {
		// err already excludes the provider body (googleoauth.tokenRequest),
		// but it is still not shown to the browser.
		slog.Warn("google authorization code exchange failed", "error", err)
		metrics.GoogleOAuthFlowsTotal.WithLabelValues(ReasonExchangeFailed).Inc()
		return CallbackResult{Reason: ReasonExchangeFailed, ReturnPath: pending.ReturnPath}
	}

	// Which account did they actually consent with? Read it from the
	// provider rather than trusting anything in the request. A failure here
	// is not fatal: an unnamed connection still works, it is just less
	// legible, so it must not cost the user their authorization.
	email, err := s.oauth.AccountEmail(ctx, tokens.AccessToken)
	if err != nil {
		slog.Warn("could not read the connected google account address", "error", err)
	}

	// The owner comes from the state record written at Start, never from the
	// callback — which is what stops one user's redirect connecting a
	// colleague's mailbox.
	view, err := s.persist(ctx, scope, pending.ActorID, pending.ActorID, email, tokens)
	if err != nil {
		slog.Error("could not persist google connection", "error", err)
		metrics.GoogleOAuthFlowsTotal.WithLabelValues(ReasonStorageFailed).Inc()
		return CallbackResult{Reason: ReasonStorageFailed, ReturnPath: pending.ReturnPath}
	}

	metrics.GoogleOAuthFlowsTotal.WithLabelValues("connected").Inc()
	return CallbackResult{
		Connected:  true,
		ReturnPath: pending.ReturnPath,
		Email:      view.GoogleAccountEmail,
	}
}

// consume resolves the callback's tenant from its opaque state value and
// redeems the pending record, once.
func (s *Service) consume(ctx context.Context, state string) (*domain.GoogleOAuthState, domain.Scope, bool) {
	if strings.TrimSpace(state) == "" {
		return nil, domain.Scope{}, false
	}
	tenantID, err := s.store.ResolveGoogleOAuthStateTenant(ctx, state)
	if err != nil {
		return nil, domain.Scope{}, false
	}
	scope := domain.Scope{TenantID: tenantID}
	pending, err := s.store.ConsumeGoogleOAuthState(ctx, scope, state, s.now())
	if err != nil {
		return nil, domain.Scope{}, false
	}
	return pending, scope, true
}

// persist seals the token set and writes the connection, then records the
// audit event in the same call so a connection cannot exist without one.
func (s *Service) persist(
	ctx context.Context, scope domain.Scope, ownerID, actorID, email string,
	tokens googleoauth.TokenSet,
) (domain.GoogleConnectionView, error) {
	sealed, err := s.oauth.Encrypt(tokens, googleoauth.AssociatedData(scope.TenantID, ownerID))
	if err != nil {
		return domain.GoogleConnectionView{}, fmt.Errorf("google: seal token set: %w", err)
	}

	now := s.now()
	saved, err := s.store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID:              ownerID,
		GoogleAccountEmail:   email,
		Status:               domain.GoogleStatusConnected,
		GrantedScopes:        strings.Fields(tokens.Scope),
		TokenCiphertext:      sealed,
		AccessTokenExpiresAt: tokens.Expiry,
		ConnectedBy:          actorID,
		ConnectedAt:          now,
		LastRefreshedAt:      now,
	})
	if err != nil {
		return domain.GoogleConnectionView{}, err
	}

	s.audit(ctx, scope, saved, EventConnectionAuthorized, actorID)
	return saved.View(), nil
}

// Status returns one owner's connection, or a zero view when there is none.
//
// A missing connection is not an error: "you have not connected Google" is
// the normal state of most users, and making the UI distinguish a 404 from a
// real failure to render that buys nothing.
func (s *Service) Status(
	ctx context.Context, scope domain.Scope, ownerID string,
) (domain.GoogleConnectionView, error) {
	connection, err := s.store.GetGoogleConnection(ctx, scope, ownerID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.GoogleConnectionView{OwnerID: ownerID}, nil
	}
	if err != nil {
		return domain.GoogleConnectionView{}, err
	}
	return connection.View(), nil
}

// List returns every connection in the tenant, for an administrator auditing
// who has linked a mailbox. It returns views, never credentials.
func (s *Service) List(ctx context.Context, scope domain.Scope) ([]domain.GoogleConnectionView, error) {
	rows, err := s.store.ListGoogleConnections(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]domain.GoogleConnectionView, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.View())
	}
	return out, nil
}

// Disconnect withdraws a connection.
//
// The order is the point: revoke at Google first, then erase locally. Erasing
// first would leave a grant alive at the provider that this system no longer
// has the refresh token to revoke — a credential nobody can withdraw. A
// provider outage therefore fails the disconnect rather than half-completing
// it, except when the token is already dead, which googleoauth.Revoke
// reports as success because it describes the state the caller wanted.
//
// purge additionally deletes the row. Without it a tombstone remains, which
// is what the audit trail needs to answer "who disconnected this, and when".
func (s *Service) Disconnect(
	ctx context.Context, scope domain.Scope, ownerID, actorID string, purge bool,
) (domain.GoogleConnectionView, error) {
	connection, err := s.store.GetGoogleConnection(ctx, scope, ownerID)
	if err != nil {
		return domain.GoogleConnectionView{}, err
	}

	if len(connection.TokenCiphertext) > 0 {
		if !s.Configured() {
			// There is a live credential and no key to open it with. Erasing
			// the row here would strand a grant at Google forever.
			return domain.GoogleConnectionView{}, ErrNotConfigured
		}
		tokens, err := s.oauth.Decrypt(connection.TokenCiphertext,
			googleoauth.AssociatedData(scope.TenantID, ownerID))
		if err != nil {
			return domain.GoogleConnectionView{}, fmt.Errorf("google: open stored credential: %w", err)
		}
		// The refresh token invalidates the entire grant; the access token
		// only itself. Prefer the former, fall back when Google never issued
		// one.
		revoke := tokens.RefreshToken
		if revoke == "" {
			revoke = tokens.AccessToken
		}
		if revoke != "" {
			if err := s.oauth.Revoke(ctx, revoke); err != nil {
				return domain.GoogleConnectionView{}, fmt.Errorf("google: revoke grant: %w", err)
			}
		}
	}

	now := s.now()
	if purge {
		if err := s.store.DeleteGoogleConnection(ctx, scope, ownerID); err != nil {
			return domain.GoogleConnectionView{}, err
		}
		// The audit event outlives the row on purpose: "this tenant purged a
		// Google connection" is exactly the fact a purge must not erase.
		connection.DisconnectedAt = now
		connection.TokenCiphertext = nil
		connection.Status = domain.GoogleStatusDisconnected
		s.audit(ctx, scope, connection, EventConnectionPurged, actorID)
		metrics.GoogleOAuthFlowsTotal.WithLabelValues("purged").Inc()
		return domain.GoogleConnectionView{OwnerID: ownerID}, nil
	}

	connection.Status = domain.GoogleStatusDisconnected
	connection.TokenCiphertext = nil
	connection.AccessTokenExpiresAt = time.Time{}
	connection.DisconnectedAt = now
	connection.LastError = ""
	saved, err := s.store.SaveGoogleConnection(ctx, scope, connection)
	if err != nil {
		return domain.GoogleConnectionView{}, err
	}
	s.audit(ctx, scope, saved, EventConnectionDisconnected, actorID)
	metrics.GoogleOAuthFlowsTotal.WithLabelValues("disconnected").Inc()
	return saved.View(), nil
}

// AccessToken returns a usable Google access token for one owner, refreshing
// it first if it has expired or is about to.
//
// This is the seam the Gmail and Calendar sync will call. It is here rather
// than in each caller so that exactly one piece of code decides when to
// refresh, what to do when a refresh fails, and — the part easy to get wrong
// — that Google's refresh response usually omits the refresh token, so the
// prior one must be carried forward or the connection dies on its next
// refresh.
func (s *Service) AccessToken(
	ctx context.Context, scope domain.Scope, ownerID string,
) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	connection, err := s.store.GetGoogleConnection(ctx, scope, ownerID)
	if err != nil {
		return "", err
	}
	if connection.Status != domain.GoogleStatusConnected {
		return "", fmt.Errorf("%w: google connection requires reauthorization", domain.ErrInvalidTransition)
	}

	binding := googleoauth.AssociatedData(scope.TenantID, ownerID)
	tokens, err := s.oauth.Decrypt(connection.TokenCiphertext, binding)
	if err != nil {
		return "", fmt.Errorf("google: open stored credential: %w", err)
	}
	if !connection.NeedsRefresh(s.now()) {
		return tokens.AccessToken, nil
	}
	if tokens.RefreshToken == "" {
		return "", s.markReauthorizationRequired(ctx, scope, connection, "no_refresh_token")
	}

	refreshed, err := s.oauth.Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		slog.Warn("google token refresh failed", "error", err)
		return "", s.markReauthorizationRequired(ctx, scope, connection, "refresh_rejected")
	}
	// Google omits refresh_token from most refresh responses. Carrying the
	// prior one forward is what keeps the connection alive past this hour.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tokens.RefreshToken
	}
	if refreshed.Scope == "" {
		refreshed.Scope = tokens.Scope
	}

	sealed, err := s.oauth.Encrypt(refreshed, binding)
	if err != nil {
		return "", fmt.Errorf("google: seal refreshed token set: %w", err)
	}
	connection.TokenCiphertext = sealed
	connection.AccessTokenExpiresAt = refreshed.Expiry
	connection.LastRefreshedAt = s.now()
	connection.LastError = ""
	if _, err := s.store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		return "", err
	}
	metrics.GoogleOAuthFlowsTotal.WithLabelValues("refreshed").Inc()
	return refreshed.AccessToken, nil
}

// markReauthorizationRequired records that the stored credential no longer
// works, and erases it.
//
// Keeping a credential that Google has stopped honouring buys nothing and
// costs a stored secret. What the user needs is the status, so the UI can ask
// them to reconnect rather than the integration going quiet.
func (s *Service) markReauthorizationRequired(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection, reason string,
) error {
	connection.Status = domain.GoogleStatusReauthorizationRequired
	connection.TokenCiphertext = nil
	connection.AccessTokenExpiresAt = time.Time{}
	connection.LastError = reason
	saved, err := s.store.SaveGoogleConnection(ctx, scope, connection)
	if err != nil {
		return err
	}
	s.audit(ctx, scope, saved, EventConnectionReauthorizationRequired, "system")
	metrics.GoogleOAuthFlowsTotal.WithLabelValues("reauthorization_required").Inc()
	return fmt.Errorf("%w: google connection requires reauthorization (%s)",
		domain.ErrInvalidTransition, reason)
}

// The audit event types this service emits, re-exported from domain so a
// caller reading this file does not have to go looking for them.
const (
	EventConnectionAuthorized              = domain.EventGoogleConnectionAuthorized
	EventConnectionReauthorizationRequired = domain.EventGoogleConnectionReauthorizationRequired
	EventConnectionDisconnected            = domain.EventGoogleConnectionDisconnected
	EventConnectionPurged                  = domain.EventGoogleConnectionPurged
)

// audit records one lifecycle event.
//
// A failure to write it is logged rather than returned. That is a real
// trade-off and it goes this way deliberately: the alternative is failing a
// disconnect — leaving a user unable to withdraw access — because an audit
// row could not be written. The event is enqueued through the same
// transactional outbox the rest of the module uses, so the ordinary failure
// mode is delayed delivery, not loss.
func (s *Service) audit(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	eventType, actor string,
) {
	err := domain.EnqueueGoogleConnectionEvent(
		ctx, s.store, scope, connection, eventType, actor, connection.LastError, s.now())
	if err != nil {
		slog.Error("could not record google connection audit event",
			"event_type", eventType, "error", err)
	}
}
