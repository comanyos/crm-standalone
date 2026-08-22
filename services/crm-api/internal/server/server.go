package server

import (
	"encoding/json"
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

// scopePolicy maps every connect procedure to the scope the caller must hold.
//
// An RPC missing from this map is enforced as public: the interceptor only
// checks procedures it finds here. Adding a handler means adding its entry.
var scopePolicy = map[string]string{
	"/crm.v1.CRMService/CreateAccount":               "crm:account:write",
	"/crm.v1.CRMService/GetAccount":                  "crm:account:read",
	"/crm.v1.CRMService/ListAccounts":                "crm:account:read",
	"/crm.v1.CRMService/UpdateAccount":               "crm:account:write",
	"/crm.v1.CRMService/DeleteAccount":               "crm:account:write",
	"/crm.v1.CRMService/CreateContact":               "crm:contact:write",
	"/crm.v1.CRMService/GetContact":                  "crm:contact:read",
	"/crm.v1.CRMService/ListContacts":                "crm:contact:read",
	"/crm.v1.CRMService/UpdateContact":               "crm:contact:write",
	"/crm.v1.CRMService/DeleteContact":               "crm:contact:write",
	"/crm.v1.CRMService/CreateLead":                  "crm:lead:write",
	"/crm.v1.CRMService/GetLead":                     "crm:lead:read",
	"/crm.v1.CRMService/ListLeads":                   "crm:lead:read",
	"/crm.v1.CRMService/UpdateLead":                  "crm:lead:write",
	"/crm.v1.CRMService/DeleteLead":                  "crm:lead:write",
	"/crm.v1.CRMService/ConvertLead":                 "crm:lead:write",
	"/crm.v1.CRMService/CreateOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/GetOpportunity":              "crm:opportunity:read",
	"/crm.v1.CRMService/ListOpportunities":           "crm:opportunity:read",
	"/crm.v1.CRMService/UpdateOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/DeleteOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/ListOpportunityStageHistory": "crm:opportunity:read",
	"/crm.v1.CRMService/CreatePipeline":              "crm:pipeline:write",
	"/crm.v1.CRMService/GetPipeline":                 "crm:pipeline:read",
	"/crm.v1.CRMService/ListPipelines":               "crm:pipeline:read",
	"/crm.v1.CRMService/UpdatePipeline":              "crm:pipeline:write",
	"/crm.v1.CRMService/CreatePipelineStage":         "crm:pipeline:write",
	"/crm.v1.CRMService/ListPipelineStages":          "crm:pipeline:read",
	"/crm.v1.CRMService/UpdatePipelineStage":         "crm:pipeline:write",
	"/crm.v1.CRMService/CreateActivity":              "crm:activity:write",
	"/crm.v1.CRMService/GetActivity":                 "crm:activity:read",
	"/crm.v1.CRMService/ListActivities":              "crm:activity:read",
	"/crm.v1.CRMService/UpdateActivity":              "crm:activity:write",
	"/crm.v1.CRMService/DeleteActivity":              "crm:activity:write",
	"/crm.v1.CRMService/ListActivitiesForSubject":    "crm:activity:read",
	"/crm.v1.CRMService/GetSummary":                  "crm:summary:read",
}

// Server holds the wired CRM HTTP server.
type Server struct {
	cfg     config.Config
	store   domain.Store
	emitter *platform.Emitter
	authn   *auth.Authenticator
	mux     *http.ServeMux
}

// New builds the server and registers routes.
func New(
	cfg config.Config, store domain.Store, authn *auth.Authenticator,
) *Server {
	emitter := platform.NewEmitter(moduleID, cfg.NATSURL)
	s := &Server{
		cfg:     cfg,
		store:   store,
		emitter: emitter,
		authn:   authn,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
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
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	interceptors := connect.WithInterceptors(s.authn.Interceptor(scopePolicy))

	modPath, modHandler := platformv1connect.NewModuleServiceHandler(
		api.NewModuleService(s.cfg, s.store), interceptors)
	s.mux.Handle(modPath, modHandler)

	crmPath, crmHandler := crmv1connect.NewCRMServiceHandler(
		api.NewCRMService(s.cfg, s.store), interceptors)
	s.mux.Handle(crmPath, crmHandler)
}
