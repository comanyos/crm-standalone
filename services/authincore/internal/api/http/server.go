package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/authincore/authincore/internal/actions"
	"github.com/authincore/authincore/internal/api/interceptor"
	"github.com/authincore/authincore/internal/api/middleware"
	"github.com/authincore/authincore/internal/authz"
	"github.com/authincore/authincore/internal/blueprint"
	"github.com/authincore/authincore/internal/command"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/authincore/authincore/internal/fga"
	"github.com/authincore/authincore/internal/flow"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/authincore/authincore/internal/metrics"
	"github.com/authincore/authincore/internal/platform"
	"github.com/authincore/authincore/internal/policy/engine"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/authincore/authincore/internal/protocol/saml"
	"github.com/authincore/authincore/internal/query"
	"github.com/authincore/authincore/internal/scim"
	"github.com/authincore/authincore/internal/serviceaccount"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/authincore/authincore/internal/token"
	"github.com/authincore/authincore/internal/tracing"
	"github.com/authincore/authincore/internal/util"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	errInvalidID     = "invalid id"
	errInvalidBody   = "invalid body"
	errInvalidFlowID = "invalid flow id"
)

// Config holds server settings.
type Config struct {
	Addr string
}

// Pinger lets the readyz handler probe a dependency.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps groups all server dependencies to avoid a >7-param constructor.
type Deps struct {
	OIDC            *oidc.Provider
	Resolver        tenancy.Resolver
	Authn           *interceptor.AuthnInterceptor
	Commands        *command.Commands
	Queries         *query.Queries
	Flows           *flow.Manager
	TokenSvc        *token.Service
	RateLimiter     middleware.RateLimiter  // optional; nil disables rate limiting
	TrustedProxies  []*net.IPNet            // optional; forwarded IP headers are ignored when empty
	DBPinger        Pinger                  // optional; used by readyz
	CachePinger     Pinger                  // optional; used by readyz
	MetricsPool     *pgxpool.Pool           // optional; exports aggregate pool stats on /metrics
	ConsentHandler  *oidc.ConsentHandler    // optional; nil disables consent screen
	SAMLSP          *saml.SP                // optional; nil disables SAML SP routes
	SAMLIdP         *saml.IdP               // optional; nil disables SAML IdP routes
	SCIMHandler     *scim.Handler           // optional; nil disables SCIM provisioning
	FGAHandler      *fga.Handler            // optional; nil disables fine-grained authz routes
	FGAService      *fga.Service            // optional; nil disables the admin FGA tuple viewer/editor
	ServiceAccSvc   *serviceaccount.Service // optional; nil disables SA endpoints
	InvitationSvc   *invitation.Service     // optional; nil disables invitation endpoints
	AuthzEnforcer   *authz.Enforcer         // optional; nil disables RBAC enforcement
	ClaimsResolver  *authz.ClaimsResolver   // optional; nil mints tokens with no roles or scopes
	PolicyEngine    *engine.Engine          // optional; nil disables policy checks at login
	ActionsRunner   *actions.Runner         // optional; nil disables JS action hooks
	BlueprintSvc    *blueprint.Applier      // optional; nil disables blueprint API
	OIDCProviders   OIDCProviderManager     // optional; nil disables OIDC provider CRUD
	WebhookStore    WebhookManager          // optional; nil disables webhook CRUD
	RoleManager     RoleManager             // optional; nil disables role CRUD
	AppManager      ApplicationManager      // optional; nil disables application CRUD
	AdminToken      string                  // optional; if set, admin-only endpoints accept Bearer <token>
	HostedUI        fs.FS                   // optional; nil disables serving the hosted login UI
	CORSOrigins     []string                // optional; browser origins allowed to call the API cross-origin
	PlatformAdapter *platform.Adapter       // optional; nil = /platform/v1 routes not mounted
	Log             *slog.Logger
	enterpriseDeps  // SSO enforcement, branding (enterprise build only)
}

// Server is the main HTTP server.
type Server struct {
	cfg  Config
	deps Deps
}

