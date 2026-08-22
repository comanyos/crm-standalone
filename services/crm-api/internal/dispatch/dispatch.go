// Package dispatch drains the transactional outbox onto the event bus.
//
// It is the Track A3 producer seam: nothing in the domain writes to the
// outbox yet, but the drain path exists and is tested against a fake
// publisher so Track B can start calling domain.EnqueueEvent without
// reopening this package. Mirrors leave's and hcm's internal/dispatch/dispatch.go
// exactly.
//
// It lives outside internal/domain because the domain must not import a
// transport, and outside internal/server because a drain loop is not a
// request handler and must keep running when nobody is calling.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/metrics"
	"github.com/santoshguru/crm/internal/platform"
	"github.com/santoshguru/crm/internal/tracing"
)

// DefaultInterval is how often the dispatcher looks for work.
const DefaultInterval = 5 * time.Second

// BusPublisher adapts platform.Publisher to domain.EventPublisher.
type BusPublisher struct {
	pub *platform.Publisher
}

// NewBusPublisher wraps a platform publisher.
func NewBusPublisher(pub *platform.Publisher) *BusPublisher { return &BusPublisher{pub: pub} }

// Publish renders the envelope and hands it to the bus on
// Envelope.Subject() — "crm.<entity_type>.<tenant>".
func (b *BusPublisher) Publish(ctx context.Context, env domain.Envelope) error {
	ctx = tracing.ContextWithTraceParent(ctx, env.TraceParent)
	ctx, span := tracing.StartOutboxPublish(ctx)
	defer span.End()
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("%w: marshal event %s: %v", domain.ErrInvalid, env.ID, err)
	}
	// Envelope.ID is the deduplication key; handing it to JetStream as
	// Nats-Msg-Id means a republished event is dropped by the server.
	return b.pub.PublishWithID(ctx, env.Subject(), payload, env.ID)
}

// Dispatcher periodically drains every tenant's outbox.
type Dispatcher struct {
	store     domain.Store
	publisher domain.EventPublisher
	interval  time.Duration
	log       *slog.Logger

	// rnd spreads the tick so several replicas started together do not wake in
	// lockstep and race for the same rows.
	rnd *rand.Rand
}

// New builds a dispatcher. A zero interval uses DefaultInterval.
func New(store domain.Store, publisher domain.EventPublisher, interval time.Duration, log *slog.Logger) *Dispatcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store:     store,
		publisher: publisher,
		interval:  interval,
		log:       log,
		//nolint:gosec // jitter, not a security decision
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run drains until the context is cancelled. It returns rather than exiting
// the process on error: a bus outage must not take the module down.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("outbox dispatcher started", "interval", d.interval)
	defer d.log.Info("outbox dispatcher stopped")

	timer := time.NewTimer(d.next())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			published, err := d.Once(ctx, time.Now())
			if err != nil {
				if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
					d.log.Error("outbox drain failed")
				}
			} else if published > 0 {
				d.log.Info("outbox drained", "published", published)
			}
			timer.Reset(d.next())
		}
	}
}

func (d *Dispatcher) next() time.Duration {
	jitter := time.Duration(d.rnd.Int63n(int64(d.interval / 4)))
	return d.interval + jitter
}

// Once performs a single drain pass across every tenant with work, and
// reports how many events reached the bus. Each tenant is drained under its
// own real Scope — the only thing that crosses a tenant boundary is the list
// of tenant identifiers returned by DueOutboxTenants.
func (d *Dispatcher) Once(ctx context.Context, now time.Time) (int, error) {
	tenants, err := d.store.DueOutboxTenants(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("find tenants with due events: %w", err)
	}

	total := 0
	var firstErr error
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		scope := domain.Scope{TenantID: tenant}
		stats, err := domain.DrainEventOutboxWithStats(ctx, d.store, scope, d.publisher, now)
		total += stats.Published
		if stats.Published > 0 {
			metrics.OutboxProcessedTotal.WithLabelValues("success").Add(float64(stats.Published))
		}
		if stats.Retried > 0 {
			metrics.OutboxProcessedTotal.WithLabelValues("retry").Add(float64(stats.Retried))
		}
		if stats.Failed > 0 {
			metrics.OutboxProcessedTotal.WithLabelValues("failure").Add(float64(stats.Failed))
		}
		if err != nil {
			// Tenant IDs and payloads are intentionally absent from worker logs.
			d.log.Error("outbox drain failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return total, firstErr
}

// Start runs the dispatcher in the background when a bus is configured, and
// returns whether it did. When no bus is configured it starts NOTHING and
// says so at WARN — an outbox that quietly empties into nowhere looks
// healthier than one that visibly backs up, which is the wrong way round.
func Start(
	ctx context.Context,
	store domain.Store,
	pub *platform.Publisher,
	interval time.Duration,
	log *slog.Logger,
) bool {
	if log == nil {
		log = slog.Default()
	}
	if !pub.Configured() {
		log.Warn("NATS_URL not set — the CRM event outbox will accumulate and nothing will " +
			"hear about domain changes; set NATS_URL to publish")
		return false
	}
	d := New(store, NewBusPublisher(pub), interval, log)
	go d.Run(ctx)
	return true
}
