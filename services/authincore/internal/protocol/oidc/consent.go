package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/authincore/authincore/internal/api/interceptor"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
)

// IdentityFromRequest is satisfied by *interceptor.AuthnInterceptor; injected to
// prevent consent-request hijacking (user A approving user B's consent).
type IdentityFromRequest interface {
	ResolveHTTP(r *http.Request) (*http.Request, bool)
}

// ConsentRequest represents a pending scope-approval request.
type ConsentRequest struct {
	ID                  string     `json:"id"`
	ClientID            string     `json:"client_id"`
	ClientName          string     `json:"client_name"`
	IdentityID          uuid.UUID  `json:"identity_id"`
	OrgID               uuid.UUID  `json:"org_id"`
	Scopes              []string   `json:"scopes"`
	RedirectURI         string     `json:"redirect_uri"`
	State               string     `json:"state"`
	CodeChallenge       string     `json:"-"`
	CodeChallengeMethod string     `json:"-"`
	ExpiresAt           time.Time  `json:"expires_at"`
	Granted             bool       `json:"granted"`
	RedeemedAt          *time.Time `json:"redeemed_at,omitempty"` // set after auth code is issued; prevents replay
}

// ConsentStore persists consent requests and remembered grants.
type ConsentStore interface {
	StoreRequest(ctx context.Context, req *ConsentRequest) error
	GetRequest(ctx context.Context, id string) (*ConsentRequest, error)
	UpdateRequest(ctx context.Context, req *ConsentRequest) error
	// RememberedScopes returns previously granted scopes for this identity+client.
	RememberedScopes(ctx context.Context, orgID uuid.UUID, identityID uuid.UUID, clientID string) ([]string, error)
	RememberGrant(ctx context.Context, orgID, identityID uuid.UUID, clientID string, scopes []string) error
}

const errInternalError = "internal error"

// ConsentHandler serves GET /oauth2/consent (show UI) and POST /oauth2/consent (submit decision).
type ConsentHandler struct {
	store ConsentStore
	authn IdentityFromRequest // optional; when set, enforces that the submitter owns the consent request
}

func NewConsentHandler(store ConsentStore) *ConsentHandler {
	return &ConsentHandler{store: store}
}

// WithAuthn enables identity verification on SubmitConsent to prevent consent hijacking.
func (h *ConsentHandler) WithAuthn(authn IdentityFromRequest) *ConsentHandler {
	h.authn = authn
	return h
}

// ShowConsent handles GET /oauth2/consent?consent_id=...
// In a real deployment this renders an HTML page; here it returns a JSON payload
// that a SPA/SSR frontend can use to render the consent screen.
func (h *ConsentHandler) ShowConsent(w http.ResponseWriter, r *http.Request) {
	r, err := h.resolveAuthedRequest(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.URL.Query().Get("consent_id")
	if id == "" {
		http.Error(w, "consent_id required", http.StatusBadRequest)
		return
	}
	req, err := h.store.GetRequest(r.Context(), id)
	if err != nil {
		if domainerr.IsNotFound(err) {
			http.Error(w, "consent request not found or expired", http.StatusNotFound)
			return
		}
		http.Error(w, errInternalError, http.StatusInternalServerError)
		return
	}
	if time.Now().After(req.ExpiresAt) {
		http.Error(w, "consent request expired", http.StatusGone)
		return
	}
	if err := h.checkOwnership(r, req.IdentityID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(req)
}

type consentDecision struct {
	ConsentID string   `json:"consent_id"`
	Grant     bool     `json:"grant"`
	Remember  bool     `json:"remember"`
	Scopes    []string `json:"scopes"`
}

// decodeConsentDecision accepts either a JSON body (the API contract this
// handler documents) or a application/x-www-form-urlencoded body (a plain
// HTML <form method="POST"> submission, which is what the consent screen
// itself must use — completing consent ends in a same-origin redirect to
// /oauth2/authorize and then a cross-origin redirect to the client's
// redirect_uri, and only a real browser navigation follows both hops
// correctly; a fetch()-driven submission cannot safely do that across
// origins). scopes arrives repeated ("scopes=a&scopes=b") in form encoding.
func decodeConsentDecision(r *http.Request) (consentDecision, error) {
	var body consentDecision
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return body, err
		}
		body.ConsentID = r.PostFormValue("consent_id")
		body.Grant = r.PostFormValue("grant") == "true"
		body.Remember = r.PostFormValue("remember") == "true"
		body.Scopes = r.PostForm["scopes"]
		return body, nil
	}
	err := json.NewDecoder(r.Body).Decode(&body)
	return body, err
}

