package gmail

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The background poller: what makes synchronisation happen without anybody
// pressing a button.
//
// It is shaped like internal/dispatch's outbox drain, and for the same
// reasons — a loop that keeps running when nobody is calling, that treats a
// provider outage as something to survive rather than to crash on, and whose
// one cross-tenant step returns identifiers and nothing else.

// DefaultInterval is how often the poller looks for mailboxes to sync.
//
// A minute is a deliberate compromise. Gmail's per-user quota is generous but
// not unlimited, and each pass costs one list or history call plus one fetch
// per message; a tighter loop would spend most of its quota discovering that
// nothing changed. Users who want their mail now have the Sync now button.
const DefaultInterval = time.Minute

// mailboxesPerCycle bounds how many mailboxes one cycle claims, so a
// deployment with thousands does not try to sync all of them at once.
const mailboxesPerCycle = 25

// Poller syncs due mailboxes on an interval.
type Poller struct {
	store    domain.Store
	syncer   *Syncer
	interval time.Duration
	log      *slog.Logger

	// rnd spreads the tick so several replicas started together do not wake
	// in lockstep and contend for the same leases.
	rnd *rand.Rand
}

// NewPoller builds a poller. A zero interval uses DefaultInterval.
func NewPoller(store domain.Store, syncer *Syncer, interval time.Duration, log *slog.Logger) *Poller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		store:    store,
		syncer:   syncer,
		interval: interval,
		log:      log,
		//nolint:gosec // jitter, not a security decision
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run polls until the context is cancelled. It returns rather than exiting
// the process on error: Gmail being unreachable must not take the CRM down.
func (p *Poller) Run(ctx context.Context) {
	p.log.Info("gmail sync poller started", "interval", p.interval)
	defer p.log.Info("gmail sync poller stopped")

	timer := time.NewTimer(p.next())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			entries, err := p.Once(ctx, time.Now().UTC())
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				// No tenant, mailbox or provider detail in worker logs.
				p.log.Error("gmail sync cycle failed")
			} else if entries > 0 {
				p.log.Info("gmail sync recorded timeline entries", "entries", entries)
			}
			timer.Reset(p.next())
		}
	}
}

func (p *Poller) next() time.Duration {
	jitter := time.Duration(p.rnd.Int63n(int64(p.interval / 4)))
	return p.interval + jitter
}

// Once runs one cycle and reports how many timeline entries it wrote.
//
// Each mailbox is synced under its own real Scope; the only thing crossing a
// tenant boundary is the list of identifiers DueGmailMailboxes returns.
//
// One mailbox's failure does not stop the cycle. A single expired credential
// or a mailbox whose owner revoked consent must not stop everyone else's mail
// from syncing, which is what an early return would do.
func (p *Poller) Once(ctx context.Context, now time.Time) (int, error) {
	mailboxes, err := p.store.DueGmailMailboxes(ctx, now, mailboxesPerCycle)
	if err != nil {
		return 0, err
	}

	entries := 0
	for _, mailbox := range mailboxes {
		if ctx.Err() != nil {
			return entries, ctx.Err()
		}
		result, err := p.syncer.SyncMailbox(ctx,
			domain.Scope{TenantID: mailbox.TenantID}, mailbox.OwnerID)
		entries += result.EntriesCreated
		if err == nil || errors.Is(err, domain.ErrConflict) {
			// ErrConflict is another replica holding the lease, which is the
			// normal outcome of running more than one and not a failure.
			continue
		}
		// The reason is already recorded on the mailbox by SyncMailbox, which
		// is where an operator or the user will look. The log line says only
		// that a mailbox failed, without naming it.
		p.log.Warn("gmail sync pass failed for one mailbox")
	}
	return entries, nil
}

// Start runs the poller in the background when synchronisation is configured,
// and reports whether it did.
//
// When it is not configured this starts nothing and says so at INFO rather
// than WARN: a CRM without Google credentials is a correctly configured CRM
// that has not enabled the feature, which is different from the outbox case
// where an unconfigured bus means events silently pile up.
func Start(
	ctx context.Context, store domain.Store, syncer *Syncer, interval time.Duration, log *slog.Logger,
) bool {
	if log == nil {
		log = slog.Default()
	}
	if syncer == nil {
		log.Info("Google is not configured — Gmail synchronisation is not running")
		return false
	}
	go NewPoller(store, syncer, interval, log).Run(ctx)
	return true
}
