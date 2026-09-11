package meta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/secrets"
)

var ErrNotConfigured = errors.New("meta: integration is not configured")
var ErrPermanent = errors.New("meta: permanent delivery failure")

const (
	ReasonAccessDenied      = "access_denied"
	ReasonProviderError     = "provider_error"
	ReasonInvalidRequest    = "invalid_request"
	ReasonInvalidState      = "invalid_state"
	ReasonExchangeFailed    = "exchange_failed"
	ReasonPermissionMissing = "permission_missing"
	ReasonStorageFailed     = "storage_failed"
	ReasonNotConfigured     = "not_configured"
	ReasonTokenUnavailable  = "token_unavailable"
	ReasonInvalidLead       = "invalid_lead"
)

type Service struct {
	store      domain.Store
	client     *Client
	sealer     *secrets.Sealer
	webBaseURL string
	now        func() time.Time
}

func NewService(store domain.Store, client *Client, sealer *secrets.Sealer, webBaseURL string) *Service {
	return &Service{store: store, client: client, sealer: sealer,
		webBaseURL: strings.TrimRight(webBaseURL, "/"), now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Configured() bool               { return s != nil && s.client != nil && s.sealer != nil }
func (s *Service) RequestedPermissions() []string { return Permissions() }

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func tokenBinding(tenantID, connectionID, kind string) []byte {
	return []byte("meta:" + kind + ":" + tenantID + ":" + connectionID)
}

func (s *Service) Start(ctx context.Context, scope domain.Scope, actorID, returnPath string) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	if strings.TrimSpace(actorID) == "" {
		return "", fmt.Errorf("%w: actor is required", domain.ErrInvalid)
	}
	if err := domain.ValidateGoogleReturnPath(returnPath); err != nil {
		return "", err
	}
	authorizationURL, state, expiry, err := s.client.Begin(s.now())
	if err != nil {
		return "", err
	}
	_, err = s.store.CreateMetaOAuthState(ctx, scope, &domain.MetaOAuthState{
		State: state, ActorID: actorID, ReturnPath: returnPath, CreatedAt: s.now(), ExpiresAt: expiry,
	})
	if err != nil {
		return "", err
	}
	_, _ = s.store.PurgeExpiredMetaOAuthStates(ctx, scope, s.now())
	return authorizationURL, nil
}

type CallbackResult struct {
	Connected                       bool
	Reason, ReturnPath, AccountName string
}

func (s *Service) Complete(ctx context.Context, state, code, providerError string) CallbackResult {
	if !s.Configured() {
		return CallbackResult{Reason: ReasonNotConfigured}
	}
	pending, scope, ok := s.consume(ctx, state)
	if !ok {
		return CallbackResult{Reason: ReasonInvalidState}
	}
	result := CallbackResult{ReturnPath: pending.ReturnPath}
	if providerError != "" {
		result.Reason = ReasonProviderError
		if providerError == "access_denied" {
			result.Reason = ReasonAccessDenied
		}
		return result
	}
	if strings.TrimSpace(code) == "" {
		result.Reason = ReasonInvalidRequest
		return result
	}
	token, err := s.client.Exchange(ctx, code)
	if err != nil {
		slog.Warn("Meta OAuth exchange failed", "error", err)
		result.Reason = ReasonExchangeFailed
		return result
	}
	profile, err := s.client.Profile(ctx, token.AccessToken)
	if err != nil {
		slog.Warn("Meta profile read failed", "error", err)
		result.Reason = ReasonProviderError
		return result
	}
	granted, err := s.client.GrantedPermissions(ctx, token.AccessToken)
	if err != nil {
		slog.Warn("Meta permission read failed", "error", err)
		result.Reason = ReasonProviderError
		return result
	}
	if missingPermissions(granted) {
		_ = s.client.Revoke(ctx, token.AccessToken)
		result.Reason = ReasonPermissionMissing
		return result
	}

	connectionID, createdAt := "", time.Time{}
	if existing, findErr := s.store.GetMetaConnection(ctx, scope); findErr == nil {
		connectionID, createdAt = existing.ID, existing.CreatedAt
	} else if !errors.Is(findErr, domain.ErrNotFound) {
		result.Reason = ReasonStorageFailed
		return result
	}
	if connectionID == "" {
		connectionID, err = randomID()
	}
	if err != nil {
		result.Reason = ReasonStorageFailed
		return result
	}
	sealed, err := s.sealer.Seal([]byte(token.AccessToken), tokenBinding(scope.TenantID, connectionID, "user"))
	if err != nil {
		result.Reason = ReasonStorageFailed
		return result
	}
	now := s.now()
	_, err = s.store.SaveMetaConnection(ctx, scope, &domain.MetaConnection{
		ID: connectionID, MetaUserID: profile.ID, MetaUserName: profile.Name,
		Status: domain.MetaStatusSelectionRequired, GrantedPermissions: granted,
		UserTokenCiphertext: sealed, AccessTokenExpiresAt: token.ExpiresAt,
		ConnectedBy: pending.ActorID, ConnectedAt: now, CreatedAt: createdAt,
	})
	if err != nil {
		slog.Error("Meta connection save failed", "error", err)
		result.Reason = ReasonStorageFailed
		return result
	}
	result.Connected, result.AccountName = true, profile.Name
	return result
}

func missingPermissions(granted []string) bool {
	have := make(map[string]bool, len(granted))
	for _, value := range granted {
		have[value] = true
	}
	for _, required := range requestedPermissions {
		if !have[required] {
			return true
		}
	}
	return false
}

func (s *Service) consume(ctx context.Context, state string) (*domain.MetaOAuthState, domain.Scope, bool) {
	if strings.TrimSpace(state) == "" {
		return nil, domain.Scope{}, false
	}
	tenantID, err := s.store.ResolveMetaOAuthStateTenant(ctx, state)
	if err != nil {
		return nil, domain.Scope{}, false
	}
	scope := domain.Scope{TenantID: tenantID}
	pending, err := s.store.ConsumeMetaOAuthState(ctx, scope, state, s.now())
	if err != nil {
		return nil, domain.Scope{}, false
	}
	return pending, scope, true
}

func (s *Service) Connection(ctx context.Context, scope domain.Scope) (*domain.MetaConnection, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	connection, err := s.store.GetMetaConnection(ctx, scope)
	if err != nil {
		return nil, err
	}
	if (connection.Status == domain.MetaStatusConnected || connection.Status == domain.MetaStatusSelectionRequired) &&
		!connection.AccessTokenExpiresAt.IsZero() && !s.now().Before(connection.AccessTokenExpiresAt) {
		connection.Status = domain.MetaStatusReauthorizationRequired
		connection.UserTokenCiphertext, connection.PageTokenCiphertext = nil, nil
		connection.LastError = ReasonTokenUnavailable
		return s.store.SaveMetaConnection(ctx, scope, connection)
	}
	return connection, nil
}

type BusinessView struct{ ID, Name string }
type PageView struct{ ID, Name string }
type FormView struct{ ID, Name, Status string }

func (s *Service) userToken(scope domain.Scope, connection *domain.MetaConnection) (string, error) {
	if connection == nil || len(connection.UserTokenCiphertext) == 0 {
		return "", domain.ErrNotFound
	}
	value, err := s.sealer.Open(connection.UserTokenCiphertext, tokenBinding(scope.TenantID, connection.ID, "user"))
	if err != nil {
		return "", fmt.Errorf("%w: user token could not be opened", ErrPermanent)
	}
	return string(value), nil
}

func (s *Service) pageToken(ref domain.MetaConnectionRef) (string, error) {
	value, err := s.sealer.Open(ref.PageTokenCiphertext, tokenBinding(ref.TenantID, ref.ConnectionID, "page"))
	if err != nil {
		return "", fmt.Errorf("%w: Page token could not be opened", ErrPermanent)
	}
	return string(value), nil
}

func (s *Service) Businesses(ctx context.Context, scope domain.Scope) ([]BusinessView, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	token, err := s.userToken(scope, connection)
	if err != nil {
		return nil, err
	}
	rows, err := s.client.Businesses(ctx, token)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, err
	}
	out := make([]BusinessView, 0, len(rows))
	for _, row := range rows {
		out = append(out, BusinessView{ID: row.ID, Name: row.Name})
	}
	return out, nil
}

