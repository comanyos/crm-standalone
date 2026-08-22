package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgStore's half of the metadata layer's definitions (task_v2 Phase 1.1).
//
// Every statement goes through s.db (scopedDB), so 0015's policy applies and a
// definition is reachable only from the tenant that owns it.

const customFieldCols = `id, object_type, field_key, label, data_type, config, required,
	archived_at, created_at, updated_at`

// uniqueViolation is PostgreSQL's SQLSTATE for a unique constraint breach.
const uniqueViolation = "23505"

func scanCustomFieldDefinition(row pgx.Row) (*CustomFieldDefinition, error) {
	v := &CustomFieldDefinition{}
	var config []byte
	var archivedAt *time.Time
	err := row.Scan(&v.ID, &v.ObjectType, &v.FieldKey, &v.Label, &v.DataType,
		&config, &v.Required, &archivedAt, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if archivedAt != nil {
		v.ArchivedAt = *archivedAt
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &v.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config of custom field %s: %w", v.ID, err)
		}
	}
	return v, nil
}

// CreateCustomFieldDefinition stores a new field definition.
func (s *PgStore) CreateCustomFieldDefinition(
	ctx context.Context, scope Scope, v *CustomFieldDefinition,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	config, err := v.MarshalConfig()
	if err != nil {
		return nil, err
	}

	// Counted before the insert rather than enforced by a constraint: a
	// per-tenant-per-object row cap is not something a CHECK can express, and
	// a trigger would put the rule somewhere neither store shares. The race
	// this leaves — two concurrent creates both seeing count-1 — overshoots
	// the cap by one, which is a ceiling on document size, not a correctness
	// boundary.
	var live int
	if err := s.db.QueryRow(ctx, scope,
		`SELECT count(*) FROM custom_field_definitions
		  WHERE tenant_id = $1 AND object_type = $2 AND archived_at IS NULL`,
		scope.TenantID, v.ObjectType).Scan(&live); err != nil {
		return nil, fmt.Errorf("count custom fields: %w", err)
	}
	if live >= MaxFieldsPerObject {
		return nil, fmt.Errorf("%w: %s already has the maximum of %d custom fields",
			ErrConflict, v.ObjectType, MaxFieldsPerObject)
	}

	if v.ID == "" {
		v.ID = newID()
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	// A field that arrives already retired is a definition nothing can write
	// to, so creation never accepts an archive stamp from the caller.
	v.ArchivedAt = time.Time{}

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO custom_field_definitions
		     (tenant_id, id, object_type, field_key, label, data_type, config, required,
		      archived_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULL,$9,$10)`,
		scope.TenantID, v.ID, v.ObjectType, v.FieldKey, v.Label, v.DataType,
		config, v.Required, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		// The unique constraint is the authority on key collisions, including
		// against an archived definition — an archived field still owns its
		// key, because its values are still sitting in rows under it.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, fmt.Errorf("%w: custom field %q already exists on %s",
				ErrConflict, v.FieldKey, v.ObjectType)
		}
		return nil, fmt.Errorf("create custom field: %w", err)
	}
	return cloneCustomFieldDefinition(v), nil
}

// GetCustomFieldDefinition returns one definition by id.
func (s *PgStore) GetCustomFieldDefinition(
	ctx context.Context, scope Scope, id string,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v, err := scanCustomFieldDefinition(s.db.QueryRow(ctx, scope,
		`SELECT `+customFieldCols+` FROM custom_field_definitions
		  WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("get custom field %s: %w", id, err)
	}
	return v, nil
}

// ListCustomFieldDefinitions returns one object's definitions, by field key.
func (s *PgStore) ListCustomFieldDefinitions(
	ctx context.Context, scope Scope, objectType string, includeArchived bool,
) ([]*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+customFieldCols+` FROM custom_field_definitions
		  WHERE tenant_id = $1 AND object_type = $2
		    AND ($3 OR archived_at IS NULL)
		  ORDER BY field_key`,
		scope.TenantID, normalizeObjectType(objectType), includeArchived)
	if err != nil {
		return nil, fmt.Errorf("list custom fields: %w", err)
	}
	defer rows.Close()

	out := make([]*CustomFieldDefinition, 0, 8)
	for rows.Next() {
		v, err := scanCustomFieldDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateCustomFieldDefinition changes a field's label, requiredness and
// config. See applyCustomFieldUpdate for why the other three are immutable.
func (s *PgStore) UpdateCustomFieldDefinition(
	ctx context.Context, scope Scope, v *CustomFieldDefinition,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	current, err := s.GetCustomFieldDefinition(ctx, scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if err := applyCustomFieldUpdate(current, v); err != nil {
		return nil, err
	}
	config, err := current.MarshalConfig()
	if err != nil {
		return nil, err
	}
	current.UpdatedAt = time.Now().UTC()

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE custom_field_definitions
		    SET label = $3, config = $4, required = $5, updated_at = $6
		  WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, current.ID, current.Label, config, current.Required, current.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update custom field: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return cloneCustomFieldDefinition(current), nil
}

// ArchiveCustomFieldDefinition retires a field. Idempotent: archiving an
// already-archived field keeps the original stamp rather than moving it, so
// the record of when it was retired survives a repeated call.
func (s *PgStore) ArchiveCustomFieldDefinition(
	ctx context.Context, scope Scope, id string,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	now := time.Now().UTC()
	v, err := scanCustomFieldDefinition(s.db.QueryRow(ctx, scope,
		`UPDATE custom_field_definitions
		    SET archived_at = COALESCE(archived_at, $3), updated_at = $3
		  WHERE tenant_id = $1 AND id = $2
		 RETURNING `+customFieldCols,
		scope.TenantID, id, now))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("archive custom field %s: %w", id, err)
	}
	return v, nil
}