func NewServer(cfg Config, deps Deps) *Server {
	return &Server{cfg: cfg, deps: deps}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// --- Health + observability ---
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.Handle("GET /metrics", s.requireAdminAuth(metrics.Handler(s.deps.MetricsPool).ServeHTTP))

	// --- OIDC discovery ---
	mux.HandleFunc("GET /.well-known/openid-configuration", s.deps.OIDC.WellKnownHandler)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.deps.OIDC.WellKnownHandler)

	// --- OAuth2 endpoints ---
	mux.HandleFunc("GET /oauth2/authorize", s.deps.OIDC.AuthorizeHandler)
	mux.HandleFunc("POST /oauth2/token", s.deps.OIDC.TokenHandler)
	mux.HandleFunc("GET /oauth2/userinfo", s.deps.OIDC.UserInfoHandler)
	mux.HandleFunc("POST /oauth2/userinfo", s.deps.OIDC.UserInfoHandler)
	mux.HandleFunc("GET /oauth2/keys", s.deps.OIDC.JWKSHandler)

	// --- Self-service flow endpoints (public) ---
	mux.HandleFunc("GET /self-service/login", s.handleInitFlow(domflow.TypeLogin))
	mux.HandleFunc("GET /self-service/registration", s.handleInitFlow(domflow.TypeRegistration))
	mux.HandleFunc("GET /self-service/recovery", s.handleInitFlow(domflow.TypeRecovery))
	mux.HandleFunc("GET /self-service/verification", s.handleInitFlow(domflow.TypeVerification))
	mux.HandleFunc("GET /self-service/settings", s.handleInitSettingsFlow)
	mux.HandleFunc("GET /self-service/flows", s.handleGetFlow)
	loginLimit, regLimit := int64(5), int64(3)
	if os.Getenv("DEV_MODE") == "true" {
		loginLimit, regLimit = 100, 100
	}
	mux.Handle("POST /self-service/login", s.rateLimited("login", loginLimit, time.Minute, s.handleSubmitFlow))
	mux.Handle("POST /self-service/registration", s.rateLimited("registration", regLimit, time.Minute, s.handleSubmitFlow))
	mux.Handle("POST /self-service/recovery", s.rateLimited("recovery", 3, time.Minute, s.handleSubmitFlow))
	mux.HandleFunc("POST /self-service/verification", s.handleSubmitFlow)
	mux.HandleFunc("POST /self-service/settings", s.handleSubmitSettingsFlow)

	// --- Identity admin API (requires auth) ---
	mux.HandleFunc("POST /api/v1/identities", s.requirePermission("identity", "write", s.handleCreateIdentity))
	mux.HandleFunc("GET /api/v1/identities/{id}", s.requirePermission("identity", "read", s.handleGetIdentity))
	mux.HandleFunc("PATCH /api/v1/identities/{id}", s.requirePermission("identity", "write", s.handleUpdateIdentity))
	mux.HandleFunc("DELETE /api/v1/identities/{id}", s.requirePermission("identity", "delete", s.handleDeleteIdentity))
	mux.HandleFunc("GET /api/v1/identities", s.requirePermission("identity", "read", s.handleListIdentities))
	mux.HandleFunc("GET /api/v1/identities/search", s.requirePermission("identity", "read", s.handleSearchIdentities))

	// --- Admin API: orgs / projects / apps (requires auth) ---
	mux.HandleFunc("POST /api/v1/admin/orgs", s.requirePermission("org", "write", s.handleCreateOrg))
	mux.HandleFunc("GET /api/v1/admin/orgs", s.requirePermission("org", "read", s.handleListOrgs))
	mux.HandleFunc("POST /api/v1/admin/orgs/{org_id}/projects", s.requirePermission("project", "write", s.handleCreateProject))
	mux.HandleFunc("GET /api/v1/admin/orgs/{org_id}/projects", s.requirePermission("project", "read", s.handleListProjects))
	mux.HandleFunc("POST /api/v1/admin/projects/{project_id}/apps", s.requirePermission("application", "write", s.handleCreateApp))
	mux.HandleFunc("GET /api/v1/admin/projects/{project_id}/apps", s.requirePermission("application", "read", s.handleListApps))

	// --- Blueprint API ---
	if s.deps.BlueprintSvc != nil {
		mux.HandleFunc("POST /api/v1/admin/blueprints", s.requirePermission("blueprint", "write", s.handleApplyBlueprint))
		mux.HandleFunc("GET /api/v1/admin/blueprints", s.requirePermission("blueprint", "read", s.handleListBlueprints))
	}

	// --- OIDC provider management ---
	mux.HandleFunc("GET /api/v1/oidc-providers", s.requirePermission("provider", "read", s.handleListOIDCProviders))
	mux.HandleFunc("POST /api/v1/oidc-providers", s.requirePermission("provider", "write", s.handleCreateOIDCProvider))
	mux.HandleFunc("DELETE /api/v1/oidc-providers/{id}", s.requirePermission("provider", "delete", s.handleDeleteOIDCProvider))

	// --- Webhook management ---
	mux.HandleFunc("GET /api/v1/webhooks", s.requirePermission("webhook", "read", s.handleListWebhooks))
	mux.HandleFunc("POST /api/v1/webhooks", s.requirePermission("webhook", "write", s.handleCreateWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", s.requirePermission("webhook", "delete", s.handleDeleteWebhook))

	// --- Role management ---
	if s.deps.RoleManager != nil {
		mux.HandleFunc("GET /api/v1/roles", s.requirePermission("role", "read", s.handleListRoles))
		mux.HandleFunc("POST /api/v1/roles", s.requirePermission("role", "write", s.handleCreateRole))
		mux.HandleFunc("DELETE /api/v1/roles/{id}", s.requirePermission("role", "delete", s.handleDeleteRole))
		mux.HandleFunc("POST /api/v1/roles/assign", s.requirePermission("role", "assign", s.handleAssignRole))
		mux.HandleFunc("POST /api/v1/roles/unassign", s.requirePermission("role", "assign", s.handleUnassignRole))
	}

	// --- Application (OIDC client) management ---
	if s.deps.AppManager != nil {
		mux.HandleFunc("GET /api/v1/applications", s.requirePermission("application", "read", s.handleListApplications))
		mux.HandleFunc("POST /api/v1/applications", s.requirePermission("application", "write", s.handleCreateApplication))
		mux.HandleFunc("DELETE /api/v1/applications/{id}", s.requirePermission("application", "delete", s.handleDeleteApplication))
	}

	// --- OIDC social login callback ---
	mux.HandleFunc("GET /self-service/oidc/callback", s.handleOIDCCallback)

	// --- Session API (requires auth) ---
	mux.HandleFunc("GET /api/v1/sessions/me", s.handleWhoAmI)
	mux.HandleFunc("DELETE /api/v1/sessions/me", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.requireAuth(s.handleGetSession))
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", s.requireAuth(s.handleRevokeSession))
	mux.HandleFunc("GET /api/v1/identities/{id}/sessions", s.requireAuth(s.handleListSessions))
	mux.HandleFunc("DELETE /api/v1/identities/{id}/sessions", s.requireAuth(s.handleRevokeAllSessions))

	// --- OAuth2 revocation + introspection ---
	mux.HandleFunc("POST /oauth2/revoke", s.deps.OIDC.RevokeHandler)
	mux.HandleFunc("POST /oauth2/introspect", s.deps.OIDC.IntrospectHandler)

	// --- Consent screen ---
	if s.deps.ConsentHandler != nil {
		mux.HandleFunc("GET /oauth2/consent", s.consentScreenOrData)
		mux.HandleFunc("POST /oauth2/consent", s.deps.ConsentHandler.SubmitConsent)
	}

	// Enterprise routes (SAML, SCIM, branding, SSO enforcement, audit export)
	// are registered by registerEnterpriseRoutes, which is build-tag-selected:
	// only the enterprise build compiles them in — the OSS build's variant is
	// a no-op.
	s.registerEnterpriseRoutes(mux)

	// --- Platform contract surface (shared /platform/v1/*) ---
	// When the PlatformAdapter is wired, mount the shell-facing routes behind
	// AuthinCore's own auth middleware so the suite shell treats AuthinCore as
	// a first-class module (sidebar + health-map + Copilot).
	if s.deps.PlatformAdapter != nil {
		mux.Handle("GET /platform/v1/manifest", s.requireAuth(s.handlePlatformManifest))
		mux.Handle("GET /platform/v1/health", s.requireAuth(s.handlePlatformHealth))
		mux.Handle("GET /platform/v1/actions", s.requireAuth(s.handlePlatformActions))
		mux.Handle("POST /platform/v1/actions/invoke", s.requireAuth(s.handlePlatformInvoke))
	}

	// --- Fine-grained authorization ---
	// /authz plane: app/runtime permission checks. Authenticated; org is derived
	// from the request's tenant/token context, never from the body.
	if s.deps.FGAHandler != nil {
		s.deps.FGAHandler.Routes(mux, "/authz", s.requireAuth, s.orgFromAuthedRequest)
	}
	// Authenticated admin plane: tuple viewer/editor in the admin UI. Org is
	// derived from the tenancy context inside each handler, never the body.
	if s.deps.FGAService != nil {
		mux.HandleFunc("GET /api/v1/admin/fga/tuples", s.requirePermission("fga", "read", s.handleFGAListTuples))
		mux.HandleFunc("POST /api/v1/admin/fga/tuples", s.requirePermission("fga", "write", s.handleFGAWriteTuple))
		mux.HandleFunc("DELETE /api/v1/admin/fga/tuples", s.requirePermission("fga", "delete", s.handleFGADeleteTuple))
		mux.HandleFunc("POST /api/v1/admin/fga/check", s.requirePermission("fga", "read", s.handleFGACheck))
		mux.HandleFunc("GET /api/v1/admin/fga/model", s.requirePermission("fga", "read", s.handleFGAModel))
	}

	// --- Service accounts ---
	if s.deps.ServiceAccSvc != nil {
		mux.HandleFunc("POST /api/v1/service-accounts", s.requirePermission("service-account", "write", s.handleCreateServiceAccount))
		mux.Handle("POST /api/v1/service-accounts/token", s.rateLimited("service-account-token", 10, time.Minute, s.handleServiceAccountToken))
	}

	// --- Invitations ---
	if s.deps.InvitationSvc != nil {
		mux.HandleFunc("POST /api/v1/invitations", s.requirePermission("invitation", "write", s.handleCreateInvitation))
		mux.HandleFunc("GET /api/v1/orgs/{org_id}/invitations", s.requirePermission("invitation", "read", s.handleListInvitations))
		mux.HandleFunc("GET /invitations/{token}", s.deps.InvitationSvc.GetHandler)            // public: user follows email link
		mux.HandleFunc("POST /invitations/{token}/accept", s.deps.InvitationSvc.AcceptHandler) // public: user accepts invite
	}

	// --- Hosted login UI (SPA, catch-all fallback) ---
	// Registered last: net/http's ServeMux prefers the more specific API,
	// /oauth2, /self-service, /saml, /scim and /.well-known patterns above, so
	// only unmatched paths (/, /login, /assets/...) reach the bundle.
	if s.deps.HostedUI != nil {
		mux.Handle("/", s.hostedUIHandler(s.deps.HostedUI))
	}

	mw := []func(http.Handler) http.Handler{
		metrics.Middleware,
		// Outside SecurityHeaders and CSRF so a request rejected by either is
		// still logged — a login that fails at the CSRF check is otherwise
		// indistinguishable from one that never arrived.
		middleware.RequestLog(s.deps.Log),
		middleware.SecurityHeaders,
		middleware.BrowserCSRF(s.deps.CORSOrigins),
	}
	if len(s.deps.CORSOrigins) > 0 {
		// Outside SecurityHeaders so preflight responses carry them too.
		mw = append(mw, middleware.CORS(s.deps.CORSOrigins))
	}
	chain := middleware.Chain(mw...)
	var handler http.Handler = chain(mux)
	if s.deps.Resolver != nil {
		handler = tenancy.Middleware(s.deps.Resolver)(handler)
	}
	// Keep the correlation field at the outermost application ingress so
	// tenancy resolution and every downstream handler share one request ID.
	handler = tracing.HTTPMiddleware(handler)
	handler = middleware.RequestID(handler)

	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.deps.Log.Info("HTTP server starting", "addr", s.cfg.Addr)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.deps.Log.Info("HTTP server shutting down, draining in-flight requests", "timeout", "10s")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		s.deps.Log.Info("HTTP server stopped")
		return nil
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}
}

