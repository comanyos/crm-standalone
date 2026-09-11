package domain

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// The in-memory half of lead capture (lead_capture.go,
// migrations/0031_lead_capture.sql).

// captureTokens indexes endpoint tokens across tenants, which is what makes
// ResolveCaptureEndpoint possible without a tenant. It mirrors the unique
// index the migration puts below RLS, and it is a separate map rather than a
// scan so a collision is impossible here too.
type captureTables struct {
	mu     sync.Mutex
	tokens map[string]CaptureEndpointRef
}

func newCaptureTables() *captureTables {
	return &captureTables{tokens: make(map[string]CaptureEndpointRef)}
}

// CreateLeadCaptureEndpoint issues one URL.
func (s *MemStore) CreateLeadCaptureEndpoint(
	_ context.Context, scope Scope, v *LeadCaptureEndpoint,
) (*LeadCaptureEndpoint, error) {
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneLeadCaptureEndpoint(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()
	if _, taken := s.capture.tokens[record.Token]; taken {
		return nil, ErrConflict
	}

	stored, err := s.captureEndpoint.create(scope, record)
	if err != nil {
		return nil, err
	}
	s.capture.tokens[stored.Token] = CaptureEndpointRef{
		TenantID: scope.TenantID, EndpointID: stored.ID,
		SecretCiphertext: stored.SecretCiphertext, Status: stored.Status,
	}
	return stored, nil
}

// GetLeadCaptureEndpoint reads one endpoint.
func (s *MemStore) GetLeadCaptureEndpoint(_ context.Context, scope Scope, id string) (*LeadCaptureEndpoint, error) {
	return s.captureEndpoint.get(scope, id)
}

// ListLeadCaptureEndpoints returns the tenant's endpoints, newest first.
func (s *MemStore) ListLeadCaptureEndpoints(_ context.Context, scope Scope) ([]*LeadCaptureEndpoint, error) {
	all, err := s.captureEndpoint.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*LeadCaptureEndpoint, 0, len(all))
	for _, endpoint := range all {
		out = append(out, cloneLeadCaptureEndpoint(endpoint))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateLeadCaptureEndpoint changes an endpoint's name, mapping, owner or
// status. The token and the sealed secret are not updatable here: rotating a
// secret is its own operation, and a token that changed would silently break
// every caller already configured against it.
func (s *MemStore) UpdateLeadCaptureEndpoint(
	_ context.Context, scope Scope, v *LeadCaptureEndpoint,
) (*LeadCaptureEndpoint, error) {
	if v == nil {
		return nil, ErrInvalid
	}
	existing, err := s.captureEndpoint.get(scope, v.ID)
	if err != nil {
		return nil, err
	}
	record := cloneLeadCaptureEndpoint(v)
	record.Token = existing.Token
	record.SecretCiphertext = existing.SecretCiphertext
	record.CreatedBy = existing.CreatedBy
	record.LastDeliveryAt = existing.LastDeliveryAt
	record.LastError = existing.LastError
	if err := record.Validate(); err != nil {
		return nil, err
	}

	stored, err := s.captureEndpoint.update(scope, record)
	if err != nil {
		return nil, err
	}

	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()
	s.capture.tokens[stored.Token] = CaptureEndpointRef{
		TenantID: scope.TenantID, EndpointID: stored.ID,
		SecretCiphertext: stored.SecretCiphertext, Status: stored.Status,
	}
	return stored, nil
}

// RotateLeadCaptureSecret replaces the sealed signing secret.
func (s *MemStore) RotateLeadCaptureSecret(
	_ context.Context, scope Scope, id string, sealed []byte,
) (*LeadCaptureEndpoint, error) {
	existing, err := s.captureEndpoint.get(scope, id)
	if err != nil {
		return nil, err
	}
	existing.SecretCiphertext = append([]byte(nil), sealed...)
	stored, err := s.captureEndpoint.update(scope, existing)
	if err != nil {
		return nil, err
	}

	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()
	s.capture.tokens[stored.Token] = CaptureEndpointRef{
		TenantID: scope.TenantID, EndpointID: stored.ID,
		SecretCiphertext: stored.SecretCiphertext, Status: stored.Status,
	}
	return stored, nil
}

// ResolveCaptureEndpoint answers "which tenant does this token belong to".
func (s *MemStore) ResolveCaptureEndpoint(_ context.Context, token string) (CaptureEndpointRef, error) {
	if strings.TrimSpace(token) == "" {
		return CaptureEndpointRef{}, ErrNotFound
	}
	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()
	ref, ok := s.capture.tokens[token]
	if !ok {
		return CaptureEndpointRef{}, ErrNotFound
	}
	return ref, nil
}

// RecordCaptureDelivery writes one delivery, or reports the existing one.
func (s *MemStore) RecordCaptureDelivery(
	_ context.Context, scope Scope, v *LeadCaptureDelivery,
) (*LeadCaptureDelivery, bool, error) {
	if v == nil {
		return nil, false, ErrInvalid
	}
	record := cloneLeadCaptureDelivery(v)
	if err := record.Validate(); err != nil {
		return nil, false, err
	}

	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()

	all, err := s.captureDelivery.all(scope)
	if err != nil {
		return nil, false, err
	}
	for _, existing := range all {
		if existing.EndpointID == record.EndpointID && existing.DedupKey == record.DedupKey {
			if existing.Outcome == DeliveryFailed {
				existing.Outcome = record.Outcome
				existing.Reason = ""
				existing.LeadID = ""
				existing.RawPayload = record.RawPayload
				reclaimed, updateErr := s.captureDelivery.update(scope, existing)
				return reclaimed, updateErr == nil, updateErr
			}
			return cloneLeadCaptureDelivery(existing), false, nil
		}
	}

	stored, err := s.captureDelivery.create(scope, record)
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
}

// CreateCapturedLead is the in-memory equivalent of PgStore's transaction.
// compound serializes the multi-table operation; the production guarantee is
// provided by PgStore's real database transaction.
func (s *MemStore) CreateCapturedLead(
	ctx context.Context, scope Scope, endpointID, dedupKey, rawPayload string, lead *Lead,
) (*LeadCaptureDelivery, *Lead, bool, error) {
	if !scope.Valid() {
		return nil, nil, false, ErrNoTenant
	}
	s.compound.Lock()
	defer s.compound.Unlock()

	delivery, created, err := s.RecordCaptureDelivery(ctx, scope, &LeadCaptureDelivery{
		EndpointID: endpointID, DedupKey: dedupKey,
		Outcome: DeliveryCreated, RawPayload: rawPayload,
	})
	if err != nil || !created {
		return delivery, nil, created, err
	}
	stored, err := s.CreateLead(ctx, scope, lead)
	if err != nil {
		_ = s.FailCaptureDelivery(ctx, scope, delivery.ID, CaptureReasonStoreFailed)
		return nil, nil, false, err
	}
	if err := s.LinkCaptureDeliveryLead(ctx, scope, delivery.ID, stored.ID); err != nil {
		_ = s.lead.delete(scope, stored.ID)
		_ = s.FailCaptureDelivery(ctx, scope, delivery.ID, CaptureReasonStoreFailed)
		return nil, nil, false, err
	}
	if err := s.TouchCaptureEndpoint(ctx, scope, endpointID, CaptureReasonNone); err != nil {
		return nil, nil, false, err
	}
	delivery.LeadID = stored.ID
	return delivery, stored, true, nil
}

// LinkCaptureDeliveryLead records which lead a delivery became.
func (s *MemStore) LinkCaptureDeliveryLead(_ context.Context, scope Scope, deliveryID, leadID string) error {
	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()

	delivery, err := s.captureDelivery.get(scope, deliveryID)
	if err != nil {
		return err
	}
	delivery.LeadID = leadID
	_, err = s.captureDelivery.update(scope, delivery)
	return err
}

func (s *MemStore) FailCaptureDelivery(_ context.Context, scope Scope, deliveryID, reason string) error {
	s.capture.mu.Lock()
	defer s.capture.mu.Unlock()
	delivery, err := s.captureDelivery.get(scope, deliveryID)
	if err != nil {
		return err
	}
	if delivery.LeadID != "" {
		return ErrConflict
	}
	delivery.Outcome = DeliveryFailed
	delivery.Reason = reason
	_, err = s.captureDelivery.update(scope, delivery)
	return err
}

// ListCaptureDeliveries returns one endpoint's deliveries, newest first.
func (s *MemStore) ListCaptureDeliveries(
	_ context.Context, scope Scope, endpointID string, limit int,
) ([]*LeadCaptureDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	all, err := s.captureDelivery.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*LeadCaptureDelivery, 0, 8)
	for _, delivery := range all {
		if endpointID == "" || delivery.EndpointID == endpointID {
			out = append(out, cloneLeadCaptureDelivery(delivery))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ReceivedAt.Equal(out[j].ReceivedAt) {
			return out[i].ReceivedAt.After(out[j].ReceivedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// TouchCaptureEndpoint records the outcome of the most recent delivery.
func (s *MemStore) TouchCaptureEndpoint(_ context.Context, scope Scope, id, reason string) error {
	existing, err := s.captureEndpoint.get(scope, id)
	if err != nil {
		return err
	}
	existing.LastDeliveryAt = nowStamp()
	existing.LastError = reason
	_, err = s.captureEndpoint.update(scope, existing)
	return err
}