// SubmitConsent handles POST /oauth2/consent. Body is either JSON
// ({ "consent_id": "...", "grant": true|false, "remember": true|false,
// "scopes": [...] }) or an equivalent form-urlencoded submission.
func (h *ConsentHandler) SubmitConsent(w http.ResponseWriter, r *http.Request) {
	r, err := h.resolveAuthedRequest(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := decodeConsentDecision(r)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	req, err := h.store.GetRequest(r.Context(), body.ConsentID)
	if err != nil {
		if domainerr.IsNotFound(err) {
			http.Error(w, "consent request not found", http.StatusNotFound)
			return
		}
		http.Error(w, errInternalError, http.StatusInternalServerError)
		return
	}
	if time.Now().After(req.ExpiresAt) {
		http.Error(w, "consent request expired", http.StatusGone)
		return
	}
	if err := h.checkOwnership(r, req.IdentityID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if !body.Grant {
		redirectURL := fmt.Sprintf("%s?error=access_denied&state=%s", req.RedirectURI, url.QueryEscape(req.State))
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	approved := intersectScopes(req.Scopes, body.Scopes)
	req.Granted = true
	req.Scopes = approved
	if err := h.store.UpdateRequest(r.Context(), req); err != nil {
		http.Error(w, errInternalError, http.StatusInternalServerError)
		return
	}
	if body.Remember {
		_ = h.store.RememberGrant(r.Context(), req.OrgID, req.IdentityID, req.ClientID, approved)
	}
	redirectURL := buildConsentResumeURL(req)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// buildConsentResumeURL restores every authorize parameter from the durable
// consent request. A public PKCE client cannot safely continue with only a
// consent_id: the callback must still validate the original client, redirect,
// state, organization, and code challenge.
func buildConsentResumeURL(req *ConsentRequest) string {
	v := url.Values{
		"consent_id":    {req.ID},
		"response_type": {"code"},
		"client_id":     {req.ClientID},
		"redirect_uri":  {req.RedirectURI},
		"state":         {req.State},
		"scope":         {strings.Join(req.Scopes, " ")},
		"org_id":        {req.OrgID.String()},
	}
	if req.CodeChallenge != "" {
		v.Set("code_challenge", req.CodeChallenge)
		v.Set("code_challenge_method", req.CodeChallengeMethod)
	}
	return "/oauth2/authorize?" + v.Encode()
}

func (h *ConsentHandler) resolveAuthedRequest(r *http.Request) (*http.Request, error) {
	if h.authn == nil {
		return r, nil
	}
	authedR, ok := h.authn.ResolveHTTP(r)
	if !ok {
		return nil, fmt.Errorf("unauthenticated")
	}
	return authedR, nil
}

func (h *ConsentHandler) checkOwnership(r *http.Request, ownerID uuid.UUID) error {
	if h.authn == nil {
		return nil
	}
	claims, ok := interceptor.ClaimsFromContext(r.Context())
	if !ok || claims.Subject != ownerID.String() {
		return fmt.Errorf("subject mismatch")
	}
	return nil
}

func intersectScopes(requested, approved []string) []string {
	set := make(map[string]bool, len(approved))
	for _, s := range approved {
		set[s] = true
	}
	var out []string
	for _, s := range requested {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}