// --- Flow handlers ---

func (s *Server) handleInitFlow(flowType domflow.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := orgIDFromRequest(r)
		returnTo := util.SafeReturnTo(r.URL.Query().Get("return_to"))

		f, err := s.deps.Flows.Create(r.Context(), flowType, orgID, returnTo)
		if err != nil {
			httpError(w, err, s.deps.Log)
			return
		}
		writeJSON(w, http.StatusOK, f)
	}
}

func (s *Server) handleGetFlow(w http.ResponseWriter, r *http.Request) {
	flowID, err := uuid.Parse(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, errInvalidFlowID, http.StatusBadRequest)
		return
	}
	f, err := s.deps.Flows.Get(r.Context(), flowID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleSubmitFlow(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracing.Start(r.Context(), "http.submit_flow")
	defer span.End()
	r = r.WithContext(ctx)

	flowID, err := uuid.Parse(r.URL.Query().Get("flow"))
	if err != nil {
		writeError(w, errInvalidFlowID, http.StatusBadRequest)
		return
	}
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid json body", http.StatusBadRequest)
		return
	}
	var req struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		writeError(w, "method required", http.StatusBadRequest)
		return
	}

	if s.checkSSOEnforcement(w, r, req.Method) {
		return
	}

	result, err := s.deps.Flows.Submit(r.Context(), flowID, req.Method, req.Body)
	if err != nil {
		metrics.LoginTotal.WithLabelValues(s.safeFlowMethod(req.Method), "failure").Inc()
		httpError(w, err, s.deps.Log)
		return
	}
	if !result.Done {
		writeJSON(w, http.StatusUnprocessableEntity, result.Flow)
		return
	}

	if blocked := s.runPostLoginChecks(w, r, result, req.Method); blocked {
		return
	}
}

