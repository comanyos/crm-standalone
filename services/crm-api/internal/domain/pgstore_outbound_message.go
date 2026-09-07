package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of drafted outbound messages (outbound_message.go,
// migrations/0032_outbound_messages.sql).

const outboundMessageColumns = `id, subject_type, subject_id, channel_kind, address, subject, body,
	status, assignee_id, rule_version_id, execution_id, created_at, sent_at, sent_by, discard_reason`

func scanOutboundMessage(row pgx.Row) (*OutboundMessage, error) {
	var v OutboundMessage
	var sentAt *time.Time
	if err := row.Scan(&v.ID, &v.SubjectType, &v.SubjectID, &v.ChannelKind, &v.Address,
		&v.Subject, &v.Body, &v.Status, &v.AssigneeID, &v.RuleVersionID, &v.ExecutionID,
		&v.CreatedAt, &sentAt, &v.SentBy, &v.DiscardReason); err != nil {
		return nil, err
	}
	if sentAt != nil {
		v.SentAt = *sentAt
	}
	return &v, nil
}

// OutboundMessageFilter narrows a queue read.
type OutboundMessageFilter struct {
	Status      string
	SubjectType string
	SubjectID   string
	AssigneeID  string
}

// DraftOutboundMessage stores one draft, reporting whether this caller made
// it.
//
// ON CONFLICT DO NOTHING against the execution index is the idempotency, and
// it exists for a specific window: the engine performs an action and only
// then persists actions_completed, so a crash in between re-runs the action.
// Without this the customer gets the same message drafted twice, and a rep
// sends both.
func (s *PgStore) DraftOutboundMessage(
	ctx context.Context, scope Scope, v *OutboundMessage,
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
	if record.ID == "" {
		record.ID = newID()
	}

	out, err := scanOutboundMessage(s.db.QueryRow(ctx, scope,
		`INSERT INTO outbound_messages
		   (tenant_id, id, subject_type, subject_id, channel_kind, address, subject, body,
		    status, assignee_id, rule_version_id, execution_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
		 ON CONFLICT (tenant_id, execution_id) WHERE execution_id <> '' DO NOTHING
		 RETURNING `+outboundMessageColumns,
		scope.TenantID, record.ID, record.SubjectType, record.SubjectID, record.ChannelKind,
		record.Address, record.Subject, record.Body, record.Status, record.AssigneeID,
		record.RuleVersionID, record.ExecutionID))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := scanOutboundMessage(s.db.QueryRow(ctx, scope,
			`SELECT `+outboundMessageColumns+` FROM outbound_messages
			  WHERE tenant_id = $1 AND execution_id = $2`, scope.TenantID, record.ExecutionID))
		if getErr != nil {
			return nil, false, fmt.Errorf("read existing draft: %w", getErr)
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("draft outbound message: %w", err)
	}
	return out, true, nil
}

// GetOutboundMessage reads one draft.
func (s *PgStore) GetOutboundMessage(ctx context.Context, scope Scope, id string) (*OutboundMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanOutboundMessage(s.db.QueryRow(ctx, scope,
		`SELECT `+outboundMessageColumns+` FROM outbound_messages
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get outbound message: %w", err)
	}
	return out, nil
}

// ListOutboundMessages returns drafts, newest first.
func (s *PgStore) ListOutboundMessages(
	ctx context.Context, scope Scope, filter OutboundMessageFilter, limit int,
) ([]*OutboundMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+outboundMessageColumns+` FROM outbound_messages
		  WHERE tenant_id = $1
		    AND ($2 = '' OR status = $2)
		    AND ($3 = '' OR (subject_type = $4 AND subject_id = $3))
		    AND ($5 = '' OR assignee_id = $5)
		  ORDER BY created_at DESC, id DESC
		  LIMIT $6`,
		scope.TenantID, filter.Status, filter.SubjectID, filter.SubjectType,
		filter.AssigneeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list outbound messages: %w", err)
	}
	defer rows.Close()

	out := make([]*OutboundMessage, 0, 8)
	for rows.Next() {
		v, err := scanOutboundMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("list outbound messages: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list outbound messages: %w", err)
	}
	return out, nil
}

// ResolveOutboundMessage moves a pending draft to sent or discarded.
//
// The WHERE clause requires it to still be pending, so two reps opening the
// same queue cannot both mark one sent — the second gets ErrConflict rather
// than silently overwriting who sent it.
func (s *PgStore) ResolveOutboundMessage(
	ctx context.Context, scope Scope, id, status, actor, reason string,
) (*OutboundMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	var (
		sentBy, discard string
		sent            any
	)
	switch status {
	case OutboundSent:
		if actor == "" {
			return nil, fmt.Errorf("%w: a sent message must record who sent it", ErrInvalid)
		}
		sentBy, sent = actor, nowStamp()
	case OutboundDiscarded:
		discard = reason
	default:
		return nil, fmt.Errorf("%w: a draft may only become %q or %q",
			ErrInvalid, OutboundSent, OutboundDiscarded)
	}

	out, err := scanOutboundMessage(s.db.QueryRow(ctx, scope,
		`UPDATE outbound_messages
		    SET status = $3, sent_by = $4, sent_at = $5, discard_reason = $6
		  WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		  RETURNING `+outboundMessageColumns,
		scope.TenantID, id, status, sentBy, sent, discard))
	if errors.Is(err, pgx.ErrNoRows) {
		// Either it does not exist in this tenant, or somebody already
		// resolved it. Distinguishing the two costs a second read and tells a
		// caller nothing they can act on differently.
		if _, getErr := s.GetOutboundMessage(ctx, scope, id); getErr != nil {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: this message has already been resolved", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve outbound message: %w", err)
	}
	return out, nil
}