func (s *Service) businessAndPages(ctx context.Context, scope domain.Scope, connection *domain.MetaConnection, businessID string) (*Business, []Page, error) {
	token, err := s.userToken(scope, connection)
	if err != nil {
		return nil, nil, err
	}
	businesses, err := s.client.Businesses(ctx, token)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, nil, err
	}
	var business *Business
	for i := range businesses {
		if businesses[i].ID == strings.TrimSpace(businessID) {
			business = &businesses[i]
			break
		}
	}
	if business == nil {
		return nil, nil, domain.ErrNotFound
	}
	pages, err := s.client.Pages(ctx, business.ID, token)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, nil, err
	}
	return business, pages, nil
}

func (s *Service) Pages(ctx context.Context, scope domain.Scope, businessID string) ([]PageView, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	_, pages, err := s.businessAndPages(ctx, scope, connection, businessID)
	if err != nil {
		return nil, err
	}
	out := make([]PageView, 0, len(pages))
	for _, page := range pages {
		out = append(out, PageView{ID: page.ID, Name: page.Name})
	}
	return out, nil
}

func (s *Service) Forms(ctx context.Context, scope domain.Scope, businessID, pageID string) ([]FormView, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	_, pages, err := s.businessAndPages(ctx, scope, connection, businessID)
	if err != nil {
		return nil, err
	}
	var selected *Page
	for i := range pages {
		if pages[i].ID == pageID {
			selected = &pages[i]
			break
		}
	}
	if selected == nil {
		return nil, domain.ErrNotFound
	}
	forms, err := s.client.Forms(ctx, selected.ID, selected.AccessToken)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, err
	}
	out := make([]FormView, 0, len(forms))
	for _, form := range forms {
		out = append(out, FormView{ID: form.ID, Name: form.Name, Status: form.Status})
	}
	return out, nil
}