// runPostLoginChecks runs policy evaluation, actions, and token minting after a
// successful flow submission. Returns true if the request was blocked and a
// response was already written.
func (s *Server) runPostLoginChecks(w http.ResponseWriter, r *http.Request, result *flow.SubmitResult, method string) bool {
	orgID := orgIDFromRequest(r)

	if s.deps.PolicyEngine != nil && result.IdentityID != nil {
		ec := engine.EvalContext{
			IdentityID: result.IdentityID,
			OrgID:      orgID,
			IP:         r.RemoteAddr,
			UserAgent:  r.UserAgent(),
		}
		res, err := s.deps.PolicyEngine.EvalBinding(r.Context(), orgID, "flow.login.post", ec)
		if err != nil {
			s.deps.Log.Error("policy evaluation unavailable at login", "err", err)
			writeError(w, "authentication policy temporarily unavailable", http.StatusServiceUnavailable)
			return true
		} else if !res.Passing {
			writeError(w, res.Message, http.StatusForbidden)
			return true
		}
	}

	if s.deps.ActionsRunner != nil && result.IdentityID != nil {
		payload := map[string]any{"identity_id": result.IdentityID.String(), "method": method}
		if _, err := s.deps.ActionsRunner.Fire(r.Context(), orgID, "post-login", payload); err != nil {
			s.deps.Log.Error("required post-login action failed", "err", err)
			writeError(w, "post-login policy action failed", http.StatusServiceUnavailable)
			return true
		}
	}

	accessToken, err := s.mintSessionCookie(w, r, *result.IdentityID, method)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return true
	}

	refreshToken, expiresAt, err := s.deps.TokenSvc.MintRefreshToken(r.Context())
	if err != nil {
		httpError(w, err, s.deps.Log)
		return true
	}

	metrics.LoginTotal.WithLabelValues(s.safeFlowMethod(method), "success").Inc()
	metrics.TokensIssued.WithLabelValues("login").Inc()
	metrics.ActiveSessions.Inc()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt,
		"token_type":    "Bearer",
		"identity_id":   result.IdentityID,
	})
	return false
}

// --- Settings flow handlers (require auth) ---

// handleInitSettingsFlow handles GET /self-service/settings.
// Requires authentication — resolves the caller's identity and embeds it into
// the flow so strategies can act on behalf of the correct user.
func (s *Server) handleInitSettingsFlow(w http.ResponseWriter, r *http.Request) {
	authedR, ok := s.deps.Authn.ResolveHTTP(r)
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, _ := interceptor.ClaimsFromContext(authedR.Context())
	identityID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, "invalid identity in token", http.StatusUnauthorized)
		return
	}

	orgID := orgIDFromRequest(r)
	returnTo := util.SafeReturnTo(r.URL.Query().Get("return_to"))

	f, err := s.deps.Flows.CreateForIdentity(authedR.Context(), domflow.TypeSettings, orgID, identityID, returnTo)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleSubmitSettingsFlow handles POST /self-service/settings.
// Requires authentication — verifies the flow's identity matches the caller,
// then submits the method. On success returns the updated flow (no new session).
func (s *Server) handleSubmitSettingsFlow(w http.ResponseWriter, r *http.Request) {
	authedR, ok := s.deps.Authn.ResolveHTTP(r)
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, _ := interceptor.ClaimsFromContext(authedR.Context())
	callerID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, "invalid identity in token", http.StatusUnauthorized)
		return
	}

	flowID, err := uuid.Parse(r.URL.Query().Get("flow"))
	if err != nil {
		writeError(w, errInvalidFlowID, http.StatusBadRequest)
		return
	}

	// Verify the flow belongs to the authenticated caller before submitting.
	f, err := s.deps.Flows.Get(authedR.Context(), flowID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if f.IdentityID == nil || *f.IdentityID != callerID {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid json body", http.StatusBadRequest)
		return
	}
	var req struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		writeError(w, "method required", http.StatusBadRequest)
		return
	}

	result, err := s.deps.Flows.Submit(authedR.Context(), flowID, req.Method, req.Body)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if !result.Done {
		writeJSON(w, http.StatusUnprocessableEntity, result.Flow)
		return
	}
	// Settings flow success — no new session minted; return updated flow state.
	writeJSON(w, http.StatusOK, result.Flow)
}

