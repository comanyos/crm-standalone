// Package leadcapture turns an unauthenticated POST into a Lead in the right
// tenant (task_crm.md §1, docs/LEAD_CAPTURE.md).
//
// The whole package is one decision applied repeatedly: the caller is
// unauthenticated, so it learns as little as possible. Every outcome that a
// well-behaved provider cannot act on differently is reported identically —
// an unknown token, a bad signature and a disabled endpoint all produce the
// same response — because a distinguishable answer is an oracle for whether a
// token is real. The reason lives in the delivery log, where the tenant can
// see it and the caller cannot.
package leadcapture

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/metrics"
	"github.com/santoshguru/crm/internal/secrets"
)

// SignatureHeader is where a caller puts the HMAC.
const SignatureHeader = "X-CRM-Signature"

// AssociatedData is the AEAD binding for one endpoint's signing secret.
//
// It exists as a function so no call site can derive the binding slightly
// differently from another — the property that a ciphertext moved to another
// tenant fails to open holds only while every caller builds this the same
// way. The prefix differs from every other sealed value in this service, so a
// capture secret can never be opened as a Google token set or the reverse.
func AssociatedData(tenantID, endpointID string) []byte {
	return []byte("lead-capture:" + tenantID + ":" + endpointID)
}

// Result is what the HTTP handler needs to answer with. It deliberately
// carries no detail the caller should not have: Accepted is almost always
// true, and Reason is for the log.
type Result struct {
	Accepted bool
	Reason   string
	LeadID   string
}

// Store is the slice of domain.Store this service uses.
type Store interface {
	ResolveCaptureEndpoint(ctx context.Context, token string) (domain.CaptureEndpointRef, error)
	GetLeadCaptureEndpoint(ctx context.Context, scope domain.Scope, id string) (*domain.LeadCaptureEndpoint, error)
	RecordCaptureDelivery(ctx context.Context, scope domain.Scope, v *domain.LeadCaptureDelivery) (*domain.LeadCaptureDelivery, bool, error)
	CreateCapturedLead(ctx context.Context, scope domain.Scope, endpointID, dedupKey, rawPayload string, lead *domain.Lead) (*domain.LeadCaptureDelivery, *domain.Lead, bool, error)
	TouchCaptureEndpoint(ctx context.Context, scope domain.Scope, id, reason string) error
}

// Service ingests deliveries.
type Service struct {
	store   Store
	sealer  *secrets.Sealer
	log     *slog.Logger
	limiter *rateLimiter
	now     func() time.Time
}

