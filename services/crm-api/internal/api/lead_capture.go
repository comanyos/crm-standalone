package api

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/leadcapture"
)

// Lead capture administration (task_crm.md §1, docs/LEAD_CAPTURE.md).
//
// The deliveries themselves do not come through here — they arrive on an
// unauthenticated HTTP route (internal/server/lead_capture.go). This is only
// the surface for issuing, configuring and inspecting endpoints.
//
// The signing secret is returned exactly once, by Create and by Rotate, and
// there is deliberately no RPC that reads one back. A secret an API can
// return is a secret every future bug — a logging change, an over-broad
// response, a client that caches — can leak.

func leadCaptureEndpointToProto(v *domain.LeadCaptureEndpoint) *crmv1.LeadCaptureEndpoint {
	if v == nil {
		return nil
	}
	mapping := make(map[string]string, len(v.FieldMapping))
	for k, target := range v.FieldMapping {
		mapping[k] = target
	}
	return &crmv1.LeadCaptureEndpoint{
		Id: v.ID, Name: v.Name, Token: v.Token, SourceKind: v.SourceKind,
		FieldMapping: mapping, DefaultOwnerId: v.DefaultOwnerID, Status: v.Status,
		CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
		LastDeliveryAt: optionalTimestamp(v.LastDeliveryAt), LastError: v.LastError,
	}
}

func leadCaptureDeliveryToProto(v *domain.LeadCaptureDelivery) *crmv1.LeadCaptureDelivery {
	if v == nil {
		return nil
	}
	return &crmv1.LeadCaptureDelivery{
		Id: v.ID, EndpointId: v.EndpointID, DedupKey: v.DedupKey,
		Outcome: v.Outcome, Reason: v.Reason, RawPayload: v.RawPayload,
		LeadId: v.LeadID, ReceivedAt: optionalTimestamp(v.ReceivedAt),
	}
}

// captureUnavailable is the error every RPC here returns when the deployment
// configured no sealing key.
//
// FailedPrecondition rather than Unimplemented: the feature exists, this
// deployment has not enabled it, and the message says exactly what to set.
// Unimplemented would send an administrator looking for a missing release.
func captureUnavailable() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errCaptureDisabled)
}

type captureDisabledError struct{}

func (captureDisabledError) Error() string {
	return "lead capture is not enabled on this deployment: set LEAD_CAPTURE_SECRET_KEY " +
		"to a base64-encoded 32-byte key (see docs/LEAD_CAPTURE.md)"
}

var errCaptureDisabled = captureDisabledError{}

