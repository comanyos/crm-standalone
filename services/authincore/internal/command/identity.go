package command

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
)

type identityWriter interface {
	Insert(ctx context.Context, i *identity.Identity) error
	Update(ctx context.Context, i *identity.Identity) error
	Delete(ctx context.Context, id, orgID uuid.UUID) error
}

type identityReader interface {
	ByID(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error)
}

// --- Event payload types ---

type identityCreatedPayload struct {
	SchemaID string          `json:"schema_id"`
	Traits   json.RawMessage `json:"traits"`
}

type identityStateChangedPayload struct {
	State identity.State `json:"state"`
}

// CreateIdentityCommand carries all data needed to create a new identity.
type CreateIdentityCommand struct {
	OrgID    uuid.UUID
	SchemaID string
	Traits   json.RawMessage
}

func (c *Commands) CreateIdentity(ctx context.Context, cmd CreateIdentityCommand) (*identity.Identity, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (*identity.Identity, error) {
		return c.createIdentity(txCtx, cmd)
	})
}

func (c *Commands) createIdentity(ctx context.Context, cmd CreateIdentityCommand) (*identity.Identity, error) {
	if cmd.SchemaID == "" {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "schema_id is required")
	}

	if c.schema != nil {
		if err := c.schema.Validate(ctx, cmd.OrgID.String(), cmd.SchemaID, cmd.Traits); err != nil {
			return nil, fmt.Errorf("traits validation: %w", err)
		}
	}

	i := identity.New(cmd.OrgID, cmd.SchemaID, cmd.Traits)

	if err := c.identities.Insert(ctx, i); err != nil {
		return nil, fmt.Errorf("create identity insert: %w", err)
	}

	ev, err := eventstore.NewEvent(
		eventstore.AggregateIdentity, i.ID, i.OrgID,
		"identity.created",
		identityCreatedPayload{SchemaID: cmd.SchemaID, Traits: cmd.Traits},
		actorFromContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("create identity build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return nil, fmt.Errorf("create identity push event: %w", err)
	}
	return i, nil
}

type UpdateIdentityCommand struct {
	ID     uuid.UUID
	OrgID  uuid.UUID
	Traits json.RawMessage
}

func (c *Commands) UpdateIdentity(ctx context.Context, cmd UpdateIdentityCommand) (*identity.Identity, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (*identity.Identity, error) {
		return c.updateIdentity(txCtx, cmd)
	})
}

func (c *Commands) updateIdentity(ctx context.Context, cmd UpdateIdentityCommand) (*identity.Identity, error) {
	i, err := c.identities.ByID(ctx, cmd.ID, cmd.OrgID)
	if err != nil {
		return nil, fmt.Errorf("update identity fetch: %w", err)
	}
	if cmd.Traits != nil {
		if c.schema != nil {
			if err := c.schema.Validate(ctx, cmd.OrgID.String(), i.SchemaID, cmd.Traits); err != nil {
				return nil, fmt.Errorf("traits validation: %w", err)
			}
		}
		i.Traits = cmd.Traits
	}
	i.UpdatedAt = time.Now().UTC()

	if err := c.identities.Update(ctx, i); err != nil {
		return nil, fmt.Errorf("update identity write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateIdentity, i.ID, i.OrgID, "identity.updated", cmd, actorFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("update identity build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return nil, fmt.Errorf("update identity push event: %w", err)
	}
	return i, nil
}

func (c *Commands) DeactivateIdentity(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (*identity.Identity, error) {
		return c.deactivateIdentity(txCtx, id, orgID)
	})
}

func (c *Commands) deactivateIdentity(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	i, err := c.identities.ByID(ctx, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("deactivate identity fetch: %w", err)
	}
	i.Deactivate()
	if err := c.identities.Update(ctx, i); err != nil {
		return nil, fmt.Errorf("deactivate identity write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateIdentity, i.ID, i.OrgID, "identity.deactivated",
		identityStateChangedPayload{State: i.State}, actorFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("deactivate identity build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return nil, fmt.Errorf("deactivate identity push event: %w", err)
	}
	return i, nil
}

func (c *Commands) ReactivateIdentity(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	return withTransaction(ctx, c.db, func(txCtx context.Context) (*identity.Identity, error) {
		return c.reactivateIdentity(txCtx, id, orgID)
	})
}

func (c *Commands) reactivateIdentity(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	i, err := c.identities.ByID(ctx, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("reactivate identity fetch: %w", err)
	}
	i.Reactivate()
	if err := c.identities.Update(ctx, i); err != nil {
		return nil, fmt.Errorf("reactivate identity write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateIdentity, i.ID, i.OrgID, "identity.reactivated",
		identityStateChangedPayload{State: i.State}, actorFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("reactivate identity build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return nil, fmt.Errorf("reactivate identity push event: %w", err)
	}
	return i, nil
}

func (c *Commands) DeleteIdentity(ctx context.Context, id, orgID uuid.UUID) error {
	_, err := withTransaction(ctx, c.db, func(txCtx context.Context) (struct{}, error) {
		return struct{}{}, c.deleteIdentity(txCtx, id, orgID)
	})
	return err
}

func (c *Commands) deleteIdentity(ctx context.Context, id, orgID uuid.UUID) error {
	if err := c.identities.Delete(ctx, id, orgID); err != nil {
		return fmt.Errorf("delete identity write: %w", err)
	}
	ev, err := eventstore.NewEvent(eventstore.AggregateIdentity, id, orgID, "identity.deleted", nil, actorFromContext(ctx))
	if err != nil {
		return fmt.Errorf("delete identity build event: %w", err)
	}
	if err := c.es.Push(ctx, ev); err != nil {
		return fmt.Errorf("delete identity push event: %w", err)
	}
	return nil
}

// actorFromContext extracts the authenticated actor string from context.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyActor{}).(string); ok {
		return v
	}
	return "system"
}

type ctxKeyActor struct{}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, ctxKeyActor{}, actor)
}