// New wires the service. A nil sealer is a configuration error rather than a
// degraded mode: without it no signature can be checked, and an endpoint that
// cannot check signatures is an open lead-injection hole.
func New(store Store, sealer *secrets.Sealer, log *slog.Logger) (*Service, error) {
	if store == nil || sealer == nil {
		return nil, errors.New("leadcapture: a store and a sealer are required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:   store,
		sealer:  sealer,
		log:     log,
		limiter: newRateLimiter(perEndpointPerMinute),
		now:     time.Now,
	}, nil
}

// Deliver handles one POST.
//
// The order is deliberate: resolve, rate-limit, verify, then parse. Parsing
// before verifying would mean an unauthenticated caller could hand arbitrary
// JSON to a decoder, and rate-limiting after verifying would mean the
// expensive check runs for every request an attacker cares to send.
func (s *Service) Deliver(ctx context.Context, token string, body []byte, signature string) Result {
	ref, err := s.store.ResolveCaptureEndpoint(ctx, token)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonStoreFailed).Inc()
			return Result{Accepted: false, Reason: domain.CaptureReasonStoreFailed}
		}
		// Deliberately the same answer a bad signature gets. A caller must
		// not be able to test whether a token is real.
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonUnknownEndpoint).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonUnknownEndpoint}
	}
	scope := domain.Scope{TenantID: ref.TenantID, OrgID: ref.TenantID}

	if !s.limiter.allow(ref.EndpointID, s.now()) {
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonRateLimited).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonRateLimited}
	}

	secret, err := s.sealer.Open(ref.SecretCiphertext, AssociatedData(ref.TenantID, ref.EndpointID))
	if err != nil {
		// The row exists but will not open: a rotated sealing key, or a row
		// copied between tenants. Either way nothing can be verified, so
		// nothing is accepted.
		s.log.Warn("a lead capture endpoint's secret could not be opened",
			"tenant", ref.TenantID, "endpoint", ref.EndpointID)
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonBadSignature).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonBadSignature}
	}

	if !domain.VerifyCaptureSignature(string(secret), body, signature) {
		s.record(ctx, scope, ref.EndpointID, body, nil,
			domain.DeliveryRejected, domain.CaptureReasonBadSignature, "")
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonBadSignature).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonBadSignature}
	}

	// Only now is the endpoint's configuration read, under its own tenant
	// scope — after the request has proved it holds the signing secret.
	if ref.Status != domain.CaptureActive {
		s.record(ctx, scope, ref.EndpointID, body, nil,
			domain.DeliveryRejected, domain.CaptureReasonDisabled, "")
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonDisabled).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonDisabled}
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		s.record(ctx, scope, ref.EndpointID, body, nil,
			domain.DeliveryRejected, domain.CaptureReasonMalformed, "")
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonMalformed).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonMalformed}
	}

	endpoint, err := s.store.GetLeadCaptureEndpoint(ctx, scope, ref.EndpointID)
	if err != nil {
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonStoreFailed).Inc()
		return Result{Accepted: false, Reason: domain.CaptureReasonStoreFailed}
	}

	lead, usable := domain.LeadFromCapture(endpoint, payload)
	if !usable {
		// A mapping that matches nothing in the payload. Creating a blank
		// lead would hide the misconfiguration behind a row somebody has to
		// clean up later; recording it as rejected puts it in the delivery
		// log where the administrator will look.
		s.record(ctx, scope, ref.EndpointID, body, payload,
			domain.DeliveryRejected, domain.CaptureReasonNoLeadFields, "")
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonNoLeadFields).Inc()
		return Result{Accepted: true, Reason: domain.CaptureReasonNoLeadFields}
	}

	// Claim, Lead row, lead.created outbox event, delivery linkage and endpoint
	// health are one transaction. A process crash can therefore leave either
	// all of that fact or none of it — never a "created" delivery with no
	// recoverable Lead.
	delivery, stored, created, err := s.store.CreateCapturedLead(
		ctx, scope, ref.EndpointID, domain.CaptureDedupKey(payload, body), string(body), lead)
	if err != nil {
		s.log.Warn("a captured lead transaction failed",
			"tenant", ref.TenantID, "endpoint", ref.EndpointID)
		_ = s.store.TouchCaptureEndpoint(ctx, scope, ref.EndpointID, domain.CaptureReasonStoreFailed)
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.CaptureReasonStoreFailed).Inc()
		return Result{Accepted: false, Reason: domain.CaptureReasonStoreFailed}
	}
	if !created {
		metrics.LeadCaptureDeliveries.WithLabelValues(domain.DeliveryDuplicate).Inc()
		return Result{Accepted: true, Reason: domain.DeliveryDuplicate, LeadID: delivery.LeadID}
	}

	metrics.LeadCaptureDeliveries.WithLabelValues(domain.DeliveryCreated).Inc()
	return Result{Accepted: true, Reason: domain.CaptureReasonNone, LeadID: stored.ID}
}

// record writes a delivery row for an outcome that produced no lead. Failures
// are logged rather than returned: the caller has already been told the
// outcome it is allowed to know, and a delivery log that could not be written
// must not change what a provider is told.
func (s *Service) record(
	ctx context.Context, scope domain.Scope, endpointID string,
	body []byte, payload map[string]any, outcome, reason, leadID string,
) {
	key := domain.CaptureDedupKey(payload, body)
	if _, _, err := s.store.RecordCaptureDelivery(ctx, scope, &domain.LeadCaptureDelivery{
		EndpointID: endpointID, DedupKey: reason + ":" + key,
		Outcome: outcome, Reason: reason, RawPayload: string(body), LeadID: leadID,
	}); err != nil {
		s.log.Warn("a lead capture delivery could not be recorded",
			"tenant", scope.TenantID, "endpoint", endpointID, "reason", reason)
		return
	}
	_ = s.store.TouchCaptureEndpoint(ctx, scope, endpointID, reason)
}

// perEndpointPerMinute bounds one endpoint's deliveries.
//
// ponytail: in-process, so it bounds one instance rather than the deployment.
// Adequate and honestly labelled — a horizontally scaled deployment needs a
// shared counter (Redis is already a dependency). The body cap is the harder
// bound and that one is not per-instance.
const perEndpointPerMinute = 120

// rateLimiter is a fixed-window counter per endpoint.
//
// A fixed window rather than a token bucket because the failure it guards is
// "somebody found the URL and is hammering it", not "smooth out a burst" —
// and a window that occasionally allows 2x at a boundary is fine for that,
// while a bucket needs per-key state nobody would tune.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	windows map[string]window
}

type window struct {
	start time.Time
	count int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, windows: make(map[string]window)}
}

func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.windows[key]
	if !ok || now.Sub(current.start) >= time.Minute {
		r.windows[key] = window{start: now, count: 1}
		// Sweep windows nobody has used for a while, so a deployment that
		// churns endpoints does not grow this map without bound.
		if len(r.windows) > 1024 {
			for k, w := range r.windows {
				if now.Sub(w.start) >= time.Minute {
					delete(r.windows, k)
				}
			}
		}
		return true
	}
	if current.count >= r.limit {
		return false
	}
	current.count++
	r.windows[key] = current
	return true
}

// NormalizeSignature trims the header a caller sent. Exported so the handler
// and the tests agree on what leniency exists — which is only whitespace.
func NormalizeSignature(raw string) string { return strings.TrimSpace(raw) }
