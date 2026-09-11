package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	"github.com/santoshguru/crm/internal/integration/leadcapture"
	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
	"github.com/santoshguru/crm/internal/integration/privacy"
	"github.com/santoshguru/crm/internal/metrics"
	"github.com/santoshguru/crm/internal/platform"
	"github.com/santoshguru/crm/internal/secrets"
	"github.com/santoshguru/crm/internal/tracing"
)

const moduleID = "crm"

const maxRequestBodyBytes = 4 << 20

// Server holds the wired CRM HTTP server.
type Server struct {
	cfg      config.Config
	store    domain.Store
	emitter  *platform.Emitter
	authn    *auth.Authenticator
	google   *google.Service
	privacy  *privacy.Service
	gmail    *gmail.Syncer
	calendar *calendar.Syncer
	// leadCapture is nil when LEAD_CAPTURE_SECRET_KEY is unset. The route is
	// then not registered at all, rather than registered and failing: an
	// endpoint that cannot verify a signature must not exist.
	leadCapture *leadcapture.Service
	meta        *metaintegration.Service
	// captureSealer is the same key the ingestion service uses, handed to the
	// API layer so issuing an endpoint and verifying its deliveries cannot
	// drift onto two different keys.
	captureSealer *secrets.Sealer
	// gmailSender is nil unless Google is configured, exactly like gmail
	// above: without a credential there is no mailbox to send from, and a
	// sender wired to a nil token source would fail on every attempt rather
	// than reporting that the feature is off.
	gmailSender *gmail.Sender
	mux         *http.ServeMux
	readiness   func(context.Context) error
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
	// Loop prevention in workflow automation is structural: no event a rule
	// may trigger on is one a workflow action produces. There is no depth
	// counter behind that, so the structure is checked here rather than
	// trusted — a rule that could fire on its own output is an unbounded loop
	// against a customer's records, and a deployment should stop rather than
	// discover it from a support ticket.
	if err := domain.AssertTriggersCannotLoop(); err != nil {
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
	var gmailSender *gmail.Sender
	if googleSvc.Configured() {
		gmailClient := gmail.New(nil)
		gmailSyncer = gmail.NewSyncer(store, gmailClient, googleSvc, privacySvc, slog.Default())
		calendarSyncer = calendar.NewSyncer(store, calendar.New(nil), googleSvc, privacySvc, slog.Default())
		// One client for reading and sending: they share a base URL and an
		// HTTP timeout, and two would be two places to configure.
		gmailSender = gmail.NewSender(store, gmailClient, googleSvc)
	}
	captureSealer, err := newCaptureSealer(cfg)
	if err != nil {
		return nil, err
	}
	var captureSvc *leadcapture.Service
	if captureSealer != nil {
		captureSvc, err = leadcapture.New(store, captureSealer, slog.Default())
		if err != nil {
			return nil, err
		}
	}
	metaSvc, err := newMetaService(cfg, store)
	if err != nil {
		return nil, err
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

		leadCapture:   captureSvc,
		meta:          metaSvc,
		captureSealer: captureSealer,
		gmailSender:   gmailSender,

		mux: http.NewServeMux(),
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
	s.authn.Close()
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
	// CORS is also the Origin gate for cookie-authenticated requests, so it
	// must remain installed when the allowlist is empty. Omitting it in that
	// state would let an unapproved same-site origin execute simple POSTs even
	// though the browser could not read their responses.
	mws := []func(http.Handler) http.Handler{requestIDMW, requestBodyLimitMW, tracing.HTTPMiddleware, metrics.Middleware, loggingMW, recoverMW, corsMW(s.cfg.CORSOrigins, s.cfg.AuthCORSOrigins)}
	return chain(s.mux, mws...)
}

func (s *Server) routes() {
	// Passwordless and Google login are first-party CRM routes. Successful
	// authentication establishes the opaque session consumed by the Connect
	// interceptors registered below.
	s.authn.RegisterRoutes(s.mux)

	// /healthz is liveness: the process is up and serving. Dependency state is
	// reported by /readyz instead, so a transient database outage does not
	// turn into a restart loop.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// /readyz is readiness: this instance can actually do its job. The auth
	// check is kept separate from the domain-store check because deployments
	// may use distinct pools or credentials even though both point at CRM
	// Postgres.
	//
	// Splitting the two is the point. A single endpoint has to choose
	// between restarting on an upstream outage and routing traffic to an
	// instance that cannot serve it; separating them means the orchestrator
	// keeps the process alive and takes it out of the load balancer instead.
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		err := s.readiness(ctx)
		if err != nil {
			slog.Warn("readiness check failed", "dependency", "database", "request_id", RequestID(r.Context()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "reason": "dependencies unavailable"})
			return
		}
		if err := s.authn.Ready(ctx); err != nil {
			slog.Warn("readiness check failed", "dependency", "authentication_store", "request_id", RequestID(r.Context()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "not_ready",
				"reason": "authentication store unavailable",
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
	readLimit := connect.WithReadMaxBytes(maxRequestBodyBytes)

	modPath, modHandler := platformv1connect.NewModuleServiceHandler(
		api.NewModuleService(s.cfg, s.store), interceptors, readLimit)
	s.mux.Handle(modPath, modHandler)

	crmPath, crmHandler := crmv1connect.NewCRMServiceHandler(
		api.NewCRMService(s.cfg, s.store, s.google, s.gmail, s.calendar, s.privacy,
			s.captureSealer, s.gmailSender, s.meta), interceptors, readLimit)
	s.mux.Handle(crmPath, crmHandler)

	// The Google OAuth callback is a plain browser redirect, not a Connect
	// RPC: Google sends the user's browser here with no Authorization header,
	// so it cannot go through the authenticating interceptor. What stands in
	// for authentication is the one-time state record written before the
	// redirect — see internal/integration/google.
	s.mux.Handle("GET "+googleCallbackPath, s.googleCallbackHandler())
	if s.meta != nil && s.meta.Configured() {
		s.mux.Handle("GET "+metaCallbackPath, s.metaCallbackHandler())
		s.mux.Handle("GET "+metaWebhookPath, s.metaWebhookHandler())
		s.mux.Handle("POST "+metaWebhookPath, s.metaWebhookHandler())
	}

	// Inbound lead capture, likewise outside the interceptor: the caller is a
	// third-party server with no CRM session. It is registered only when a
	// sealing key is configured, because without one no signature can be
	// checked and an endpoint that accepts unsigned posts is an open
	// lead-injection hole. See internal/integration/leadcapture and
	// docs/LEAD_CAPTURE.md.
	if s.leadCapture != nil {
		s.mux.Handle(leadCapturePrefix, s.leadCaptureHandler())
	}
}

// newCaptureSealer builds the lead-capture sealer, or returns nil when the
// deployment supplied no key.
//
// Nil is "off", and off is a real state: a deployment that has not decided to
// accept inbound leads should not have a URL that does. A malformed key is an
// error rather than off, for the same reason a partial Google configuration
// is — silently disabling a feature somebody configured is worse than
// refusing to start.
func newCaptureSealer(cfg config.Config) (*secrets.Sealer, error) {
	if strings.TrimSpace(cfg.LeadCaptureSecretKey) == "" {
		return nil, nil
	}
	sealer, err := secrets.New(cfg.LeadCaptureSecretKey)
	if err != nil {
		return nil, fmt.Errorf("lead capture: %w", err)
	}
	return sealer, nil
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

func newMetaService(cfg config.Config, store domain.Store) (*metaintegration.Service, error) {
	if cfg.MetaAppID == "" && cfg.MetaAppSecret == "" && cfg.MetaOAuthRedirectURL == "" &&
		cfg.MetaWebhookVerifyToken == "" && cfg.MetaTokenKey == "" {
		return metaintegration.NewService(store, nil, nil, cfg.WebAppBaseURL), nil
	}
	sealer, err := secrets.New(cfg.MetaTokenKey)
	if err != nil {
		return nil, fmt.Errorf("meta token sealing: %w", err)
	}
	client, err := metaintegration.NewClient(metaintegration.Config{
		AppID: cfg.MetaAppID, AppSecret: cfg.MetaAppSecret,
		RedirectURL: cfg.MetaOAuthRedirectURL, GraphVersion: cfg.MetaGraphVersion,
	}, nil)
	if err != nil {
		return nil, err
	}
	return metaintegration.NewService(store, client, sealer, cfg.WebAppBaseURL), nil
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
