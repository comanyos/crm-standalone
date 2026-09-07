package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of message templates (message_template.go,
// migrations/0030_message_templates.sql).

const messageTemplateColumns = `id, name, channel_kind, subject, body, created_by, created_at, updated_at`

func scanMessageTemplate(row pgx.Row) (*MessageTemplate, error) {
	var v MessageTemplate
	if err := row.Scan(&v.ID, &v.Name, &v.ChannelKind, &v.Subject, &v.Body,
		&v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateMessageTemplate stores new wording.
func (s *PgStore) CreateMessageTemplate(ctx context.Context, scope Scope, v *MessageTemplate) (*MessageTemplate, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMessageTemplate(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()

	out, err := scanMessageTemplate(s.db.QueryRow(ctx, scope,
		`INSERT INTO message_templates
		   (tenant_id, id, name, channel_kind, subject, body, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		 RETURNING `+messageTemplateColumns,
		scope.TenantID, record.ID, record.Name, record.ChannelKind,
		record.Subject, record.Body, record.CreatedBy, now))
	if err != nil {
		if isNameUniqueViolation(err) {
			return nil, fmt.Errorf("%w: a %s template named %q already exists",
				ErrConflict, record.ChannelKind, record.Name)
		}
		return nil, fmt.Errorf("create message template: %w", err)
	}
	return out, nil
}

// GetMessageTemplate reads one template.
func (s *PgStore) GetMessageTemplate(ctx context.Context, scope Scope, id string) (*MessageTemplate, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanMessageTemplate(s.db.QueryRow(ctx, scope,
		`SELECT `+messageTemplateColumns+` FROM message_templates
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get message template: %w", err)
	}
	return out, nil
}

// ListMessageTemplates returns the tenant's templates, optionally narrowed to
// one channel, ordered by name.
func (s *PgStore) ListMessageTemplates(ctx context.Context, scope Scope, channelKind string) ([]*MessageTemplate, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+messageTemplateColumns+` FROM message_templates
		  WHERE tenant_id = $1 AND ($2 = '' OR channel_kind = $2)
		  ORDER BY channel_kind, name, id`,
		scope.TenantID, channelKind)
	if err != nil {
		return nil, fmt.Errorf("list message templates: %w", err)
	}
	defer rows.Close()

	out := make([]*MessageTemplate, 0, 8)
	for rows.Next() {
		v, err := scanMessageTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("list message templates: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list message templates: %w", err)
	}
	return out, nil
}

// UpdateMessageTemplate rewrites one template's wording. created_by is not
// updatable: it records who introduced this wording, and a later editor does
// not become its author.
func (s *PgStore) UpdateMessageTemplate(ctx context.Context, scope Scope, v *MessageTemplate) (*MessageTemplate, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMessageTemplate(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	out, err := scanMessageTemplate(s.db.QueryRow(ctx, scope,
		`UPDATE message_templates
		    SET name = $3, channel_kind = $4, subject = $5, body = $6, updated_at = now()
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING `+messageTemplateColumns,
		scope.TenantID, record.ID, record.Name, record.ChannelKind, record.Subject, record.Body))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isNameUniqueViolation(err) {
			return nil, fmt.Errorf("%w: a %s template named %q already exists",
				ErrConflict, record.ChannelKind, record.Name)
		}
		return nil, fmt.Errorf("update message template: %w", err)
	}
	return out, nil
}

// DeleteMessageTemplate removes one.
func (s *PgStore) DeleteMessageTemplate(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM message_templates WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete message template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
