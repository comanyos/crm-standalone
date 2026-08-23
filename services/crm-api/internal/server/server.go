package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/santoshguru/crm/gen/crm/v1/crmv1connect"
	"github.com/santoshguru/crm/gen/platform/v1/platformv1connect"
	"github.com/santoshguru/crm/internal/api"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/calendar"
	"github.com/santoshguru/crm/internal/integration/gmail"
	"github.com/santoshguru/crm/internal/integration/google"
	"github.com/santoshguru/crm/internal/integration/googleoauth"
	"github.com/santoshguru/crm/internal/integration/privacy"
	"github.com/santoshguru/crm/internal/metrics"
	"github.com/santoshguru/crm/internal/platform"
	"github.com/santoshguru/crm/internal/tracing"
)

const moduleID = "crm"

// Server holds the wired CRM HTTP server.
type Server struct {
	cfg       config.Config
	store     domain.Store
	emitter   *platform.Emitter
	authn     *auth.Authenticator
	google    *google.Service
	privacy   *privacy.Service
	gmail     *gmail.Syncer
	calendar  *calendar.Syncer
	mux       *http.ServeMux
	readiness func(context.Context) error
}

// New builds the server and registers routes.
//
// It refuses to build a server whose scope policy does not decide every
// procedure it is about to serve — see assertPolicyIsTotal in scopes.go. A
// procedure with no decision is already denied at request time, so this is
// not what keeps it closed; it is what makes the hole visible at deploy time
// instead of as a support ticket about an RPC that returns permission denied
// for everyone.
func New(
	cfg config.Config, store domain.Store, authn *auth.Authenticator,
) (*Server, error) {
	if err := assertPolicyIsTotal(scopePolicy); err != nil {
		return nil, err
	}
	googleSvc, err := newGoogleService(cfg, store)
	if err != nil {
		return nil, err
	}
	// The syncer is built only when Google is actually configured: without a
	// credential there is no mailbox to read, and a syncer wired to a nil
	// token source would fail on every pass instead of not running.
	// The privacy service exists whether or not Google is configured: an
	// erasure request has to be servable for content a deployment collected
	// before the integration was turned off.
	privacySvc := privacy.New(store, cfg.SyncRetentionDays, slog.Default())

	var (
		gmailSyncer    *gmail.Syncer
		calendarSyncer *calendar.Syncer
	)
	if googleSvc.Configured() {
		gmailSyncer = gmail.NewSyncer(store, gmail.New(nil), googleSvc, privacySvc, slog.Default())
		calendarSyncer = calendar.NewSyncer(store, calendar.New(nil), googleSvc, privacySvc, slog.Default())
	}
	emitter := platform.NewEmitter(moduleID, cfg.NATSURL)
	s := &Server{
		cfg:      cfg,
		store:    store,
		emitter:  emitter,
		authn:    authn,
		google:   googleSvc,
		privacy:  privacySvc,
		gmail:    gmailSyncer,
		calendar: calendarSyncer,
		mux:      http.NewServeMux(),
	}
	if pinger, ok := store.(interface{ Ping(context.Context) error }); ok {
		s.readiness = pinger.Ping
	} else {
		s.readiness = func(context.Context) error { return nil }
	}
	s.routes()
	return s, nil
}

// GmailSyncer and CalendarSyncer expose the sync engines to the background
// pollers in cmd/crm. Both are nil when the deployment has no Google
// credentials.
func (s *Server) GmailSyncer() *gmail.Syncer { return s.gmail }

// CalendarSyncer is GmailSyncer's twin.
func (s *Server) CalendarSyncer() *calendar.Syncer { return s.calendar }

// Close gracefully drains the server's NATS connection.
func (s *Server) Close() {
	s.emitter.Close()
}

// Publisher returns a bus publisher sharing the server's NATS connection.
// It is separate from the Emitter's fire-and-forget Emit: a transactional
// outbox drain (internal/dispatch) needs a publish that can fail out loud,
// which is what platform.Publisher provides.
func (s *Server) Publisher() *platform.Publisher {
	return platform.PublisherFor(s.emitter)
}

// Handler exposes the mux wrapped in middleware.
func (s *Server) Handler() http.Handler {
	mws := []func(http.Handler) http.Handler{requestIDMW, tracing.HTTPMiddleware, metrics.Middleware, loggingMW, recoverMW}
	if len(s.cfg.CORSOrigins) > 0 {
		mws = append(mws, corsMW(s.cfg.CORSOrigins))
	}
	return chain(s.mux, mws...)
}

