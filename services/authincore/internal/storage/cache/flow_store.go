package cache

import (
	"context"
	"fmt"
	"time"

	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

// FlowStore caches in-flight self-service flows in Redis.
// The Postgres repo is the durable store; Redis is a read-through cache
// to avoid a DB round-trip on every flow fetch during multi-step submissions.
type FlowStore struct {
	c *Client
}

func NewFlowStore(c *Client) *FlowStore {
	return &FlowStore{c: c}
}

func (fs *FlowStore) Set(ctx context.Context, f *domflow.Flow) error {
	ttl := time.Until(f.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("flow already expired")
	}
	return fs.c.Set(ctx, FlowKey(f.ID.String()), f, ttl)
}

func (fs *FlowStore) Get(ctx context.Context, id uuid.UUID) (*domflow.Flow, error) {
	var f domflow.Flow
	if err := fs.c.Get(ctx, FlowKey(id.String()), &f); err != nil {
		return nil, fmt.Errorf("flow cache miss for %s: %w", id, err)
	}
	return &f, nil
}

func (fs *FlowStore) Delete(ctx context.Context, id uuid.UUID) error {
	return fs.c.Del(ctx, FlowKey(id.String()))
}
