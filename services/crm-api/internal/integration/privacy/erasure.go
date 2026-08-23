// Package privacy serves data-subject requests against synced communication
// content, and enforces the retention window.
//
// It sits above the store for one reason: an erasure is three writes and an
// audit event, and every caller must do all four. Leaving that to call sites
// is how one of them ends up erasing content without suppressing collection,
// which reads as success and is undone by the next poll.
package privacy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/metrics"
)

// Service performs erasures and retention sweeps.
type Service struct {
	store domain.Store
	// retention is how long synced content is kept. Zero means indefinitely,
	// which is the default: silently deleting an organization's records
	// because nobody configured this would be the worse failure.
	retention time.Duration
	now       func() time.Time
	log       *slog.Logger
}

// New wires the service. A zero or negative retention disables the sweep.
func New(store domain.Store, retentionDays int, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	retention := time.Duration(0)
	if retentionDays > 0 {
		retention = time.Duration(retentionDays) * 24 * time.Hour
	}
	return &Service{
		store: store, retention: retention,
		now: func() time.Time { return time.Now().UTC() }, log: log,
	}
}

// RetentionEnabled reports whether a window is configured.
func (s *Service) RetentionEnabled() bool { return s != nil && s.retention > 0 }

// RetentionDays reports the configured window in days, for the UI to state.
func (s *Service) RetentionDays() int {
	if !s.RetentionEnabled() {
		return 0
	}
	return int(s.retention / (24 * time.Hour))
}

// EraseForContact serves "delete the email and meetings you hold about me"
// for one Contact.
//
// It suppresses the Contact's own address as part of the same operation.
// That is the difference between erasure and theatre: without it the next
// poll — a minute later for mail — indexes the next message from that person
// as though nothing had been asked.
func (s *Service) EraseForContact(
	ctx context.Context, scope domain.Scope, contactID, actor, reason string,
) (domain.ErasureResult, error) {
	contact, err := s.store.GetContact(ctx, scope, contactID)
	if err != nil {
		return domain.ErasureResult{}, err
	}

	in := domain.EraseSubject{
		SubjectType: domain.SubjectContact,
		SubjectID:   contact.ID,
		Actor:       actor,
		Reason:      reason,
	}
	if address := domain.NormalizeEmail(contact.Email); address != "" {
		in.SuppressAddresses = []string{address}
	}
	return s.erase(ctx, scope, in, false)
}

// EraseForMailbox removes everything one connected account's synchronisation
// produced.
//
// It does not suppress any address: the request here is "undo what my mailbox
// put into this CRM", not "stop tracking these people", and suppressing every
// correspondent of a departing employee would quietly stop their colleagues'
// syncs from recording the same customers.
func (s *Service) EraseForMailbox(
	ctx context.Context, scope domain.Scope, ownerID, actor, reason string,
) (domain.ErasureResult, error) {
	if ownerID == "" {
		return domain.ErasureResult{}, fmt.Errorf("%w: an erasure must name a mailbox", domain.ErrInvalid)
	}
	return s.erase(ctx, scope, domain.EraseSubject{
		OwnerID: ownerID, Actor: actor, Reason: reason,
	}, false)
}

// ApplyRetention erases one mailbox's synced content older than the window.
//
// It runs inside the sync pass that already holds that mailbox's lease rather
// than as a separate sweep. That means no additional cross-tenant read, no
// second scheduler, and — the part that matters — no window in which a pass
// re-indexes content another job is concurrently deleting.
func (s *Service) ApplyRetention(
	ctx context.Context, scope domain.Scope, ownerID string,
) (domain.ErasureResult, error) {
	if !s.RetentionEnabled() {
		return domain.ErasureResult{}, nil
	}
	cutoff := s.now().Add(-s.retention)
	return s.erase(ctx, scope, domain.EraseSubject{
		OwnerID: ownerID,
		Before:  cutoff,
		Actor:   "system",
		Reason:  fmt.Sprintf("retention window of %d days", s.RetentionDays()),
	}, true)
}

// erase performs the store operation and records the audit event.
func (s *Service) erase(
	ctx context.Context, scope domain.Scope, in domain.EraseSubject, automatic bool,
) (domain.ErasureResult, error) {
	result, err := s.store.EraseSyncedContent(ctx, scope, in)
	if err != nil {
		metrics.SyncErasuresTotal.WithLabelValues(outcomeLabel(automatic, "failed")).Inc()
		return domain.ErasureResult{}, err
	}
	if result.Total() == 0 {
		// Nothing to erase is a legitimate answer — most retention sweeps —
		// and writing an audit event for every one of them would bury the
		// erasures that did something.
		metrics.SyncErasuresTotal.WithLabelValues(outcomeLabel(automatic, "nothing_to_erase")).Inc()
		return result, nil
	}

	// A failed audit write does not fail the erasure. The content is already
	// gone, and returning an error would invite a caller to retry an
	// operation that has nothing left to do — while the outbox's own retry
	// still delivers the event.
	if err := domain.EnqueueSyncErasureEvent(ctx, s.store, scope, in, result, automatic, s.now()); err != nil {
		s.log.Error("could not record a sync erasure audit event", "error", err)
	}
	metrics.SyncErasuresTotal.WithLabelValues(outcomeLabel(automatic, "erased")).Inc()
	return result, nil
}

func outcomeLabel(automatic bool, outcome string) string {
	if automatic {
		return "retention_" + outcome
	}
	return "request_" + outcome
}

// Suppress adds one address to the never-match list without erasing anything,
// for somebody who asks not to be tracked going forward.
func (s *Service) Suppress(
	ctx context.Context, scope domain.Scope, address, actor, reason string,
) error {
	return s.store.SuppressSyncAddress(ctx, scope, &domain.SyncSuppression{
		Address: address, Reason: reason, CreatedBy: actor,
	})
}

// Release removes an address suppressed in error.
func (s *Service) Release(ctx context.Context, scope domain.Scope, address string) error {
	err := s.store.ReleaseSyncAddress(ctx, scope, address)
	if errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return err
}

// List returns the tenant's suppression list.
func (s *Service) List(ctx context.Context, scope domain.Scope) ([]*domain.SyncSuppression, error) {
	return s.store.ListSyncSuppressions(ctx, scope)
}
