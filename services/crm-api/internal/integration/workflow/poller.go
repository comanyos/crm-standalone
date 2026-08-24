package workflow

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The background runner, shaped like the outbox dispatcher and the two sync
// pollers before it: a loop that keeps running when nobody is calling, that
// survives one tenant's failure, and whose only cross-tenant step returns
// identifiers.

// DefaultInterval is how often the runner looks for work.
//
// Ten seconds rather than the outbox dispatcher's five. Automation is
// user-visible — somebody is waiting for the task a rule creates — but it
// reads a batch per tenant per tick, so a tighter loop mostly discovers that
// nothing has happened.
const DefaultInterval = 10 * time.Second

// tenantsPerCycle bounds how many tenants one cycle claims.
const tenantsPerCycle = 25

// Poller runs automation on an interval.
type Poller struct {
	store    domain.Store
	engine   *Engine
	interval time.Duration
	log      *slog.Logger
	//nolint:gosec // jitter, not a security decision
	rnd *rand.Rand
}

// NewPoller builds a runner. A zero interval uses DefaultInterval.
func NewPoller(store domain.Store, engine *Engine, interval time.Duration, log *slog.Logger) *Poller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		store: store, engine: engine, interval: interval, log: log,
		//nolint:gosec // jitter, not a security decision
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run polls until the context is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.log.Info("workflow runner started", "interval", p.interval)
	defer p.log.Info("workflow runner stopped")

	timer := time.NewTimer(p.next())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			actions, err := p.Once(ctx, time.Now().UTC())
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				p.log.Error("workflow cycle failed")
			} else if actions > 0 {
				p.log.Info("workflow performed actions", "actions", actions)
			}
			timer.Reset(p.next())
		}
	}
}

func (p *Poller) next() time.Duration {
	jitter := time.Duration(p.rnd.Int63n(int64(p.interval / 4)))
	return p.interval + jitter
}

// Once runs one cycle and reports how many actions it performed.
//
// One tenant's failure never stops the cycle: a single rule pointing at a
// deleted record must not stop every other organization's automation, which
// is what an early return would do.
func (p *Poller) Once(ctx context.Context, now time.Time) (int, error) {
	tenants, err := p.store.DueWorkflowTenants(ctx, now, tenantsPerCycle)
	if err != nil {
		return 0, err
	}

	actions := 0
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return actions, ctx.Err()
		}
		result, err := p.engine.RunTenant(ctx, domain.Scope{TenantID: tenant})
		actions += result.ActionsPerformed
		if err == nil || errors.Is(err, domain.ErrConflict) {
			continue
		}
		// The reason is on the execution row, which is where a rule author
		// looks. The log names no tenant.
		p.log.Warn("a workflow pass failed for one tenant")
	}
	return actions, nil
}

// Start runs the runner in the background.
//
// Unlike the sync pollers this has no configuration to be missing: automation
// is part of the CRM rather than an integration, so it always runs. A
// deployment with no active rules simply finds no due tenants.
func Start(ctx context.Context, store domain.Store, interval time.Duration, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	go NewPoller(store, New(store, log), interval, log).Run(ctx)
}
