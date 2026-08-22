// Package redis provides Redis-backed implementations of cache-dependent storage interfaces.
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/storage/cache"
	"github.com/google/uuid"
)

const (
	failureWindow = 15 * time.Minute
	lockDuration  = 30 * time.Minute
)

// LockoutStore implements lockout.Store using Redis only.
// For durability across restarts, pair with postgres.LockoutStore via a composite store.
type LockoutStore struct {
	c *cache.Client
}

func NewLockoutStore(c *cache.Client) *LockoutStore {
	return &LockoutStore{c: c}
}

func failureKey(orgID, identityID uuid.UUID) string {
	return fmt.Sprintf("lockout:failures:%s:%s", orgID, identityID)
}

func lockKey(orgID, identityID uuid.UUID) string {
	return fmt.Sprintf("lockout:lock:%s:%s", orgID, identityID)
}

func (s *LockoutStore) IncrFailure(ctx context.Context, orgID, identityID uuid.UUID) (int, error) {
	count, err := s.c.Incr(ctx, failureKey(orgID, identityID), failureWindow)
	if err != nil {
		return 0, fmt.Errorf("lockout incr failure: %w", err)
	}
	return int(count), nil
}

func (s *LockoutStore) Lock(ctx context.Context, orgID, identityID uuid.UUID, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		ttl = lockDuration
	}
	return s.c.Set(ctx, lockKey(orgID, identityID), expiresAt.Unix(), ttl)
}

func (s *LockoutStore) IsLocked(ctx context.Context, orgID, identityID uuid.UUID) (bool, time.Time, error) {
	var expUnix int64
	if err := s.c.Get(ctx, lockKey(orgID, identityID), &expUnix); err != nil {
		// Cache miss means not locked.
		return false, time.Time{}, nil
	}
	expiresAt := time.Unix(expUnix, 0).UTC()
	if time.Now().UTC().After(expiresAt) {
		return false, time.Time{}, nil
	}
	return true, expiresAt, nil
}

func (s *LockoutStore) Unlock(ctx context.Context, orgID, identityID uuid.UUID) error {
	return s.c.Del(ctx, lockKey(orgID, identityID))
}

func (s *LockoutStore) ResetFailures(ctx context.Context, orgID, identityID uuid.UUID) error {
	return s.c.Del(ctx, failureKey(orgID, identityID))
}
