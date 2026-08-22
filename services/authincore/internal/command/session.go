package command

import (
	"context"
	"fmt"
	"time"

	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
)

type sessionWriter interface {
	Insert(ctx context.Context, s *domsession.Session) error
	UpdateState(ctx context.Context, id, orgID uuid.UUID, state domsession.State) error
	UpdateExpiry(ctx context.Context, id, orgID uuid.UUID, expiresAt time.Time) error
	RevokeAllByIdentity(ctx context.Context, identityID, orgID uuid.UUID) (int64, error)
}

const defaultSessionTTL = 24 * time.Hour

type CreateSessionCommand struct {
	IdentityID uuid.UUID
	OrgID      uuid.UUID
	Methods    []domsession.AuthMethod
	TTL        time.Duration
}

func (c *Commands) CreateSession(ctx context.Context, cmd CreateSessionCommand) (*domsession.Session, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (*domsession.Session, error) {
		return c.createSession(txCtx, cmd)
	})
}

func (c *Commands) createSession(ctx context.Context, cmd CreateSessionCommand) (*domsession.Session, error) {
	ttl := cmd.TTL
	if ttl == 0 {
		ttl = defaultSessionTTL
	}
	s := domsession.New(cmd.IdentityID, cmd.OrgID, cmd.Methods, ttl)

	if err := c.sessions.Insert(ctx, s); err != nil {
		return nil, fmt.Errorf("create session insert: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateSession, s.ID, s.OrgID, "session.created", s, actorFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("create session build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return nil, fmt.Errorf("create session push event: %w", err)
	}
	return s, nil
}

func (c *Commands) RevokeSession(ctx context.Context, id, orgID uuid.UUID) error {
	_, err := withTransaction(ctx, c.db, func(txCtx context.Context) (struct{}, error) {
		return struct{}{}, c.revokeSession(txCtx, id, orgID)
	})
	return err
}

func (c *Commands) revokeSession(ctx context.Context, id, orgID uuid.UUID) error {
	if err := c.sessions.UpdateState(ctx, id, orgID, domsession.StateInactive); err != nil {
		return fmt.Errorf("revoke session write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateSession, id, orgID, "session.revoked", nil, actorFromContext(ctx))
	if err != nil {
		return fmt.Errorf("revoke session build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return fmt.Errorf("revoke session push event: %w", err)
	}
	return nil
}

func (c *Commands) RevokeAllSessions(ctx context.Context, identityID, orgID uuid.UUID) (int64, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (int64, error) {
		return c.revokeAllSessions(txCtx, identityID, orgID)
	})
}

func (c *Commands) revokeAllSessions(ctx context.Context, identityID, orgID uuid.UUID) (int64, error) {
	n, err := c.sessions.RevokeAllByIdentity(ctx, identityID, orgID)
	if err != nil {
		return 0, fmt.Errorf("revoke all sessions: %w", err)
	}
	if n > 0 {
		ev, err := eventstore.NewEvent(eventstore.AggregateSession, identityID, orgID, "session.revoked_all",
			map[string]any{"count": n}, actorFromContext(ctx))
		if err != nil {
			return n, fmt.Errorf("revoke all sessions build event: %w", err)
		}
		if err := c.es.Push(ctx, ev); err != nil {
			return n, fmt.Errorf("revoke all sessions push event: %w", err)
		}
	}
	return n, nil
}

func (c *Commands) ExtendSession(ctx context.Context, id, orgID uuid.UUID, ttl time.Duration) error {
	_, err := withTransaction(ctx, c.db, func(txCtx context.Context) (struct{}, error) {
		return struct{}{}, c.extendSession(txCtx, id, orgID, ttl)
	})
	return err
}

func (c *Commands) extendSession(ctx context.Context, id, orgID uuid.UUID, ttl time.Duration) error {
	newExpiry := time.Now().UTC().Add(ttl)
	if err := c.sessions.UpdateExpiry(ctx, id, orgID, newExpiry); err != nil {
		return fmt.Errorf("extend session write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateSession, id, orgID, "session.extended",
		map[string]any{"expires_at": newExpiry}, actorFromContext(ctx))
	if err != nil {
		return fmt.Errorf("extend session build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return fmt.Errorf("extend session push event: %w", err)
	}
	return nil
}
