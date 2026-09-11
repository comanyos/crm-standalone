package domain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Inbound lead capture (task_crm.md §1, docs/LEAD_CAPTURE.md,
// migrations/0031_lead_capture.sql).
//
// An external system POSTs a lead to a URL this CRM issues, and an ordinary
// Lead appears in the right tenant. "Ordinary" is the design: a captured lead
// emits the same lead.created event as one typed in by hand, so the workflow
// engine — round-robin distribution included — fires on it with no change to
// any of them. A captured lead that were its own kind of record would need
// every downstream feature taught about it.
//
// The request carries no principal, so three independent things stand in for
// one: an unguessable token that resolves the tenant, an HMAC signature over
// the exact bytes received, and a dedup key that makes a replay a no-op. Only
// the second authorizes anything; see docs/LEAD_CAPTURE.md.

// Endpoint lifecycle states.
const (
	CaptureActive   = "active"
	CaptureDisabled = "disabled"
)

// SourceKindGeneric is the one source kind v1 ships: a documented
// HMAC-SHA256 contract any system can meet. Per-provider adapters wait for a
// payload that actually refuses to fit — see docs/LEAD_CAPTURE.md.
const SourceKindGeneric = "generic"

// Delivery outcomes.
const (
	DeliveryCreated   = "created"
	DeliveryDuplicate = "duplicate"
	DeliveryRejected  = "rejected"
	DeliveryFailed    = "failed"
)

// The bounded reasons a delivery can carry. Bounded because they reach a
// metric label and a UI, and because a reason built from a payload would put
// somebody's submitted personal data in both.
const (
	CaptureReasonNone            = ""
	CaptureReasonUnknownEndpoint = "unknown_endpoint"
	CaptureReasonBadSignature    = "bad_signature"
	CaptureReasonDisabled        = "endpoint_disabled"
	CaptureReasonMalformed       = "malformed_payload"
	CaptureReasonNoLeadFields    = "no_usable_fields"
	CaptureReasonRateLimited     = "rate_limited"
	CaptureReasonStoreFailed     = "store_failed"
)

const (
	maxCaptureNameLen = 100
	// MaxCaptureBodyBytes bounds one delivery. Generous for a lead form and
	// small enough that a delivery cannot be a memory or storage vector.
	MaxCaptureBodyBytes = 64 * 1024
	// captureTokenBytes is the entropy in an endpoint's URL token. It is not
	// a secret — it appears in the caller's own logs — but it has to be
	// unguessable enough that endpoints cannot be enumerated.
	captureTokenBytes = 32
	// captureSecretBytes is the HMAC signing secret, which *is* a secret.
	captureSecretBytes = 32
)

// LeadCaptureEndpoint is one issued URL.
type LeadCaptureEndpoint struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
	// SecretCiphertext is the sealed HMAC key. There is no plaintext field:
	// the secret is shown once at creation and never read back, only rotated.
	SecretCiphertext []byte            `json:"-"`
	SourceKind       string            `json:"source_kind"`
	FieldMapping     map[string]string `json:"field_mapping"`
	DefaultOwnerID   string            `json:"default_owner_id"`
	Status           string            `json:"status"`
	CreatedBy        string            `json:"created_by"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	LastDeliveryAt   time.Time         `json:"last_delivery_at"`
	LastError        string            `json:"last_error"`
}

// EntityID returns the record's identifier.
func (e *LeadCaptureEndpoint) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *LeadCaptureEndpoint) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record.
func (e *LeadCaptureEndpoint) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Created reports when the endpoint was issued.
func (e *LeadCaptureEndpoint) Created() time.Time { return e.CreatedAt }

// Validate rejects an endpoint that cannot be stored.
func (e *LeadCaptureEndpoint) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no endpoint", ErrInvalid)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: a capture endpoint needs a name", ErrInvalid)
	}
	if len([]rune(e.Name)) > maxCaptureNameLen {
		return fmt.Errorf("%w: endpoint name exceeds %d characters", ErrInvalid, maxCaptureNameLen)
	}
	if len(e.Token) < 32 {
		return fmt.Errorf("%w: a capture endpoint needs an unguessable token", ErrInvalid)
	}
	if len(e.SecretCiphertext) == 0 {
		return fmt.Errorf("%w: a capture endpoint needs a sealed signing secret", ErrInvalid)
	}
	if e.SourceKind != SourceKindGeneric {
		return fmt.Errorf("%w: %q is not a source kind this version supports (only %q)",
			ErrInvalid, e.SourceKind, SourceKindGeneric)
	}
	if e.Status != CaptureActive && e.Status != CaptureDisabled {
		return fmt.Errorf("%w: endpoint status must be %q or %q",
			ErrInvalid, CaptureActive, CaptureDisabled)
	}
	return validateCaptureMapping(e.FieldMapping)
}

func cloneLeadCaptureEndpoint(v *LeadCaptureEndpoint) *LeadCaptureEndpoint {
	if v == nil {
		return nil
	}
	c := *v
	c.SecretCiphertext = append([]byte(nil), v.SecretCiphertext...)
	if v.FieldMapping != nil {
		c.FieldMapping = make(map[string]string, len(v.FieldMapping))
		for k, val := range v.FieldMapping {
			c.FieldMapping[k] = val
		}
	}
	return &c
}

// CaptureLeadFields is the allowlist a mapping may target.
//
// It is the CSV import vocabulary rather than a second one
// (docs/CSV_IMPORT_EXPORT.md): the two features answer the same question —
// "which of my columns is which Lead field" — and having two vocabularies is
// how they come to disagree about what "company" means.
var CaptureLeadFields = map[string]bool{
	"company":       true,
	"contact_email": true,
	"first_name":    true,
	"last_name":     true,
	"phone":         true,
	"source":        true,
	"score":         true,
	"owner_id":      true,
}

// SortedCaptureLeadFields returns the allowlist, sorted, for an editor.
func SortedCaptureLeadFields() []string {
	out := make([]string, 0, len(CaptureLeadFields))
	for field := range CaptureLeadFields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func validateCaptureMapping(mapping map[string]string) error {
	if len(mapping) > 50 {
		return fmt.Errorf("%w: a field mapping may name at most 50 fields", ErrInvalid)
	}
	for incoming, target := range mapping {
		if strings.TrimSpace(incoming) == "" {
			return fmt.Errorf("%w: a mapping cannot have a blank source field", ErrInvalid)
		}
		if !CaptureLeadFields[target] {
			return fmt.Errorf("%w: %q is not a lead field a capture endpoint may set (allowed: %s)",
				ErrInvalid, target, strings.Join(SortedCaptureLeadFields(), ", "))
		}
	}
	return nil
}

// NewCaptureToken returns an endpoint's unguessable URL token.
func NewCaptureToken() (string, error) {
	return randomHex(captureTokenBytes)
}

// NewCaptureSecret returns an endpoint's HMAC signing secret, in the form the
// user copies into their calling system.
func NewCaptureSecret() (string, error) {
	return randomHex(captureSecretBytes)
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%w: entropy source unavailable", ErrInvalid)
	}
	return hex.EncodeToString(buf), nil
}

// SignCapturePayload computes the header value a caller must send.
//
// Exported because the tests, the documentation example and any future
// per-provider adapter must all produce the identical string; a second
// implementation of this is a second opinion about what is signed.
func SignCapturePayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyCaptureSignature reports whether provided matches the body.
//
// hmac.Equal, not ==: a byte-by-byte comparison that returns early leaks how
// much of a guess was right, which over enough attempts is the whole
// signature. The caller is unauthenticated and can retry as often as it
// likes, so this is the one place where that actually matters.
func VerifyCaptureSignature(secret string, body []byte, provided string) bool {
	expected := SignCapturePayload(secret, body)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(provided)))
}

// CaptureDedupKey returns the key that makes a redelivery a no-op.
//
// A provider's own event id when the payload carries one — that is the only
// value that identifies "the same lead" across two posts whose bytes differ
// by a timestamp. Otherwise a hash of the body, which catches the common case
// of a byte-identical retry.
func CaptureDedupKey(payload map[string]any, body []byte) string {
	for _, field := range []string{"id", "event_id", "lead_id", "leadgen_id"} {
		if raw, ok := payload[field]; ok {
			if value := strings.TrimSpace(captureString(raw)); value != "" {
				return "provider:" + value
			}
		}
	}
	sum := sha256.Sum256(body)
	return "body:" + hex.EncodeToString(sum[:])
}

// captureString renders one JSON value as text.
//
// JSON numbers arrive as float64, and a lead score of 5 must not become
// "5.000000" — or, worse, "5e+00" in a company name. Booleans and nulls
// render as nothing rather than "false", since a form that sends null means
// "not answered".
func captureString(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return ""
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

// LeadFromCapture builds a Lead from a payload and an endpoint's mapping.
//
// It reports false when nothing usable was found. A delivery that maps to an
// empty Lead is a misconfigured mapping, not a lead: creating a blank record
// would hide the mistake behind a row somebody has to clean up later.
func LeadFromCapture(endpoint *LeadCaptureEndpoint, payload map[string]any) (*Lead, bool) {
	lead := &Lead{OwnerID: strings.TrimSpace(endpoint.DefaultOwnerID)}
	found := false

	for incoming, target := range endpoint.FieldMapping {
		raw, ok := payload[incoming]
		if !ok {
			continue
		}
		value := strings.TrimSpace(captureString(raw))
		if value == "" {
			continue
		}
		switch target {
		case "first_name":
			lead.FirstName, found = value, true
		case "last_name":
			lead.LastName, found = value, true
		case "phone":
			lead.Phone, found = value, true
		case "company":
			lead.Company, found = value, true
		case "contact_email":
			lead.ContactEmail, found = strings.ToLower(value), true
		case "source":
			lead.Source, found = value, true
		case "owner_id":
			lead.OwnerID, found = value, true
		case "score":
			if score, err := strconv.ParseInt(value, 10, 64); err == nil {
				lead.Score, found = score, true
			}
		}
	}

	if lead.Source == "" {
		// Where a lead came from is the first thing anybody filters on, and a
		// capture endpoint knows the answer even when the payload does not.
		lead.Source = "capture:" + endpoint.Name
	}
	if !found || normalizeLeadIdentity(lead) != nil || lead.Validate() != nil {
		return lead, false
	}
	return lead, true
}

// LeadCaptureDelivery is one POST received, kept as the feature's own
// debugger and audit trail.
type LeadCaptureDelivery struct {
	ID         string    `json:"id"`
	EndpointID string    `json:"endpoint_id"`
	DedupKey   string    `json:"dedup_key"`
	Outcome    string    `json:"outcome"`
	Reason     string    `json:"reason"`
	RawPayload string    `json:"raw_payload"`
	LeadID     string    `json:"lead_id"`
	ReceivedAt time.Time `json:"received_at"`
}

// EntityID returns the record's identifier.
func (e *LeadCaptureDelivery) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *LeadCaptureDelivery) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps ReceivedAt on creation. A delivery is written once.
func (e *LeadCaptureDelivery) SetTimestamps(created, _ time.Time) {
	if e.ReceivedAt.IsZero() && !created.IsZero() {
		e.ReceivedAt = created
	}
}

// Created reports when the delivery arrived.
func (e *LeadCaptureDelivery) Created() time.Time { return e.ReceivedAt }

// Validate rejects a delivery row that cannot be stored.
func (e *LeadCaptureDelivery) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no delivery", ErrInvalid)
	}
	if strings.TrimSpace(e.EndpointID) == "" {
		return fmt.Errorf("%w: a delivery needs an endpoint", ErrInvalid)
	}
	if strings.TrimSpace(e.DedupKey) == "" {
		return fmt.Errorf("%w: a delivery needs a dedup key", ErrInvalid)
	}
	switch e.Outcome {
	case DeliveryCreated, DeliveryDuplicate, DeliveryRejected, DeliveryFailed:
	default:
		return fmt.Errorf("%w: %q is not a delivery outcome", ErrInvalid, e.Outcome)
	}
	return nil
}

func cloneLeadCaptureDelivery(v *LeadCaptureDelivery) *LeadCaptureDelivery {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// CaptureEndpointRef is what resolving an unauthenticated request yields:
// identifiers and the sealed secret, and nothing else.
//
// It is a distinct type from LeadCaptureEndpoint so the cross-tenant lookup
// cannot accidentally return a whole configuration record — the discipline
// DueOutboxTenants and ResolveGoogleOAuthStateTenant are held to.
type CaptureEndpointRef struct {
	TenantID         string
	EndpointID       string
	SecretCiphertext []byte
	Status           string
}
