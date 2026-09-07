package domain

import (
	"context"
	"fmt"
	"sort"
)

// The in-memory half of drafted outbound messages (outbound_message.go,
// migrations/0032_outbound_messages.sql).

// DraftOutboundMessage stores one draft, reporting whether this caller made
// it. See PgStore for why the execution id is the idempotency key.
func (s *MemStore) DraftOutboundMessage(
	_ context.Context, scope Scope, v *OutboundMessage,
) (*OutboundMessage, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if v == nil {
		return nil, false, ErrInvalid
	}
	record := cloneOutboundMessage(v)
	if record.Status == "" {
		record.Status = OutboundPending
	}
	if err := record.Validate(); err != nil {
		return nil, false, err
	}

	if record.ExecutionID != "" {
		all, err := s.outbound.all(scope)
		if err != nil {
			return nil, false, err
		}
		for _, existing := range all {
			if existing.ExecutionID == record.ExecutionID {
				return cloneOutboundMessage(existing), false, nil
			}
		}
	}

	stored, err := s.outbound.create(scope, record)
	if err != nil {
		return nil, false, err
	}
	return stored, true, nil
}

// GetOutboundMessage reads one draft.
func (s *MemStore) GetOutboundMessage(_ context.Context, scope Scope, id string) (*OutboundMessage, error) {
	return s.outbound.get(scope, id)
}

// ListOutboundMessages returns drafts, newest first, optionally narrowed to a
// status and to one record.
func (s *MemStore) ListOutboundMessages(
	_ context.Context, scope Scope, filter OutboundMessageFilter, limit int,
) ([]*OutboundMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	all, err := s.outbound.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*OutboundMessage, 0, 8)
	for _, message := range all {
		if filter.Status != "" && message.Status != filter.Status {
			continue
		}
		if filter.SubjectID != "" &&
			(message.SubjectID != filter.SubjectID || message.SubjectType != filter.SubjectType) {
			continue
		}
		if filter.AssigneeID != "" && message.AssigneeID != filter.AssigneeID {
			continue
		}
		out = append(out, cloneOutboundMessage(message))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ResolveOutboundMessage moves a pending draft to sent or discarded.
func (s *MemStore) ResolveOutboundMessage(
	_ context.Context, scope Scope, id, status, actor, reason string,
) (*OutboundMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.outbound.get(scope, id)
	if err != nil {
		return nil, err
	}
	if existing.Status != OutboundPending {
		return nil, fmt.Errorf("%w: this message is already %s", ErrConflict, existing.Status)
	}
	switch status {
	case OutboundSent:
		existing.Status, existing.SentBy, existing.SentAt = OutboundSent, actor, nowStamp()
	case OutboundDiscarded:
		existing.Status, existing.DiscardReason = OutboundDiscarded, reason
	default:
		return nil, fmt.Errorf("%w: a draft may only become %q or %q",
			ErrInvalid, OutboundSent, OutboundDiscarded)
	}
	return s.outbound.update(scope, existing)
}