func (s *Service) SelectForm(ctx context.Context, scope domain.Scope, businessID, pageID, formID string) (*domain.MetaConnection, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	business, pages, err := s.businessAndPages(ctx, scope, connection, businessID)
	if err != nil {
		return nil, err
	}
	var page *Page
	for i := range pages {
		if pages[i].ID == pageID {
			page = &pages[i]
			break
		}
	}
	if page == nil {
		return nil, domain.ErrNotFound
	}
	forms, err := s.client.Forms(ctx, page.ID, page.AccessToken)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, err
	}
	var form *Form
	for i := range forms {
		if forms[i].ID == formID {
			form = &forms[i]
			break
		}
	}
	if form == nil {
		return nil, domain.ErrNotFound
	}
	if err := s.client.SubscribePage(ctx, page.ID, page.AccessToken); err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return nil, err
	}
	sealed, err := s.sealer.Seal([]byte(page.AccessToken), tokenBinding(scope.TenantID, connection.ID, "page"))
	if err != nil {
		return nil, err
	}
	connection.Status = domain.MetaStatusConnected
	connection.BusinessID, connection.BusinessName = business.ID, business.Name
	connection.PageID, connection.PageName = page.ID, page.Name
	connection.FormID, connection.FormName = form.ID, form.Name
	connection.PageTokenCiphertext = sealed
	connection.ConnectedAt = s.now()
	connection.DisconnectedAt = time.Time{}
	connection.LastError = ""
	return s.store.SaveMetaConnection(ctx, scope, connection)
}

