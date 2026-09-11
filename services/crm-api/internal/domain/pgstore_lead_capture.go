package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of lead capture (lead_capture.go,
// migrations/0031_lead_capture.sql).

const captureEndpointColumns = `id, name, token, source_kind, secret_ciphertext, field_mapping,
	default_owner_id, status, created_by, created_at, updated_at, last_delivery_at, last_error`

func scanCaptureEndpoint(row pgx.Row) (*LeadCaptureEndpoint, error) {
	var v LeadCaptureEndpoint
	var mapping []byte
	var lastDelivery *time.Time
	if err := row.Scan(&v.ID, &v.Name, &v.Token, &v.SourceKind, &v.SecretCiphertext, &mapping,
		&v.DefaultOwnerID, &v.Status, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt,
		&lastDelivery, &v.LastError); err != nil {
		return nil, err
	}
	if len(mapping) > 0 {
		if err := json.Unmarshal(mapping, &v.FieldMapping); err != nil {
			return nil, fmt.Errorf("decode field mapping: %w", err)
		}
	}
	if lastDelivery != nil {
		v.LastDeliveryAt = *lastDelivery
	}
	return &v, nil
}

func encodeCaptureMapping(mapping map[string]string) ([]byte, error) {
	if mapping == nil {
		mapping = map[string]string{}
	}
	return json.Marshal(mapping)
}

// CreateLeadCaptureEndpoint issues one URL.
func (s *PgStore) CreateLeadCaptureEndpoint(
	ctx context.Context, scope Scope, v *LeadCaptureEndpoint,
) (*LeadCaptureEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneLeadCaptureEndpoint(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	mapping, err := encodeCaptureMapping(record.FieldMapping)
	if err != nil {
		return nil, err
	}
	now := nowStamp()

	out, err := scanCaptureEndpoint(s.db.QueryRow(ctx, scope,
		`INSERT INTO lead_capture_endpoints
		   (tenant_id, id, name, token, source_kind, secret_ciphertext, field_mapping,
		    default_owner_id, status, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		 RETURNING `+captureEndpointColumns,
		scope.TenantID, record.ID, record.Name, record.Token, record.SourceKind,
		record.SecretCiphertext, mapping, record.DefaultOwnerID, record.Status,
		record.CreatedBy, now))
	if err != nil {
		if isNameUniqueViolation(err) {
			return nil, fmt.Errorf("%w: that endpoint token is already in use", ErrConflict)
		}
		return nil, fmt.Errorf("create capture endpoint: %w", err)
	}
	return out, nil
}

// GetLeadCaptureEndpoint reads one endpoint.
func (s *PgStore) GetLeadCaptureEndpoint(ctx context.Context, scope Scope, id string) (*LeadCaptureEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanCaptureEndpoint(s.db.QueryRow(ctx, scope,
		`SELECT `+captureEndpointColumns+` FROM lead_capture_endpoints
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get capture endpoint: %w", err)
	}
	return out, nil
}

// ListLeadCaptureEndpoints returns the tenant's endpoints, newest first.
func (s *PgStore) ListLeadCaptureEndpoints(ctx context.Context, scope Scope) ([]*LeadCaptureEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+captureEndpointColumns+` FROM lead_capture_endpoints
		  WHERE tenant_id = $1 ORDER BY created_at DESC, id`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list capture endpoints: %w", err)
	}
	defer rows.Close()

	out := make([]*LeadCaptureEndpoint, 0, 8)
	for rows.Next() {
		v, err := scanCaptureEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("list capture endpoints: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list capture endpoints: %w", err)
	}
	return out, nil
}

// UpdateLeadCaptureEndpoint changes an endpoint's name, mapping, owner or
// status. The token and secret columns are deliberately absent from the SET
// list: a token that changed would silently break every caller already
// configured against it, and rotating a secret is its own operation.
func (s *PgStore) UpdateLeadCaptureEndpoint(
	ctx context.Context, scope Scope, v *LeadCaptureEndpoint,
) (*LeadCaptureEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	if err := validateCaptureMapping(v.FieldMapping); err != nil {
		return nil, err
	}
	if strings.TrimSpace(v.Name) == "" {
		return nil, fmt.Errorf("%w: a capture endpoint needs a name", ErrInvalid)
	}
	if v.Status != CaptureActive && v.Status != CaptureDisabled {
		return nil, fmt.Errorf("%w: endpoint status must be %q or %q",
			ErrInvalid, CaptureActive, CaptureDisabled)
	}
	mapping, err := encodeCaptureMapping(v.FieldMapping)
	if err != nil {
		return nil, err
	}

	out, err := scanCaptureEndpoint(s.db.QueryRow(ctx, scope,
		`UPDATE lead_capture_endpoints
		    SET name = $3, field_mapping = $4, default_owner_id = $5, status = $6, updated_at = now()
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING `+captureEndpointColumns,
		scope.TenantID, v.ID, v.Name, mapping, v.DefaultOwnerID, v.Status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update capture endpoint: %w", err)
	}
	return out, nil
}

// RotateLeadCaptureSecret replaces the sealed signing secret.
func (s *PgStore) RotateLeadCaptureSecret(
	ctx context.Context, scope Scope, id string, sealed []byte,
) (*LeadCaptureEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if len(sealed) == 0 {
		return nil, fmt.Errorf("%w: a rotation needs a sealed secret", ErrInvalid)
	}
	out, err := scanCaptureEndpoint(s.db.QueryRow(ctx, scope,
		`UPDATE lead_capture_endpoints
		    SET secret_ciphertext = $3, updated_at = now()
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING `+captureEndpointColumns,
		scope.TenantID, id, sealed))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("rotate capture secret: %w", err)
	}
	return out, nil
}

// ResolveCaptureEndpoint answers "which tenant does this token belong to" for
// a POST that carries no credentials.
//
// It runs on the drain pool for the same reason ResolveGoogleOAuthStateTenant
// does: there is no tenant to bind yet, so RLS would filter the answer away.
// It is held to the same discipline — identifiers and the sealed secret only,
// and migration 0031 grants crm_drain SELECT on this table alone, never on
// lead_capture_deliveries, which holds the personal data people submitted.
func (s *PgStore) ResolveCaptureEndpoint(ctx context.Context, token string) (CaptureEndpointRef, error) {
	if strings.TrimSpace(token) == "" {
		return CaptureEndpointRef{}, ErrNotFound
	}
	var ref CaptureEndpointRef
	err := s.drain.QueryRow(ctx,
		`SELECT tenant_id, id, secret_ciphertext, status
		   FROM lead_capture_endpoints WHERE token = $1`, token).
		Scan(&ref.TenantID, &ref.EndpointID, &ref.SecretCiphertext, &ref.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CaptureEndpointRef{}, ErrNotFound
		}
		return CaptureEndpointRef{}, fmt.Errorf("resolve capture endpoint: %w", err)
	}
	return ref, nil
}

// RecordCaptureDelivery writes one delivery, reporting whether this caller
// created it.
//
// ON CONFLICT DO NOTHING against the (tenant, endpoint, dedup_key) index is
// the idempotency: every webhook provider retries, so a repeat is the
// ordinary case, and the second arrival must lose the race rather than create
// a second lead. It returns false with the stored row, so the caller can tell
// the provider "accepted" either way.
func (s *PgStore) RecordCaptureDelivery(
	ctx context.Context, scope Scope, v *LeadCaptureDelivery,
) (*LeadCaptureDelivery, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if v == nil {
		return nil, false, ErrInvalid
	}
	record := cloneLeadCaptureDelivery(v)
	if err := record.Validate(); err != nil {
		return nil, false, err
	}
	if record.ID == "" {
		record.ID = newID()
	}

	out, err := scanCaptureDelivery(s.db.QueryRow(ctx, scope,
		`INSERT INTO lead_capture_deliveries
		   (tenant_id, id, endpoint_id, dedup_key, outcome, reason, raw_payload, lead_id, received_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		 ON CONFLICT (tenant_id, endpoint_id, dedup_key) DO NOTHING
		 RETURNING `+captureDeliveryColumns,
		scope.TenantID, record.ID, record.EndpointID, record.DedupKey,
		record.Outcome, record.Reason, record.RawPayload, record.LeadID))
	if errors.Is(err, pgx.ErrNoRows) {
		reclaimed, reclaimErr := scanCaptureDelivery(s.db.QueryRow(ctx, scope,
			`UPDATE lead_capture_deliveries
			    SET outcome = $4, reason = '', lead_id = '', raw_payload = $5, received_at = now()
			  WHERE tenant_id = $1 AND endpoint_id = $2 AND dedup_key = $3 AND outcome = $6
			  RETURNING `+captureDeliveryColumns,
			scope.TenantID, record.EndpointID, record.DedupKey, record.Outcome,
			record.RawPayload, DeliveryFailed))
		if reclaimErr == nil {
			return reclaimed, true, nil
		}
		if !errors.Is(reclaimErr, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("reclaim failed delivery: %w", reclaimErr)
		}
		existing, getErr := scanCaptureDelivery(s.db.QueryRow(ctx, scope,
			`SELECT `+captureDeliveryColumns+` FROM lead_capture_deliveries
			  WHERE tenant_id = $1 AND endpoint_id = $2 AND dedup_key = $3`,
			scope.TenantID, record.EndpointID, record.DedupKey))
		if getErr != nil {
			return nil, false, fmt.Errorf("read existing delivery: %w", getErr)
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("record capture delivery: %w", err)
	}
	return out, true, nil
}

// CreateCapturedLead commits the provider delivery and every consequence of
// accepting it as one tenant-bound transaction. Every nested store call joins
// scopedDB.transaction through the context, including the outbox write made
// by CreateLead.
func (s *PgStore) CreateCapturedLead(
	ctx context.Context, scope Scope, endpointID, dedupKey, rawPayload string, lead *Lead,
) (delivery *LeadCaptureDelivery, stored *Lead, created bool, err error) {
	if !scope.Valid() {
		return nil, nil, false, ErrNoTenant
	}
	err = s.db.transaction(ctx, scope, func(txCtx context.Context) error {
		var txErr error
		delivery, created, txErr = s.RecordCaptureDelivery(txCtx, scope, &LeadCaptureDelivery{
			EndpointID: endpointID, DedupKey: dedupKey,
			Outcome: DeliveryCreated, RawPayload: rawPayload,
		})
		if txErr != nil || !created {
			return txErr
		}
		stored, txErr = s.CreateLead(txCtx, scope, lead)
		if txErr != nil {
			return txErr
		}
		if txErr = s.LinkCaptureDeliveryLead(txCtx, scope, delivery.ID, stored.ID); txErr != nil {
			return txErr
		}
		if txErr = s.TouchCaptureEndpoint(txCtx, scope, endpointID, CaptureReasonNone); txErr != nil {
			return txErr
		}
		delivery.LeadID = stored.ID
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return delivery, stored, created, nil
}

const captureDeliveryColumns = `id, endpoint_id, dedup_key, outcome, reason, raw_payload, lead_id, received_at`

func scanCaptureDelivery(row pgx.Row) (*LeadCaptureDelivery, error) {
	var v LeadCaptureDelivery
	if err := row.Scan(&v.ID, &v.EndpointID, &v.DedupKey, &v.Outcome,
		&v.Reason, &v.RawPayload, &v.LeadID, &v.ReceivedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// LinkCaptureDeliveryLead records which lead a delivery became.
func (s *PgStore) LinkCaptureDeliveryLead(ctx context.Context, scope Scope, deliveryID, leadID string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE lead_capture_deliveries SET lead_id = $3
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, deliveryID, leadID)
	if err != nil {
		return fmt.Errorf("link capture delivery lead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FailCaptureDelivery releases a delivery claim for a provider retry while
// retaining the failed attempt in the tenant-visible delivery log.
func (s *PgStore) FailCaptureDelivery(ctx context.Context, scope Scope, deliveryID, reason string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE lead_capture_deliveries SET outcome = $3, reason = $4
		  WHERE tenant_id = $1 AND id = $2 AND lead_id = ''`,
		scope.TenantID, deliveryID, DeliveryFailed, reason)
	if err != nil {
		return fmt.Errorf("fail capture delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCaptureDeliveries returns one endpoint's deliveries, newest first —
// the log that makes this feature debuggable without a support call.
func (s *PgStore) ListCaptureDeliveries(
	ctx context.Context, scope Scope, endpointID string, limit int,
) ([]*LeadCaptureDelivery, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+captureDeliveryColumns+` FROM lead_capture_deliveries
		  WHERE tenant_id = $1 AND ($2 = '' OR endpoint_id = $2)
		  ORDER BY received_at DESC, id DESC
		  LIMIT $3`, scope.TenantID, endpointID, limit)
	if err != nil {
		return nil, fmt.Errorf("list capture deliveries: %w", err)
	}
	defer rows.Close()

	out := make([]*LeadCaptureDelivery, 0, 8)
	for rows.Next() {
		v, err := scanCaptureDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("list capture deliveries: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list capture deliveries: %w", err)
	}
	return out, nil
}

// TouchCaptureEndpoint records the outcome of the most recent delivery, so an
// administrator can see at a glance which endpoint has stopped working.
func (s *PgStore) TouchCaptureEndpoint(ctx context.Context, scope Scope, id, reason string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	_, err := s.db.Exec(ctx, scope,
		`UPDATE lead_capture_endpoints
		    SET last_delivery_at = now(), last_error = $3
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id, reason)
	if err != nil {
		return fmt.Errorf("touch capture endpoint: %w", err)
	}
	return nil
}
