package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/santoshguru/crm/gen/crm/v1/crmv1connect"
	"github.com/santoshguru/crm/gen/platform/v1/platformv1connect"
	"github.com/santoshguru/crm/internal/api"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/platform"
)

const moduleID = "crm"

// Server holds the wired CRM HTTP server.
type Server struct {
	cfg     config.Config
	store   domain.Store
	emitter *platform.Emitter
	authn   *auth.Authenticator
	mux     *http.ServeMux
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
	emitter := platform.NewEmitter(moduleID, cfg.NATSURL)
	s := &Server{
		cfg:     cfg,
		store:   store,
		emitter: emitter,
		authn:   authn,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

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
	mws := []func(http.Handler) http.Handler{requestIDMW, loggingMW, recoverMW}
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
		if err := s.authn.Ready(r.Context()); err != nil {
			slog.Warn("readiness check failed", "err", err,
				"request_id", RequestID(r.Context()))
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

	interceptors := connect.WithInterceptors(s.authn.Interceptor(scopePolicy))

	modPath, modHandler := platformv1connect.NewModuleServiceHandler(
		api.NewModuleService(s.cfg, s.store), interceptors)
	s.mux.Handle(modPath, modHandler)

	crmPath, crmHandler := crmv1connect.NewCRMServiceHandler(
		api.NewCRMService(s.cfg, s.store), interceptors)
	s.mux.Handle(crmPath, crmHandler)
}
