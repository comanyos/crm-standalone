package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrConflict indicates optimistic concurrency violation or state conflict.
var ErrConflict = errors.New("domain: conflict")

// SourceAppCRM is the origin module for CRM events.
const SourceAppCRM = "crm"

// Envelope matches the event envelope structure published across the suite.
//
// Track A3 builds this seam only — nothing in the domain constructs an
// Envelope yet. Track B decides which state transitions publish, and calls
// EnqueueEvent when it does.
type Envelope struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	TenantID      string          `json:"tenant_id"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	SourceApp     string          `json:"source_app"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	SourceVersion int64           `json:"source_version"`
	Actor         string          `json:"actor"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// WithPayload attaches the record the event is about.
func (e Envelope) WithPayload(v any) (Envelope, error) {
	if v == nil {
		return e, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return e, fmt.Errorf("%w: event payload: %v", ErrInvalid, err)
	}
	e.Payload = b
	return e, nil
}

// Subject is the bus subject this event publishes on:
// "<source_app>.<entity_type>.<tenant>" — the convention this module's
// consumers subscribe against.
func (e Envelope) Subject() string {
	return e.SourceApp + "." + e.EntityType + "." + e.TenantID
}

// Validate checks basic envelope invariants.
func (e Envelope) Validate() error {
	switch {
	case strings.TrimSpace(e.ID) == "":
		return fmt.Errorf("%w: event has no id", ErrInvalid)
	case strings.TrimSpace(e.TenantID) == "":
		return fmt.Errorf("%w: event %s has no tenant", ErrInvalid, e.ID)
	case strings.TrimSpace(e.EntityType) == "":
		return fmt.Errorf("%w: event %s has no entity type", ErrInvalid, e.ID)
	case strings.TrimSpace(e.EntityID) == "":
		return fmt.Errorf("%w: event %s names no record", ErrInvalid, e.ID)
	case e.SourceVersion < 1:
		return fmt.Errorf(
			"%w: event %s has source version %d; a projection cannot detect staleness without one",
			ErrInvalid, e.ID, e.SourceVersion)
	}
	return nil
}
