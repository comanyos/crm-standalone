// Package lockout enforces account lockout after repeated authentication failures.
// State is dual-written: Redis for fast lookup, Postgres for durability across restarts.
package lockout

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	maxFailures   = 10
	lockDuration  = 15 * time.Minute
	failureWindow = 15 * time.Minute // rolling window for failure counts
)

// Store is the persistence backend for lockout state.
type Store interface {
	// IncrFailure increments the failure counter for an identity.
	// Returns the new failure count. The store manages the rolling window TTL.
	IncrFailure(ctx context.Context, orgID, identityID uuid.UUID) (int, error)
	// Lock persists a lockout until expiresAt.
	Lock(ctx context.Context, orgID, identityID uuid.UUID, expiresAt time.Time) error
	// IsLocked returns true and the expiry if the identity is currently locked.
	IsLocked(ctx context.Context, orgID, identityID uuid.UUID) (bool, time.Time, error)
	// Unlock clears the lockout (admin action or TTL expiry).
	Unlock(ctx context.Context, orgID, identityID uuid.UUID) error
	// ResetFailures clears the failure counter after a successful login.
	ResetFailures(ctx context.Context, orgID, identityID uuid.UUID) error
}

// Service wraps lockout logic.
type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

// CheckLocked returns an error if the identity is currently locked out.
func (s *Service) CheckLocked(ctx context.Context, orgID, identityID uuid.UUID) error {
	locked, expiresAt, err := s.store.IsLocked(ctx, orgID, identityID)
	if err != nil {
		return fmt.Errorf("lockout check: %w", err)
	}
	if locked {
		remaining := time.Until(expiresAt).Round(time.Second)
		return &LockedError{ExpiresAt: expiresAt, RetryAfter: remaining}
	}
	return nil
}

// RecordFailure increments the failure counter. If it reaches maxFailures, locks the account.
func (s *Service) RecordFailure(ctx context.Context, orgID, identityID uuid.UUID) error {
	count, err := s.store.IncrFailure(ctx, orgID, identityID)
	if err != nil {
		return fmt.Errorf("lockout record failure: %w", err)
	}
	if count >= maxFailures {
		expiresAt := time.Now().UTC().Add(lockDuration)
		if err := s.store.Lock(ctx, orgID, identityID, expiresAt); err != nil {
			return fmt.Errorf("lockout lock: %w", err)
		}
	}
	return nil
}

// RecordSuccess clears failure counters after a successful login.
func (s *Service) RecordSuccess(ctx context.Context, orgID, identityID uuid.UUID) error {
	return s.store.ResetFailures(ctx, orgID, identityID)
}

// AdminUnlock clears a lockout (admin API action).
func (s *Service) AdminUnlock(ctx context.Context, orgID, identityID uuid.UUID) error {
	if err := s.store.Unlock(ctx, orgID, identityID); err != nil {
		return fmt.Errorf("admin unlock: %w", err)
	}
	return s.store.ResetFailures(ctx, orgID, identityID)
}

// LockedError is returned when an account is locked.
type LockedError struct {
	ExpiresAt  time.Time
	RetryAfter time.Duration
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("account locked until %s (retry after %s)", e.ExpiresAt.Format(time.RFC3339), e.RetryAfter)
}