// CreateLeadCaptureEndpoint issues one URL and returns its secret once.
func (s *CRMService) CreateLeadCaptureEndpoint(
	ctx context.Context, req *connect.Request[crmv1.CreateLeadCaptureEndpointRequest],
) (*connect.Response[crmv1.CreateLeadCaptureEndpointResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.capture == nil {
		return nil, captureUnavailable()
	}

	token, err := domain.NewCaptureToken()
	if err != nil {
		return nil, connectErr(err)
	}
	secret, err := domain.NewCaptureSecret()
	if err != nil {
		return nil, connectErr(err)
	}

	mapping := make(map[string]string, len(req.Msg.GetFieldMapping()))
	for k, target := range req.Msg.GetFieldMapping() {
		mapping[strings.TrimSpace(k)] = strings.TrimSpace(target)
	}
	if len(mapping) == 0 {
		// A newly issued endpoint must work before an administrator learns a
		// mapping DSL. These names cover our documented contract and the common
		// spelling used by form and ad providers; explicit mappings still win.
		mapping = map[string]string{
			"first_name": "first_name", "last_name": "last_name",
			"email": "contact_email", "phone": "phone",
			"company": "company", "source": "source",
		}
	}

	// The row is created first with the secret sealed against a placeholder
	// binding, then re-sealed against the real endpoint id. The binding names
	// the endpoint, and the endpoint id does not exist until the insert — so
	// either the id is generated here (duplicating the store's job) or the
	// seal happens twice. Twice is the cheaper mistake: the placeholder
	// ciphertext is never used to verify anything, because the row is
	// re-sealed before this call returns.
	endpoint, err := s.store.CreateLeadCaptureEndpoint(ctx, scope, &domain.LeadCaptureEndpoint{
		Name: req.Msg.GetName(), Token: token, SourceKind: domain.SourceKindGeneric,
		SecretCiphertext: []byte("pending"), Status: domain.CaptureActive,
		FieldMapping: mapping, DefaultOwnerID: req.Msg.GetDefaultOwnerId(),
		CreatedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}

	sealed, err := s.capture.Seal([]byte(secret),
		leadcapture.AssociatedData(scope.TenantID, endpoint.ID))
	if err != nil {
		return nil, connectErr(err)
	}
	endpoint, err = s.store.RotateLeadCaptureSecret(ctx, scope, endpoint.ID, sealed)
	if err != nil {
		return nil, connectErr(err)
	}

	return connect.NewResponse(&crmv1.CreateLeadCaptureEndpointResponse{
		Endpoint:      leadCaptureEndpointToProto(endpoint),
		SigningSecret: secret,
		DeliveryUrl:   s.captureDeliveryURL(endpoint.Token),
	}), nil
}

// ListLeadCaptureEndpoints returns the tenant's endpoints.
func (s *CRMService) ListLeadCaptureEndpoints(
	ctx context.Context, _ *connect.Request[crmv1.ListLeadCaptureEndpointsRequest],
) (*connect.Response[crmv1.ListLeadCaptureEndpointsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	endpoints, err := s.store.ListLeadCaptureEndpoints(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.LeadCaptureEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out = append(out, leadCaptureEndpointToProto(endpoint))
	}
	return connect.NewResponse(&crmv1.ListLeadCaptureEndpointsResponse{
		Endpoints: out, BaseUrl: s.captureDeliveryURL(""),
	}), nil
}

// UpdateLeadCaptureEndpoint changes a name, mapping, owner or status.
func (s *CRMService) UpdateLeadCaptureEndpoint(
	ctx context.Context, req *connect.Request[crmv1.UpdateLeadCaptureEndpointRequest],
) (*connect.Response[crmv1.UpdateLeadCaptureEndpointResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	mapping := make(map[string]string, len(req.Msg.GetFieldMapping()))
	for k, target := range req.Msg.GetFieldMapping() {
		mapping[strings.TrimSpace(k)] = strings.TrimSpace(target)
	}
	status := req.Msg.GetStatus()
	if status == "" {
		status = domain.CaptureActive
	}
	endpoint, err := s.store.UpdateLeadCaptureEndpoint(ctx, scope, &domain.LeadCaptureEndpoint{
		ID: req.Msg.GetId(), Name: req.Msg.GetName(), FieldMapping: mapping,
		DefaultOwnerID: req.Msg.GetDefaultOwnerId(), Status: status,
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateLeadCaptureEndpointResponse{
		Endpoint: leadCaptureEndpointToProto(endpoint),
	}), nil
}

// RotateLeadCaptureSecret replaces the signing secret and returns the new one
// once. The previous secret stops working immediately — which is the point,
// and why the UI has to say so before anybody presses it.
func (s *CRMService) RotateLeadCaptureSecret(
	ctx context.Context, req *connect.Request[crmv1.RotateLeadCaptureSecretRequest],
) (*connect.Response[crmv1.RotateLeadCaptureSecretResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.capture == nil {
		return nil, captureUnavailable()
	}
	// Read first, through the caller's own scope, so another tenant's
	// endpoint id is NotFound rather than silently rotated.
	if _, err := s.store.GetLeadCaptureEndpoint(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}

	secret, err := domain.NewCaptureSecret()
	if err != nil {
		return nil, connectErr(err)
	}
	sealed, err := s.capture.Seal([]byte(secret),
		leadcapture.AssociatedData(scope.TenantID, req.Msg.GetId()))
	if err != nil {
		return nil, connectErr(err)
	}
	endpoint, err := s.store.RotateLeadCaptureSecret(ctx, scope, req.Msg.GetId(), sealed)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.RotateLeadCaptureSecretResponse{
		Endpoint: leadCaptureEndpointToProto(endpoint), SigningSecret: secret,
	}), nil
}

// ListLeadCaptureDeliveries is the delivery log — the feature's own debugger.
func (s *CRMService) ListLeadCaptureDeliveries(
	ctx context.Context, req *connect.Request[crmv1.ListLeadCaptureDeliveriesRequest],
) (*connect.Response[crmv1.ListLeadCaptureDeliveriesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	deliveries, err := s.store.ListCaptureDeliveries(ctx, scope,
		req.Msg.GetEndpointId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.LeadCaptureDelivery, 0, len(deliveries))
	for _, delivery := range deliveries {
		out = append(out, leadCaptureDeliveryToProto(delivery))
	}
	return connect.NewResponse(&crmv1.ListLeadCaptureDeliveriesResponse{Deliveries: out}), nil
}

// captureDeliveryURL assembles the URL a caller configures.
//
// Built server-side rather than in the browser so a client cannot get the
// path wrong, and from configuration rather than from a request header — an
// origin taken from a Host header is how a poisoned link ends up in somebody's
// integration settings.
func (s *CRMService) captureDeliveryURL(token string) string {
	base := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if base == "" {
		// No public origin is configured, so there is nothing truthful to
		// prefix with. The UI shows the path and says to prepend the API's
		// own origin, which is better than guessing one.
		return "/integrations/lead-capture/" + token
	}
	return base + "/integrations/lead-capture/" + token
}