// --- Identity handlers ---

func (s *Server) handleCreateIdentity(w http.ResponseWriter, r *http.Request) {
	var req command.CreateIdentityCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	i, err := s.deps.Commands.CreateIdentity(r.Context(), req)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, i)
}

func (s *Server) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	i, err := s.deps.Queries.GetIdentity(r.Context(), id, orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (s *Server) handleUpdateIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	var req command.UpdateIdentityCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	req.ID = id
	req.OrgID = orgID
	i, err := s.deps.Commands.UpdateIdentity(r.Context(), req)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (s *Server) handleDeleteIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	if err := s.deps.Commands.DeleteIdentity(r.Context(), id, orgID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	pageToken := r.URL.Query().Get("page_token")
	identities, nextToken, err := s.deps.Queries.ListIdentities(r.Context(), orgID, 50, pageToken)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":           identities,
		"total":           len(identities),
		"next_page_token": nextToken,
	})
}

// --- Session handlers ---

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	r, ok := s.deps.Authn.ResolveHTTP(r)
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, _ := interceptor.ClaimsFromContext(r.Context())
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		writeError(w, "invalid session", http.StatusUnauthorized)
		return
	}
	orgID, err := uuid.Parse(claims.OrgID)
	if err != nil || orgID == uuid.Nil {
		writeError(w, "invalid tenant", http.StatusUnauthorized)
		return
	}
	sess, err := s.deps.Queries.GetSession(r.Context(), sessionID, orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	claims, _ := interceptor.ClaimsFromContext(r.Context())
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		writeError(w, "invalid session", http.StatusUnauthorized)
		return
	}
	orgID := orgIDFromRequest(r)
	_ = s.deps.Commands.RevokeSession(r.Context(), sessionID, orgID)
	for _, name := range []string{"__Host-authincore_session", "authincore_session", "ory_session"} {
		secure := requestIsHTTPS(r) || strings.HasPrefix(name, "__Host-")
		// #nosec G124 -- all cookies are HttpOnly/SameSite; __Host- deletion is always Secure and local legacy cleanup may use HTTP.
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	callerOrg := orgIDFromRequest(r)
	sess, err := s.deps.Queries.GetSession(r.Context(), id, callerOrg)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if !s.requireSelfOrPermission(w, r, sess.IdentityID, "session", "read") {
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	sess, err := s.deps.Queries.GetSession(r.Context(), id, orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if !s.requireSelfOrPermission(w, r, sess.IdentityID, "session", "revoke") {
		return
	}
	if err := s.deps.Commands.RevokeSession(r.Context(), id, orgID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	metrics.ActiveSessions.Dec()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	type check struct {
		name string
		p    Pinger
	}
	checks := []check{{"db", s.deps.DBPinger}, {"cache", s.deps.CachePinger}}
	failed := make(map[string]string)
	for _, c := range checks {
		if c.p == nil {
			continue
		}
		if err := c.p.Ping(ctx); err != nil {
			failed[c.name] = err.Error()
		}
	}
	if len(failed) > 0 {
		// Dependency errors can contain DSNs, hosts, or provider detail. The
		// probe is intentionally useful to orchestration but not diagnostic to
		// an unauthenticated caller.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "dependencies unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	if !s.requireSelfOrPermission(w, r, id, "session", "read") {
		return
	}
	activeOnly := r.URL.Query().Get("active") != "false"
	pageToken := r.URL.Query().Get("page_token")
	orgID := orgIDFromRequest(r)
	sessions, nextToken, err := s.deps.Queries.ListSessions(r.Context(), id, orgID, activeOnly, 50, pageToken)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":        sessions,
		"next_page_token": nextToken,
	})
}

// --- Search ---

func (s *Server) handleSearchIdentities(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	q := r.URL.Query().Get("q")
	identities, total, err := s.deps.Queries.SearchIdentities(r.Context(), query.IdentitySearchQuery{
		OrgID:    orgID,
		Query:    q,
		PageSize: 50,
		Page:     0,
	})
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": identities, "total": total})
}

// --- Admin: orgs ---

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	var req command.CreateOrgCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	tenant, ok := tenancy.FromContext(r.Context())
	if !ok || tenant.InstanceID == uuid.Nil {
		writeError(w, "tenant instance not resolved", http.StatusUnauthorized)
		return
	}
	// The authenticated tenant, not caller-controlled JSON, selects the instance.
	req.InstanceID = tenant.InstanceID
	org, err := s.deps.Commands.CreateOrg(r.Context(), req)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenancy.FromContext(r.Context())
	if !ok || tenant.InstanceID == uuid.Nil {
		writeError(w, "tenant instance not resolved", http.StatusUnauthorized)
		return
	}
	orgs, err := s.deps.Queries.ListOrgs(r.Context(), tenant.InstanceID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": orgs})
}

// --- Service accounts and invitations ---

func (s *Server) handleCreateServiceAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, "name required", http.StatusBadRequest)
		return
	}
	result, err := s.deps.ServiceAccSvc.Create(r.Context(), orgIDFromRequest(r), strings.TrimSpace(body.Name), body.Scopes)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// handleServiceAccountToken is intentionally unauthenticated: it is the
