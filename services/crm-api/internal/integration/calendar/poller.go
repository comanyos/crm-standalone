package calendar

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The background poller, shaped like Gmail's and the outbox dispatcher's.
//
// It is a separate loop from Gmail's rather than one pass doing both streams,
// which costs a goroutine and buys independent failure: a Gmail quota problem
// must not stop meetings reaching customer timelines, and a calendar whose
// sync token expired must not delay anybody's mail.

// DefaultInterval is how often the poller looks for calendars to sync.
//
// Five minutes rather than Gmail's one. A meeting that moves is worth knowing
// about promptly but not instantly, calendars change far less often than
// mailboxes, and each pass costs a list call whether or not anything changed.
// Users who want it now have the button.
const DefaultInterval = 5 * time.Minute

// calendarsPerCycle bounds how many one cycle claims.
const calendarsPerCycle = 25

// Poller syncs due calendars on an interval.
type Poller struct {
	store    domain.Store
	syncer   *Syncer
	interval time.Duration
	log      *slog.Logger
	//nolint:gosec // jitter, not a security decision
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
		store: store, syncer: syncer, interval: interval, log: log,
		//nolint:gosec // jitter, not a security decision
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run polls until the context is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.log.Info("calendar sync poller started", "interval", p.interval)
	defer p.log.Info("calendar sync poller stopped")

	timer := time.NewTimer(p.next())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			entries, err := p.Once(ctx, time.Now().UTC())
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				p.log.Error("calendar sync cycle failed")
			} else if entries > 0 {
				p.log.Info("calendar sync recorded timeline entries", "entries", entries)
			}
			timer.Reset(p.next())
		}
	}
}

func (p *Poller) next() time.Duration {
	jitter := time.Duration(p.rnd.Int63n(int64(p.interval / 4)))
	return p.interval + jitter
}

// Once runs one cycle and reports how many timeline entries it wrote. One
// calendar's failure never stops the cycle.
func (p *Poller) Once(ctx context.Context, now time.Time) (int, error) {
	calendars, err := p.store.DueCalendarMailboxes(ctx, now, calendarsPerCycle)
	if err != nil {
		return 0, err
	}

	entries := 0
	for _, ref := range calendars {
		if ctx.Err() != nil {
			return entries, ctx.Err()
		}
		result, err := p.syncer.SyncCalendar(ctx,
			domain.Scope{TenantID: ref.TenantID}, ref.OwnerID)
		entries += result.EntriesCreated
		if err == nil || errors.Is(err, domain.ErrConflict) {
			continue
		}
		// The reason is already on the calendar, which is where anyone will
		// look. The log names nothing.
		p.log.Warn("calendar sync pass failed for one account")
	}
	return entries, nil
}

// Start runs the poller in the background when synchronisation is configured.
func Start(
	ctx context.Context, store domain.Store, syncer *Syncer, interval time.Duration, log *slog.Logger,
) bool {
	if log == nil {
		log = slog.Default()
	}
	if syncer == nil {
		log.Info("Google is not configured — Calendar synchronisation is not running")
		return false
	}
	go NewPoller(store, syncer, interval, log).Run(ctx)
	return true
}