func (s *Server) routes() {
	// /healthz is liveness: the process is up and serving. It deliberately
	// does not consult the identity provider — a CRM that restarts itself
	// because AuthinCore is briefly unreachable turns a recoverable outage
	// into a crash loop.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// /readyz is readiness: this instance can actually do its job. It can
	// only do its job if it holds signing keys, because without them every
	// authenticated request fails — and fails in a way that looks to the user
	// like their own credentials are wrong, not like an outage.
	//
	// Splitting the two is the point. A single endpoint has to choose
	// between restarting on an upstream outage and routing traffic to an
	// instance that cannot serve it; separating them means the orchestrator
	// keeps the process alive and takes it out of the load balancer instead.
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := s.readiness(ctx)
		cancel()
		if err != nil {
			slog.Warn("readiness check failed", "dependency", "database", "request_id", RequestID(r.Context()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "reason": "dependencies unavailable"})
			return
		}
		if err := s.authn.Ready(r.Context()); err != nil {
			slog.Warn("readiness check failed", "dependency", "signing_keys", "request_id", RequestID(r.Context()))
			w.WriteHeader(http.StatusServiceUnavailable)
			// The reason names the subsystem, never the JWKS URL's contents
			// or any token material.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "not_ready",
				"reason": "signing keys unavailable",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	})

	// Metrics contain aggregate counters and bounded operation labels only.
	// Compose exposes this for local/internal development; production network
	// policy must restrict access to the scrape endpoint.
	metricsHandler := metrics.Handler(func() {
		metrics.SetNATSStatus(s.emitter.NATSStatus())
		if store, ok := s.store.(interface {
			PoolStats() (int32, int32, int32, int32)
		}); ok {
			acquired, idle, total, max := store.PoolStats()
			metrics.SetDBPoolStats(metrics.DBPoolStats{Acquired: acquired, Idle: idle, Total: total, Max: max})
		}
	})
	s.mux.Handle("GET /metrics", s.requireMetricsToken(metricsHandler))

	interceptors := connect.WithInterceptors(s.authn.Interceptor(scopePolicy))

	modPath, modHandler := platformv1connect.NewModuleServiceHandler(
		api.NewModuleService(s.cfg, s.store), interceptors)
	s.mux.Handle(modPath, modHandler)

	crmPath, crmHandler := crmv1connect.NewCRMServiceHandler(
		api.NewCRMService(s.cfg, s.store, s.google, s.gmail, s.calendar, s.privacy), interceptors)
	s.mux.Handle(crmPath, crmHandler)

	// The Google OAuth callback is a plain browser redirect, not a Connect
	// RPC: Google sends the user's browser here with no Authorization header,
	// so it cannot go through the authenticating interceptor. What stands in
	// for authentication is the one-time state record written before the
	// redirect — see internal/integration/google.
	s.mux.Handle("GET "+googleCallbackPath, s.googleCallbackHandler())
}

// newGoogleService builds the Google integration, or returns nil when the
// deployment supplied no credentials.
//
// A partial configuration is a startup error rather than a disabled feature.
// config.Validate already rejects it, and this is the second gate: a client
// ID without a token key would otherwise mean a CRM that offers a Connect
// button and then cannot seal what it receives.
func newGoogleService(cfg config.Config, store domain.Store) (*google.Service, error) {
	if cfg.GoogleOAuthClientID == "" && cfg.GoogleOAuthClientSecret == "" &&
		cfg.GoogleOAuthRedirectURL == "" && cfg.GoogleOAuthTokenKey == "" {
		return google.New(store, nil, cfg.WebAppBaseURL), nil
	}
	client, err := googleoauth.New(googleoauth.Config{
		ClientID:     cfg.GoogleOAuthClientID,
		ClientSecret: cfg.GoogleOAuthClientSecret,
		RedirectURL:  cfg.GoogleOAuthRedirectURL,
		TokenKey:     cfg.GoogleOAuthTokenKey,
	}, nil)
	if err != nil {
		return nil, err
	}
	return google.New(store, client, cfg.WebAppBaseURL), nil
}

// requireMetricsToken protects scrape data when CRM_METRICS_TOKEN is set. An
// empty token is intentionally the local/internal Compose posture documented
// in docs/OBSERVABILITY.md; production config validation rejects that state.
func (s *Server) requireMetricsToken(next http.Handler) http.Handler {
	if s.cfg.MetricsToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		provided := r.Header.Get("Authorization")
		if len(provided) <= len(prefix) || provided[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(provided[len(prefix):]), []byte(s.cfg.MetricsToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