// client_credentials authentication endpoint. The Host-resolved tenant is the
// authority for org selection, and rate limiting is applied at registration.
func (s *Server) handleServiceAccountToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, "invalid request", http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	if orgID == uuid.Nil {
		writeError(w, "tenant not resolved", http.StatusUnauthorized)
		return
	}
	tok, err := s.deps.ServiceAccSvc.IssueToken(r.Context(), orgID, r.FormValue("client_id"), r.FormValue("client_secret"))
	if err != nil {
		writeError(w, "invalid client", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
	})
}

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		writeError(w, "email required", http.StatusBadRequest)
		return
	}
	claims, ok := interceptor.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	inviterID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, "invalid authenticated subject", http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(body.Role) == "" {
		body.Role = "member"
	}
	inv, err := s.deps.InvitationSvc.Create(r.Context(), orgIDFromRequest(r), inviterID, strings.TrimSpace(body.Email), body.Role)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	requestedOrg, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		writeError(w, "invalid org_id", http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	if requestedOrg != orgID {
		writeError(w, "not found", http.StatusNotFound)
		return
	}
	invitations, err := s.deps.InvitationSvc.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invitations, "total": len(invitations)})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	if orgID != orgIDFromRequest(r) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}
	var req command.CreateProjectCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	req.OrgID = orgID
	proj, err := s.deps.Commands.CreateProject(r.Context(), req)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	if orgID != orgIDFromRequest(r) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}
	projects, err := s.deps.Queries.ListProjects(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(r.PathValue("project_id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	var req command.CreateAppCommand
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	req.ProjectID = projectID
	req.OrgID = orgIDFromRequest(r)
	app, err := s.deps.Commands.CreateApp(r.Context(), req)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(r.PathValue("project_id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	apps, err := s.deps.Queries.ListApps(r.Context(), projectID, orgIDFromRequest(r))
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// mintSessionCookie creates a session for identityID, mints an access token,
// sets the session cookie, and returns the token. Extracted to reduce
// cognitive complexity in flows that complete authentication.
func (s *Server) mintSessionCookie(w http.ResponseWriter, r *http.Request, identityID uuid.UUID, method string) (string, error) {
	orgID := orgIDFromRequest(r)
	sess, err := s.deps.Commands.CreateSession(r.Context(), command.CreateSessionCommand{
		IdentityID: identityID,
		OrgID:      orgID,
		Methods:    []domsession.AuthMethod{{Method: method}},
	})
	if err != nil {
		return "", err
	}
	// Resolve authority inside the org the session belongs to. Failing the login
	// on a resolver error is deliberate: the alternative is handing out a token
	// that silently authorises nothing, which surfaces much later as an
	// unexplainable 403 rather than as a login that did not work.
	roles, scopes, err := s.deps.ClaimsResolver.Resolve(r.Context(), orgID, identityID)
	if err != nil {
		return "", err
	}
	accessToken, err := s.deps.TokenSvc.MintAccessToken(r.Context(), identityID, orgID, sess.ID, roles, scopes, "", 1)
	if err != nil {
		return "", err
	}
	secure := requestIsHTTPS(r)
	cookieName := "authincore_session"
	if secure {
		cookieName = "__Host-authincore_session"
	}
	// #nosec G124 -- production HTTPS uses Secure and __Host-; the non-Secure cookie is restricted to explicit local HTTP development.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	return accessToken, nil
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// providerFromFlowContext extracts the selected OIDC provider ID from flow internal context.
func providerFromFlowContext(ctx json.RawMessage) string {
	var ic map[string]string
	if err := json.Unmarshal(ctx, &ic); err == nil {
		return ic["provider"]
	}
	return ""
}

// --- OIDC social login callback ---
// The upstream IdP redirects here with ?code=...&state=<flowID>.
// We resume the flow by submitting the code to the oidc strategy.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		writeError(w, "upstream IdP error: "+errParam, http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state") // state == flow ID
	flowID, err := uuid.Parse(state)
	if err != nil {
		writeError(w, "invalid state", http.StatusBadRequest)
		return
	}

	f, err := s.deps.Flows.Get(r.Context(), flowID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}

	provider := providerFromFlowContext(f.InternalContext)
	if provider == "" {
		provider = "oidc"
	}
	bodyMap := map[string]string{"provider": provider, "code": code, "state": state}
	submitBody, _ := json.Marshal(map[string]any{"method": "oidc", "body": bodyMap})

	result, err := s.deps.Flows.Submit(r.Context(), flowID, "oidc", json.RawMessage(submitBody))
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if !result.Done || result.IdentityID == nil {
		// Browser-facing redirect: target the hosted-UI SPA route, not the JSON
		// flow API of the same name.
		// #nosec G710 -- flowID is a parsed UUID and the destination is a fixed same-origin path.
		http.Redirect(w, r, "/login?flow="+flowID.String(), http.StatusFound)
		return
	}

	if _, err := s.mintSessionCookie(w, r, *result.IdentityID, "oidc"); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	returnTo := util.SafeReturnTo(result.Flow.ReturnTo)
	if returnTo == "" {
		returnTo = "/"
	}
	// #nosec G710 -- SafeReturnTo accepts only same-origin absolute paths and rejects protocol-relative forms.
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (s *Server) handleRevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	if !s.requireSelfOrPermission(w, r, id, "session", "revoke") {
		return
	}
	orgID := orgIDFromRequest(r)
	n, err := s.deps.Commands.RevokeAllSessions(r.Context(), id, orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// consentScreenOrData serves GET /oauth2/consent. AuthorizeHandler redirects
// the browser here as a real top-level navigation (Accept: text/html, no
// Accept: application/json) when consent is required; that request gets the
// hosted-ui SPA shell so its client-side ConsentPage route can render an
// actual Allow/Deny screen. The SPA's own fetch back to this same path (with
// Accept: application/json) gets ConsentHandler.ShowConsent's JSON payload,
// same as before. Without this split, a browser landed on a bare JSON blob
// with no way to grant consent — the OAuth flow could not complete for any
// public client.
func (s *Server) consentScreenOrData(w http.ResponseWriter, r *http.Request) {
	if s.deps.HostedUI != nil && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		s.hostedUIHandler(s.deps.HostedUI).ServeHTTP(w, r)
		return
	}
	s.deps.ConsentHandler.ShowConsent(w, r)
}

// hostedUIHandler serves the embedded hosted login UI as a single-page app:
// real files (index.html, hashed assets) are served directly, and any unknown
// path falls back to index.html so client-side routes like /login resolve.
func (s *Server) hostedUIHandler(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The global SecurityHeaders CSP is "default-src 'none'", which is
		// correct for the JSON API but blocks this SPA's own bundle. Narrow it
		// to what the login UI actually needs; still no third-party origins.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"form-action 'self'; "+
				"base-uri 'none'; "+
				"frame-ancestors 'none'")

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			fileServer.ServeHTTP(w, r) // "/" → index.html
			return
		}
		if f, err := distFS.Open(name); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path: serve the SPA shell so the client router can handle it.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// rateLimited wraps a handler with per-IP rate limiting when a RateLimiter is configured.
// maxReqs requests are allowed per window per IP+action bucket.
func (s *Server) rateLimited(action string, maxReqs int64, window time.Duration, h http.HandlerFunc) http.Handler {
	if s.deps.RateLimiter == nil {
		return h
	}
	return middleware.RateLimit(s.deps.RateLimiter, middleware.RateLimitConfig{
		MaxRequests: maxReqs,
		Window:      window,
		KeyFunc:     middleware.OrgIPKeyFunc(action, s.deps.TrustedProxies...),
		FailClosed:  true,
	})(h)
}

// --- helpers ---

// unknownFlowMethod is the single label every unregistered method collapses
// into, so the series count stays bounded by the registered strategies.
const unknownFlowMethod = "unknown"

// safeFlowMethod bounds a submitted flow method before it becomes a metric
// label.
//
// The method is read straight out of the request body, and Submit rejects an
// unregistered one — which put the caller's own string on the failure counter.
// A Prometheus label set fed from untrusted input grows without limit: every
// distinct value is a new time series held in memory for the process's life,
// so a caller who can reach this endpoint can drive the exporter into
// exhaustion, one request at a time. Rate limiting bounds the rate, not the
// total. Only a registered strategy id survives; anything else is "unknown".
func (s *Server) safeFlowMethod(method string) string {
	if s.deps.Flows.KnownMethod(method) {
		return method
	}
	return unknownFlowMethod
}

// errorResponse is the canonical error body for every non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response — use instead of http.Error so every
// error in the API has the same shape: {"error":"...","code":"..."}.
func writeError(w http.ResponseWriter, msg string, status int) {
	writeJSON(w, status, errorResponse{Error: msg, Code: statusToCode(status)})
}

func httpError(w http.ResponseWriter, err error, log *slog.Logger) {
	var de *domainerr.Error
	if errors.As(err, &de) {
		status := codeToHTTP(de.Code)
		writeJSON(w, status, errorResponse{Error: de.Message, Code: de.Code.String()})
		return
	}
	log.Error("internal error", "err", err)
	writeJSON(w, http.StatusInternalServerError, errorResponse{
		Error: "internal server error",
		Code:  "internal",
	})
}

func codeToHTTP(code domainerr.Code) int {
	switch code {
	case domainerr.CodeNotFound:
		return http.StatusNotFound
	case domainerr.CodeAlreadyExists:
		return http.StatusConflict
	case domainerr.CodeInvalidArgument:
		return http.StatusBadRequest
	case domainerr.CodeUnauthenticated:
		return http.StatusUnauthorized
	case domainerr.CodePermissionDenied:
		return http.StatusForbidden
	case domainerr.CodePreconditionFailed:
		return http.StatusPreconditionFailed
	case domainerr.CodeFeatureGated:
		return http.StatusPaymentRequired
	default:
		return http.StatusInternalServerError
	}
}

func statusToCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusTooManyRequests:
		return "too_many_requests"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		return "internal"
	}
}

func orgIDFromRequest(r *http.Request) uuid.UUID {
	if t, ok := tenancy.FromContext(r.Context()); ok {
		return t.OrgID
	}
	return uuid.Nil
}

// orgFromAuthedRequest resolves the caller's org from a trusted source: the
// Host-based tenant context first, then the authenticated token's org_id claim
// (the path service accounts take on a shared host). It never reads the request
// body, so a caller cannot operate across tenants by spoofing an org_id. Returns
// false when no org can be resolved. Intended for handlers behind requireAuth.
func (s *Server) orgFromAuthedRequest(r *http.Request) (uuid.UUID, bool) {
	if t, ok := tenancy.FromContext(r.Context()); ok && t.OrgID != uuid.Nil {
		return t.OrgID, true
	}
	if claims, ok := interceptor.ClaimsFromContext(r.Context()); ok {
		if orgID, err := uuid.Parse(claims.OrgID); err == nil && orgID != uuid.Nil {
			return orgID, true
		}
	}
	return uuid.Nil, false
}

// --- Blueprint handlers ---

func (s *Server) handleApplyBlueprint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML   string `json:"yaml"`
		DryRun bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.YAML == "" {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	bp, diff, err := s.deps.BlueprintSvc.Apply(r.Context(), orgID, body.YAML, body.DryRun)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blueprint": bp, "diff": diff})
}

func (s *Server) handleListBlueprints(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	bps, err := s.deps.BlueprintSvc.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, bps)
}

// --- Platform contract handler implementations ---

// handlePlatformManifest responds to GET /platform/v1/manifest with the module's
// sidebar/nav/band metadata (shared contract surface for the suite shell).
func (s *Server) handlePlatformManifest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.PlatformAdapter.Manifest())
}

