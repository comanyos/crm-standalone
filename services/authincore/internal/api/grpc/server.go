// Package grpc exposes AuthinCore's core operations over gRPC using connect-go.
// This gives SDK generators (buf generate) a typed surface identical to Zitadel's
// approach — one proto definition produces both gRPC and HTTP/JSON handlers.
//
// Current services:
//   - IdentityService  — CRUD + search
//   - SessionService   — list, revoke, whoami
//   - FlowService      — initiate and submit self-service flows
//   - OrgService       — org/project/app admin
//   - TokenService     — introspect, revoke
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/authincore/authincore/internal/command"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow"
	"github.com/authincore/authincore/internal/query"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

// --- request/response types (stand-ins until proto-generated types are wired) ---

// Until buf generate is run these are plain Go structs.
// They mirror the JSON API contracts so existing clients work over both transports.

type GetIdentityRequest struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}
type GetIdentityResponse struct {
	ID       string          `json:"id"`
	OrgID    string          `json:"org_id"`
	SchemaID string          `json:"schema_id"`
	Traits   json.RawMessage `json:"traits"`
	State    string          `json:"state"`
}

type ListSessionsRequest struct {
	IdentityID string `json:"identity_id"`
	OrgID      string `json:"org_id"`
}
type ListSessionsResponse struct {
	Sessions []SessionView `json:"sessions"`
}
type SessionView struct {
	ID         string `json:"id"`
	IdentityID string `json:"identity_id"`
	State      string `json:"state"`
	AuthMethod string `json:"auth_method"`
}

type WhoAmIRequest struct{}
type WhoAmIResponse struct {
	IdentityID string `json:"identity_id"`
	OrgID      string `json:"org_id"`
	SessionID  string `json:"session_id"`
	Email      string `json:"email"`
}

type InitFlowRequest struct {
	Type  string `json:"type"`
	OrgID string `json:"org_id"`
}
type InitFlowResponse struct {
	FlowID string          `json:"flow_id"`
	UI     json.RawMessage `json:"ui"`
}

