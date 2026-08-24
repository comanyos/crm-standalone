package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of saved views (saved_views.go, migrations/0025_saved_views.sql).

const savedViewColumns = `id, owner_id, object_type, name, filters, sort, columns, created_at, updated_at`

func scanSavedView(row pgx.Row) (*SavedView, error) {
	var (
		v       SavedView
		filters []byte
		columns []byte
	)
	if err := row.Scan(&v.ID, &v.OwnerID, &v.ObjectType, &v.Name, &filters, &v.Sort, &columns,
		&v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if len(filters) > 0 {
		if err := json.Unmarshal(filters, &v.Filters); err != nil {
			return nil, fmt.Errorf("decode saved view filters: %w", err)
		}
	}
	if len(columns) > 0 {
		if err := json.Unmarshal(columns, &v.Columns); err != nil {
			return nil, fmt.Errorf("decode saved view columns: %w", err)
		}
	}
	return &v, nil
}

func marshalSavedViewFilters(v *SavedView) ([]byte, error) {
	if v.Filters == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v.Filters)
}

func marshalSavedViewColumns(v *SavedView) ([]byte, error) {
	if v.Columns == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v.Columns)
}

// CreateSavedView stores a new view. Names are unique per owner per object
// type, case-insensitively.
func (s *PgStore) CreateSavedView(ctx context.Context, scope Scope, v *SavedView) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneSavedView(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	filters, err := marshalSavedViewFilters(record)
	if err != nil {
		return nil, err
	}
	columns, err := marshalSavedViewColumns(record)
	if err != nil {
		return nil, err
	}
	now := nowStamp()

	out, err := scanSavedView(s.db.QueryRow(ctx, scope,
		`INSERT INTO saved_views (tenant_id, id, owner_id, object_type, name, filters, sort, columns, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		 RETURNING `+savedViewColumns,
		scope.TenantID, record.ID, record.OwnerID, record.ObjectType, record.Name, filters, record.Sort, columns, now))
	if err != nil {
		if isNameUniqueViolation(err) {
			return nil, fmt.Errorf("%w: you already have a view named %q for %s", ErrConflict, record.Name, record.ObjectType)
		}
		return nil, fmt.Errorf("create saved view: %w", err)
	}
	return out, nil
}

// GetSavedView reads one of the caller's own views. ownerID is part of the
// WHERE clause rather than checked afterwards, so a view owned by someone
// else is indistinguishable from one that does not exist.
func (s *PgStore) GetSavedView(ctx context.Context, scope Scope, ownerID, id string) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanSavedView(s.db.QueryRow(ctx, scope,
		`SELECT `+savedViewColumns+` FROM saved_views
		  WHERE tenant_id = $1 AND id = $2 AND owner_id = $3`,
		scope.TenantID, id, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get saved view: %w", err)
	}
	return out, nil
}

// ListSavedViews returns the caller's own views, optionally narrowed to one
// object type, ordered by name.
func (s *PgStore) ListSavedViews(ctx context.Context, scope Scope, ownerID, objectType string) ([]*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if ownerID == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+savedViewColumns+` FROM saved_views
		  WHERE tenant_id = $1 AND owner_id = $2 AND ($3::text = '' OR object_type = $3)
		  ORDER BY name, id`,
		scope.TenantID, ownerID, objectType)
	if err != nil {
		return nil, fmt.Errorf("list saved views: %w", err)
	}
	defer rows.Close()

	out := make([]*SavedView, 0, 8)
	for rows.Next() {
		record, err := scanSavedView(rows)
		if err != nil {
			return nil, fmt.Errorf("scan saved view: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// UpdateSavedView replaces one of the caller's own views.
func (s *PgStore) UpdateSavedView(ctx context.Context, scope Scope, ownerID string, v *SavedView) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneSavedView(v)
	record.OwnerID = ownerID
	if err := record.Validate(); err != nil {
		return nil, err
	}
	filters, err := marshalSavedViewFilters(record)
	if err != nil {
		return nil, err
	}
	columns, err := marshalSavedViewColumns(record)
	if err != nil {
		return nil, err
	}
	out, err := scanSavedView(s.db.QueryRow(ctx, scope,
		`UPDATE saved_views SET name = $4, filters = $5, sort = $6, columns = $7, updated_at = $8
		  WHERE tenant_id = $1 AND id = $2 AND owner_id = $3
		RETURNING `+savedViewColumns,
		scope.TenantID, record.ID, ownerID, record.Name, filters, record.Sort, columns, nowStamp()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isNameUniqueViolation(err) {
			return nil, fmt.Errorf("%w: you already have a view named %q for %s", ErrConflict, record.Name, record.ObjectType)
		}
		return nil, fmt.Errorf("update saved view: %w", err)
	}
	return out, nil
}

// DeleteSavedView removes one of the caller's own views.
func (s *PgStore) DeleteSavedView(ctx context.Context, scope Scope, ownerID, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	result, err := s.db.Exec(ctx, scope,
		`DELETE FROM saved_views WHERE tenant_id = $1 AND id = $2 AND owner_id = $3`,
		scope.TenantID, id, ownerID)
	if err != nil {
		return fmt.Errorf("delete saved view: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
