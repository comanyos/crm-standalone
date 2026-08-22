package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"time"
)

const customObjectCols = `id, api_name, label, plural_label, archived_at, created_at, updated_at`

func scanCustomObject(row pgx.Row) (*CustomObjectDefinition, error) {
	v := &CustomObjectDefinition{}
	var archived *time.Time
	err := row.Scan(&v.ID, &v.APIName, &v.Label, &v.PluralLabel, &archived, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if archived != nil {
		v.ArchivedAt = *archived
	}
	return v, nil
}
func (s *PgStore) CreateCustomObjectDefinition(ctx context.Context, scope Scope, v *CustomObjectDefinition) (*CustomObjectDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err := s.db.Exec(ctx, scope, `INSERT INTO custom_object_definitions (tenant_id,id,api_name,label,plural_label,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, scope.TenantID, v.ID, v.APIName, v.Label, v.PluralLabel, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, fmt.Errorf("%w: custom object already exists", ErrConflict)
		}
		return nil, err
	}
	return cloneCustomObjectDefinition(v), nil
}
func (s *PgStore) GetCustomObjectDefinition(ctx context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	return scanCustomObject(s.db.QueryRow(ctx, scope, `SELECT `+customObjectCols+` FROM custom_object_definitions WHERE tenant_id=$1 AND api_name=$2`, scope.TenantID, normalizeObjectType(apiName)))
}
func (s *PgStore) ListCustomObjectDefinitions(ctx context.Context, scope Scope, includeArchived bool) ([]*CustomObjectDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+customObjectCols+` FROM custom_object_definitions WHERE tenant_id=$1 AND ($2 OR archived_at IS NULL) ORDER BY api_name`, scope.TenantID, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CustomObjectDefinition{}
	for rows.Next() {
		v, err := scanCustomObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PgStore) ArchiveCustomObjectDefinition(ctx context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	now := nowStamp()
	return scanCustomObject(s.db.QueryRow(ctx, scope, `UPDATE custom_object_definitions SET archived_at=COALESCE(archived_at,$3),updated_at=$3 WHERE tenant_id=$1 AND api_name=$2 RETURNING `+customObjectCols, scope.TenantID, normalizeObjectType(apiName), now))
}
func (s *PgStore) requireLiveCustomObject(ctx context.Context, scope Scope, objectType string) error {
	v, err := s.GetCustomObjectDefinition(ctx, scope, objectType)
	if err != nil {
		return err
	}
	if v.Archived() {
		return fmt.Errorf("%w: custom object is archived", ErrInvalid)
	}
	return nil
}

const customRecordCols = `id, object_type, fields, relations, created_at, updated_at`

func scanCustomRecord(row pgx.Row) (*CustomRecord, error) {
	v := &CustomRecord{}
	var fields, relations []byte
	err := row.Scan(&v.ID, &v.ObjectType, &fields, &relations, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var e error
	if v.Fields, e = parseCustomValues(fields); e != nil {
		return nil, e
	}
	if len(relations) > 0 {
		if e = json.Unmarshal(relations, &v.Relations); e != nil {
			return nil, e
		}
	}
	return v, nil
}
func (s *PgStore) CreateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireLiveCustomObject(ctx, scope, v.ObjectType); err != nil {
		return nil, err
	}
	fields, err := CoerceCustomValues(ctx, s, scope, v.ObjectType, v.Fields, nil)
	if err != nil {
		return nil, err
	}
	v.Fields = fields
	for kind, id := range v.Relations {
		if err := requireSubject(ctx, s, scope, "custom record", kind, id); err != nil {
			return nil, err
		}
	}
	document, err := fields.MarshalDocument()
	if err != nil {
		return nil, err
	}
	relations, err := json.Marshal(v.Relations)
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err = s.db.Exec(ctx, scope, `INSERT INTO custom_records (tenant_id,object_type,id,fields,relations,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, scope.TenantID, v.ObjectType, v.ID, document, relations, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return cloneCustomRecord(v), nil
}
func (s *PgStore) GetCustomRecord(ctx context.Context, scope Scope, objectType, id string) (*CustomRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	return scanCustomRecord(s.db.QueryRow(ctx, scope, `SELECT `+customRecordCols+` FROM custom_records WHERE tenant_id=$1 AND object_type=$2 AND id=$3`, scope.TenantID, normalizeObjectType(objectType), id))
}
func (s *PgStore) ListCustomRecords(ctx context.Context, scope Scope, objectType string, page Page) ([]*CustomRecord, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	limit := page.size()
	rows, err := s.db.Query(ctx, scope, `SELECT `+customRecordCols+` FROM custom_records WHERE tenant_id=$1 AND object_type=$2 AND id>$3 ORDER BY id LIMIT $4`, scope.TenantID, normalizeObjectType(objectType), page.Token, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []*CustomRecord{}
	for rows.Next() {
		v, err := scanCustomRecord(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
func (s *PgStore) UpdateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error) {
	current, err := s.GetCustomRecord(ctx, scope, v.ObjectType, v.ID)
	if err != nil {
		return nil, err
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireLiveCustomObject(ctx, scope, v.ObjectType); err != nil {
		return nil, err
	}
	fields, err := CoerceCustomValues(ctx, s, scope, v.ObjectType, v.Fields, current.Fields)
	if err != nil {
		return nil, err
	}
	v.Fields = fields
	for kind, id := range v.Relations {
		if err := requireSubject(ctx, s, scope, "custom record", kind, id); err != nil {
			return nil, err
		}
	}
	document, err := fields.MarshalDocument()
	if err != nil {
		return nil, err
	}
	relations, err := json.Marshal(v.Relations)
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()
	tag, err := s.db.Exec(ctx, scope, `UPDATE custom_records SET fields=$4,relations=$5,updated_at=$6 WHERE tenant_id=$1 AND object_type=$2 AND id=$3`, scope.TenantID, v.ObjectType, v.ID, document, relations, v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	v.CreatedAt = current.CreatedAt
	return cloneCustomRecord(v), nil
}
func (s *PgStore) DeleteCustomRecord(ctx context.Context, scope Scope, objectType, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM custom_records WHERE tenant_id=$1 AND object_type=$2 AND id=$3`, scope.TenantID, normalizeObjectType(objectType), id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
