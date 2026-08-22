package cache

import (
	"context"
	"fmt"
	"time"

	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/google/uuid"
)

// SessionStore wraps the Redis cache for session read/write operations.
type SessionStore struct {
	c   *Client
	ttl time.Duration
}

func NewSessionStore(c *Client, ttl time.Duration) *SessionStore {
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &SessionStore{c: c, ttl: ttl}
}

func (s *SessionStore) Set(ctx context.Context, sess *domsession.Session) error {
	return s.c.Set(ctx, SessionKey(sess.ID.String()), sess, s.ttl)
}

func (s *SessionStore) Get(ctx context.Context, id uuid.UUID) (*domsession.Session, error) {
	var sess domsession.Session
	if err := s.c.Get(ctx, SessionKey(id.String()), &sess); err != nil {
		return nil, fmt.Errorf("session cache miss for %s: %w", id, err)
	}
	return &sess, nil
}

func (s *SessionStore) Delete(ctx context.Context, id uuid.UUID) error {
	return s.c.Del(ctx, SessionKey(id.String()))
}