func (s *Service) Disconnect(ctx context.Context, scope domain.Scope, purge bool) (*domain.MetaConnection, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	if token, openErr := s.userToken(scope, connection); openErr == nil {
		_ = s.client.Revoke(ctx, token)
	}
	connection.UserTokenCiphertext, connection.PageTokenCiphertext = nil, nil
	connection.Status = domain.MetaStatusDisconnected
	connection.DisconnectedAt = s.now()
	connection.LastError = ""
	stored, err := s.store.SaveMetaConnection(ctx, scope, connection)
	if err != nil {
		return nil, err
	}
	if purge {
		if err := s.store.DeleteMetaConnection(ctx, scope, stored.ID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return stored, nil
}

type ImportResult struct{ Examined, Created, Duplicates, Rejected int }

func (s *Service) Import(ctx context.Context, scope domain.Scope, limit int) (ImportResult, error) {
	connection, err := s.Connection(ctx, scope)
	if err != nil {
		return ImportResult{}, err
	}
	if connection.Status != domain.MetaStatusConnected {
		return ImportResult{}, domain.ErrConflict
	}
	ref := domain.MetaConnectionRef{TenantID: scope.TenantID, ConnectionID: connection.ID,
		PageTokenCiphertext: connection.PageTokenCiphertext, PageID: connection.PageID,
		FormID: connection.FormID, Status: connection.Status}
	pageToken, err := s.pageToken(ref)
	if err != nil {
		return ImportResult{}, err
	}
	leads, err := s.client.Leads(ctx, connection.FormID, pageToken, limit)
	if err != nil {
		s.handleAuthFailure(ctx, scope, connection, err)
		return ImportResult{}, err
	}
	result := ImportResult{Examined: len(leads)}
	for _, providerLead := range leads {
		created, ingestErr := s.ingestProviderLead(ctx, ref, pageToken, providerLead)
		if ingestErr != nil {
			if errors.Is(ingestErr, ErrPermanent) {
				result.Rejected++
				continue
			}
			return result, ingestErr
		}
		if created {
			result.Created++
		} else {
			result.Duplicates++
		}
	}
	connection.LastImportAt, connection.LastError = s.now(), ""
	_, err = s.store.SaveMetaConnection(ctx, scope, connection)
	return result, err
}

func (s *Service) ProcessWebhookLead(ctx context.Context, ref domain.MetaConnectionRef, providerLeadID, rawEvent string) (bool, error) {
	if !s.Configured() {
		return false, ErrNotConfigured
	}
	pageToken, err := s.pageToken(ref)
	if err != nil {
		return false, err
	}
	providerLead, err := s.client.Lead(ctx, providerLeadID, pageToken)
	if err != nil {
		scope := domain.Scope{TenantID: ref.TenantID}
		if connection, findErr := s.store.GetMetaConnection(ctx, scope); findErr == nil {
			s.handleAuthFailure(ctx, scope, connection, err)
		}
		if IsAuthorizationError(err) {
			return false, fmt.Errorf("%w: authorization rejected", ErrPermanent)
		}
		return false, err
	}
	created, err := s.ingestProviderLead(ctx, ref, pageToken, providerLead)
	if err != nil {
		return false, err
	}
	scope := domain.Scope{TenantID: ref.TenantID}
	if connection, findErr := s.store.GetMetaConnection(ctx, scope); findErr == nil {
		connection.LastWebhookAt, connection.LastError = s.now(), ""
		_, _ = s.store.SaveMetaConnection(ctx, scope, connection)
	}
	_ = rawEvent // the provider Lead JSON below is the canonical stored payload.
	return created, nil
}

func (s *Service) ingestProviderLead(ctx context.Context, ref domain.MetaConnectionRef, pageToken string, providerLead Lead) (bool, error) {
	lead, err := mapProviderLead(providerLead)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	attribution, attrErr := s.client.Attribution(ctx, providerLead.AdID, pageToken)
	if attrErr != nil {
		slog.Warn("Meta ad attribution lookup failed", "ad_id_present", providerLead.AdID != "", "error", attrErr)
	}
	raw, _ := json.Marshal(providerLead)
	providerCreatedAt, _ := time.Parse(time.RFC3339, providerLead.CreatedTime)
	row := &domain.MetaLeadAttribution{
		ConnectionID: ref.ConnectionID, ProviderLeadID: providerLead.ID,
		Platform: normalizePlatform(providerLead.Platform), PageID: ref.PageID, FormID: ref.FormID,
		AdID: attribution.AdID, AdName: attribution.AdName, AdSetID: attribution.AdSetID,
		AdSetName: attribution.AdSetName, CampaignID: attribution.CampaignID,
		CampaignName: attribution.CampaignName, ProviderCreatedAt: providerCreatedAt,
		CapturedAt: s.now(), RawPayload: string(raw),
	}
	_, created, err := s.store.CreateMetaCapturedLead(ctx, domain.Scope{TenantID: ref.TenantID}, lead, row)
	return created, err
}

func mapProviderLead(in Lead) (*domain.Lead, error) {
	values := make(map[string]string, len(in.FieldData))
	for _, field := range in.FieldData {
		key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(field.Name), " ", "_"), "-", "_"))
		if len(field.Values) > 0 {
			values[key] = strings.TrimSpace(field.Values[0])
		}
	}
	first, last := values["first_name"], values["last_name"]
	if first == "" && last == "" {
		parts := strings.Fields(values["full_name"])
		if len(parts) > 0 {
			first = parts[0]
			if len(parts) > 1 {
				last = strings.Join(parts[1:], " ")
			}
		}
	}
	phone := firstNonBlank(values["phone_number"], values["phone"], values["mobile_number"])
	email := firstNonBlank(values["email"], values["email_address"])
	company := firstNonBlank(values["company_name"], values["company"], values["business_name"])
	platform := normalizePlatform(in.Platform)
	source := "Meta Lead Ads"
	if platform != "" {
		source = platform + " Lead Ads"
	}
	lead := &domain.Lead{FirstName: first, LastName: last, Phone: phone,
		ContactEmail: email, Company: company, Source: source}
	if err := lead.Validate(); err != nil {
		return nil, err
	}
	return lead, nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizePlatform(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ig", "instagram":
		return "Instagram"
	case "fb", "facebook":
		return "Facebook"
	default:
		return "Meta"
	}
}

func (s *Service) handleAuthFailure(ctx context.Context, scope domain.Scope, connection *domain.MetaConnection, err error) {
	if !IsAuthorizationError(err) || connection == nil {
		return
	}
	connection.Status = domain.MetaStatusReauthorizationRequired
	connection.UserTokenCiphertext, connection.PageTokenCiphertext = nil, nil
	connection.LastError = ReasonTokenUnavailable
	_, saveErr := s.store.SaveMetaConnection(ctx, scope, connection)
	if saveErr != nil {
		slog.Error("could not mark Meta connection for reauthorization", "error", saveErr)
	}
}