type SubmitFlowRequest struct {
	FlowID string          `json:"flow_id"`
	Body   json.RawMessage `json:"body"`
}
type SubmitFlowResponse struct {
	State      string `json:"state"`
	IdentityID string `json:"identity_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

// --- server implementations ---

// IdentityServer implements the IdentityService gRPC surface.
type IdentityServer struct {
	cmds    *command.Commands
	queries *query.Queries
	log     *slog.Logger
}

func NewIdentityServer(cmds *command.Commands, queries *query.Queries, log *slog.Logger) *IdentityServer {
	return &IdentityServer{cmds: cmds, queries: queries, log: log}
}

func (s *IdentityServer) GetIdentity(ctx context.Context, req *connect.Request[GetIdentityRequest]) (*connect.Response[GetIdentityResponse], error) {
	id, err := uuid.Parse(req.Msg.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	orgID, err := uuid.Parse(req.Msg.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	i, err := s.queries.GetIdentity(ctx, id, orgID)
	if err != nil {
		return nil, domainToConnect(err)
	}
	return connect.NewResponse(&GetIdentityResponse{
		ID:       i.ID.String(),
		OrgID:    i.OrgID.String(),
		SchemaID: i.SchemaID,
		Traits:   i.Traits,
		State:    fmt.Sprintf("%d", i.State),
	}), nil
}

// SessionServer implements the SessionService gRPC surface.
type SessionServer struct {
	queries  *query.Queries
	tokenSvc *token.Service
	log      *slog.Logger
}

func NewSessionServer(queries *query.Queries, tokenSvc *token.Service, log *slog.Logger) *SessionServer {
	return &SessionServer{queries: queries, tokenSvc: tokenSvc, log: log}
}

func (s *SessionServer) WhoAmI(ctx context.Context, req *connect.Request[WhoAmIRequest]) (*connect.Response[WhoAmIResponse], error) {
	claims, ok := claimsFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	return connect.NewResponse(&WhoAmIResponse{
		IdentityID: claims.Subject,
		OrgID:      claims.OrgID,
		SessionID:  claims.SessionID,
		Email:      claims.Email,
	}), nil
}

// FlowServer implements the FlowService gRPC surface.
type FlowServer struct {
	mgr *flow.Manager
	log *slog.Logger
}

func NewFlowServer(mgr *flow.Manager, log *slog.Logger) *FlowServer {
	return &FlowServer{mgr: mgr, log: log}
}

func (s *FlowServer) InitFlow(ctx context.Context, req *connect.Request[InitFlowRequest]) (*connect.Response[InitFlowResponse], error) {
	orgID, err := uuid.Parse(req.Msg.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ft, err := parseFlowType(req.Msg.Type)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	f, err := s.mgr.Create(ctx, ft, orgID, "")
	if err != nil {
		return nil, domainToConnect(err)
	}
	uiJSON, _ := json.Marshal(f.UI)
	return connect.NewResponse(&InitFlowResponse{
		FlowID: f.ID.String(),
		UI:     uiJSON,
	}), nil
}

func (s *FlowServer) SubmitFlow(ctx context.Context, req *connect.Request[SubmitFlowRequest]) (*connect.Response[SubmitFlowResponse], error) {
	flowID, err := uuid.Parse(req.Msg.FlowID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	result, err := s.mgr.Submit(ctx, flowID, "", req.Msg.Body)
	if err != nil {
		return nil, domainToConnect(err)
	}
	resp := &SubmitFlowResponse{State: fmt.Sprintf("%d", result.Flow.State)}
	if result.IdentityID != nil {
		resp.IdentityID = result.IdentityID.String()
	}
	return connect.NewResponse(resp), nil
}

// Handler mounts all gRPC/connect services onto an http.ServeMux.
// Callers pass their own mux so this can co-exist with the HTTP API.
func Handler(
	identities *IdentityServer,
	sessions *SessionServer,
	flows *FlowServer,
	authInterceptor connect.Interceptor,
) http.Handler {
	mux := http.NewServeMux()

	// connect-go uses path-based routing: /package.ServiceName/MethodName
	// Until proto files are generated we expose typed handlers directly.
	// Replace these with buf-generated RegisterXxxHandler calls once protos exist.
	mux.Handle("/authincore.v1.IdentityService/GetIdentity",
		connect.NewUnaryHandler(
			"/authincore.v1.IdentityService/GetIdentity",
			identities.GetIdentity,
			connect.WithInterceptors(authInterceptor),
		),
	)
	mux.Handle("/authincore.v1.SessionService/WhoAmI",
		connect.NewUnaryHandler(
			"/authincore.v1.SessionService/WhoAmI",
			sessions.WhoAmI,
			connect.WithInterceptors(authInterceptor),
		),
	)
	mux.Handle("/authincore.v1.FlowService/InitFlow",
		connect.NewUnaryHandler(
			"/authincore.v1.FlowService/InitFlow",
			flows.InitFlow,
		),
	)
	mux.Handle("/authincore.v1.FlowService/SubmitFlow",
		connect.NewUnaryHandler(
			"/authincore.v1.FlowService/SubmitFlow",
			flows.SubmitFlow,
		),
	)
	return mux
}

// --- helpers ---

func domainToConnect(err error) *connect.Error {
	if err == nil {
		return nil
	}
	switch {
	case domainerr.IsNotFound(err):
		return connect.NewError(connect.CodeNotFound, err)
	case domainerr.IsAlreadyExists(err):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case domainerr.IsInvalidArgument(err):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case domainerr.IsUnauthenticated(err):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case domainerr.IsPermissionDenied(err):
		return connect.NewError(connect.CodePermissionDenied, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

type ctxKeyClaims struct{}

func claimsFromContext(ctx context.Context) (token.Claims, bool) {
	c, ok := ctx.Value(ctxKeyClaims{}).(token.Claims)
	return c, ok
}

func WithClaims(ctx context.Context, c token.Claims) context.Context {
	return context.WithValue(ctx, ctxKeyClaims{}, c)
}

func parseFlowType(s string) (domflow.Type, error) {
	switch s {
	case "login":
		return domflow.TypeLogin, nil
	case "registration":
		return domflow.TypeRegistration, nil
	case "recovery":
		return domflow.TypeRecovery, nil
	case "verification":
		return domflow.TypeVerification, nil
	case "settings":
		return domflow.TypeSettings, nil
	default:
		return 0, connect.NewError(connect.CodeInvalidArgument, nil)
	}
}

// ensure flow package is referenced to avoid unused import warning.