// handlePlatformHealth responds to GET /platform/v1/health with the module's
// GREEN/YELLOW/RED rollup and per-subsystem checks for the Health Map.
func (s *Server) handlePlatformHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.PlatformAdapter.Health(r.Context()))
}

// handlePlatformActions responds to GET /platform/v1/actions with the Copilot
// action descriptors this module exposes.
func (s *Server) handlePlatformActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.PlatformAdapter.Actions())
}

// handlePlatformInvoke responds to POST /platform/v1/actions/invoke by
// dispatching a Copilot action by ID.
func (s *Server) handlePlatformInvoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		writeError(w, "action is required", http.StatusBadRequest)
		return
	}
	var requiredScope string
	for _, action := range s.deps.PlatformAdapter.Actions() {
		if action.ID == body.Action {
			requiredScope = action.RequiredScope
			break
		}
	}
	if requiredScope == "" {
		writeError(w, "unknown action", http.StatusBadRequest)
		return
	}
	parts := strings.Split(requiredScope, ":")
	if len(parts) != 3 {
		writeError(w, "invalid action authorization", http.StatusInternalServerError)
		return
	}
	claims, _ := interceptor.ClaimsFromContext(r.Context())
	allowed, authErr := s.permissionAllowed(r.Context(), claims, parts[1], parts[2])
	if authErr != nil {
		writeError(w, "authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	if body.Params == nil {
		body.Params = make(map[string]any)
	}
	orgID, ok := s.orgFromAuthedRequest(r)
	if !ok {
		writeError(w, "tenant required", http.StatusForbidden)
		return
	}
	body.Params["_org_id"] = orgID.String()
	body.Params["_actor_id"] = claims.Subject
	if tenant, ok := tenancy.FromContext(r.Context()); ok {
		body.Params["_instance_id"] = tenant.InstanceID.String()
	}
	out, err := s.deps.PlatformAdapter.Invoke(r.Context(), body.Action, body.Params)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

// requireAuth wraps a handler so unauthenticated requests receive 401.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authedR, ok := s.deps.Authn.ResolveHTTP(r)
		if !ok {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims, ok := interceptor.ClaimsFromContext(authedR.Context())
		if !ok {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if tenant, hasTenant := tenancy.FromContext(authedR.Context()); hasTenant {
			claimOrgID, err := uuid.Parse(claims.OrgID)
			if err != nil || claimOrgID == uuid.Nil || claimOrgID != tenant.OrgID {
				writeError(w, "tenant mismatch", http.StatusForbidden)
				return
			}
		}
		h(w, authedR)
	}
}

// requirePermission authenticates the caller and then enforces a tenant-local
// RBAC permission. Token scopes are accepted for service accounts; interactive
// sessions are checked against the authoritative role/permission tables.
func (s *Server) requirePermission(resource, scope string, h http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := interceptor.ClaimsFromContext(r.Context())
		allowed, err := s.permissionAllowed(r.Context(), claims, resource, scope)
		if err != nil {
			s.deps.Log.Error("authorization check failed", "resource", resource, "scope", scope, "err", err)
			writeError(w, "authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

func (s *Server) permissionAllowed(ctx context.Context, claims *token.Claims, resource, scope string) (bool, error) {
	required := resource + ":" + scope
	qualified := "authincore:" + required
	for _, granted := range claims.Scopes {
		if granted == "*" || granted == required || granted == qualified {
			return true, nil
		}
	}
	if s.deps.AuthzEnforcer == nil {
		return false, fmt.Errorf("authorization enforcer is not configured")
	}
	identityID, identityErr := uuid.Parse(claims.Subject)
	orgID, orgErr := uuid.Parse(claims.OrgID)
	if identityErr != nil || orgErr != nil || identityID == uuid.Nil || orgID == uuid.Nil {
		return false, nil
	}
	decision, err := s.deps.AuthzEnforcer.Check(ctx, authz.CheckRequest{
		OrgID: orgID, IdentityID: identityID, Resource: resource, Scope: scope,
	})
	return decision.Allowed, err
}

func (s *Server) requireSelfOrPermission(w http.ResponseWriter, r *http.Request, targetIdentity uuid.UUID, resource, scope string) bool {
	claims, ok := interceptor.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if claims.Subject == targetIdentity.String() {
		return true
	}
	allowed, err := s.permissionAllowed(r.Context(), claims, resource, scope)
	if err != nil {
		s.deps.Log.Error("authorization check failed", "resource", resource, "scope", scope, "err", err)
		writeError(w, "authorization unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !allowed {
		writeError(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// requireAdminAuth accepts either a valid session or a static Bearer admin token.
// Used for endpoints (like /metrics) that need to work before any identity exists.
func (s *Server) requireAdminAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check static admin token first (for bootstrap / headless use).
		if tok := s.deps.AdminToken; tok != "" {
			const prefix = "Bearer "
			if auth := r.Header.Get("Authorization"); len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
				if auth[len(prefix):] == tok {
					h(w, r)
					return
				}
			}
		}
		// Fall back to normal session auth.
		authedR, ok := s.deps.Authn.ResolveHTTP(r)
		if !ok {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, authedR)
	}
}
