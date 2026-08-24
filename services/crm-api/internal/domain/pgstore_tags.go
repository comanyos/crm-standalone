package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The PostgreSQL half of tags (tags.go, migrations/0024_tags.sql).

const tagDefinitionColumns = `id, name, color, created_by, created_at, updated_at`

func scanTagDefinition(row pgx.Row) (*TagDefinition, error) {
	var v TagDefinition
	if err := row.Scan(&v.ID, &v.Name, &v.Color, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// tagUniqueViolation reports whether err is the PostgreSQL unique_violation
// (23505) tag_definitions_name_key raises on a case-insensitive name
// collision.
func tagUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// CreateTagDefinition stores a new tag. Names are unique per tenant,
// case-insensitively.
func (s *PgStore) CreateTagDefinition(ctx context.Context, scope Scope, v *TagDefinition) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagDefinition(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()

	out, err := scanTagDefinition(s.db.QueryRow(ctx, scope,
		`INSERT INTO tag_definitions (tenant_id, id, name, color, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$6)
		 RETURNING `+tagDefinitionColumns,
		scope.TenantID, record.ID, record.Name, record.Color, record.CreatedBy, now))
	if err != nil {
		if tagUniqueViolation(err) {
			return nil, fmt.Errorf("%w: a tag named %q already exists", ErrConflict, record.Name)
		}
		return nil, fmt.Errorf("create tag: %w", err)
	}
	return out, nil
}

// GetTagDefinition reads one tag.
func (s *PgStore) GetTagDefinition(ctx context.Context, scope Scope, id string) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanTagDefinition(s.db.QueryRow(ctx, scope,
		`SELECT `+tagDefinitionColumns+` FROM tag_definitions WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get tag: %w", err)
	}
	return out, nil
}

// ListTagDefinitions returns the tenant's tags, ordered by name.
func (s *PgStore) ListTagDefinitions(ctx context.Context, scope Scope) ([]*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+tagDefinitionColumns+` FROM tag_definitions
		  WHERE tenant_id = $1 ORDER BY name, id`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	out := make([]*TagDefinition, 0, 8)
	for rows.Next() {
		record, err := scanTagDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// UpdateTagDefinition renames or recolors a tag.
func (s *PgStore) UpdateTagDefinition(ctx context.Context, scope Scope, v *TagDefinition) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagDefinition(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	out, err := scanTagDefinition(s.db.QueryRow(ctx, scope,
		`UPDATE tag_definitions SET name = $3, color = $4, updated_at = $5
		  WHERE tenant_id = $1 AND id = $2
		RETURNING `+tagDefinitionColumns,
		scope.TenantID, record.ID, record.Name, record.Color, nowStamp()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if tagUniqueViolation(err) {
			return nil, fmt.Errorf("%w: a tag named %q already exists", ErrConflict, record.Name)
		}
		return nil, fmt.Errorf("update tag: %w", err)
	}
	return out, nil
}

// DeleteTagDefinition removes a tag. migrations/0024_tags.sql's
// ON DELETE CASCADE removes its assignments in the same statement.
func (s *PgStore) DeleteTagDefinition(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM tag_definitions WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const tagAssignmentColumns = `id, tag_id, object_type, object_id, created_by, created_at`

func scanTagAssignment(row pgx.Row) (*TagAssignment, error) {
	var v TagAssignment
	if err := row.Scan(&v.ID, &v.TagID, &v.ObjectType, &v.ObjectID, &v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// AssignTag attaches a tag to a record. Idempotent: ON CONFLICT DO NOTHING
// against tag_assignments_unique means a repeat call returns the assignment
// that already existed rather than an error, so a bulk "add tag" action can
// retry safely without needing to check first.
func (s *PgStore) AssignTag(ctx context.Context, scope Scope, v *TagAssignment) (*TagAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagAssignment(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	// The tag must exist in this tenant; without the check, a foreign key
	// violation on tag_id would surface as an opaque internal error instead
	// of the same ErrInvalid MemStore returns for the same mistake.
	if _, err := s.GetTagDefinition(ctx, scope, record.TagID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: tag %q does not exist in this tenant", ErrInvalid, record.TagID)
		}
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()

	out, err := scanTagAssignment(s.db.QueryRow(ctx, scope,
		`INSERT INTO tag_assignments (tenant_id, id, tag_id, object_type, object_id, created_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (tenant_id, tag_id, object_type, object_id) DO NOTHING
		 RETURNING `+tagAssignmentColumns,
		scope.TenantID, record.ID, record.TagID, record.ObjectType, record.ObjectID, record.CreatedBy, now))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already assigned. Fetch and return the existing row so the
			// caller gets the same idempotent success MemStore gives.
			existing, getErr := scanTagAssignment(s.db.QueryRow(ctx, scope,
				`SELECT `+tagAssignmentColumns+` FROM tag_assignments
				  WHERE tenant_id = $1 AND tag_id = $2 AND object_type = $3 AND object_id = $4`,
				scope.TenantID, record.TagID, record.ObjectType, record.ObjectID))
			if getErr != nil {
				return nil, fmt.Errorf("assign tag: %w", getErr)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("assign tag: %w", err)
	}
	return out, nil
}

// RemoveTag detaches a tag from a record. Idempotent: a DELETE that matches
// nothing is a success, not a fault.
func (s *PgStore) RemoveTag(ctx context.Context, scope Scope, objectType, objectID, tagID string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if _, err := s.db.Exec(ctx, scope,
		`DELETE FROM tag_assignments
		  WHERE tenant_id = $1 AND tag_id = $2 AND object_type = $3 AND object_id = $4`,
		scope.TenantID, tagID, objectType, objectID); err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}
	return nil
}

// ListTagsForRecord returns the tags attached to one record, ordered by name.
func (s *PgStore) ListTagsForRecord(ctx context.Context, scope Scope, objectType, objectID string) ([]*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+prefixed(tagDefinitionColumns, "d")+`
		   FROM tag_definitions d
		   JOIN tag_assignments a
		     ON a.tenant_id = d.tenant_id AND a.tag_id = d.id
		  WHERE d.tenant_id = $1 AND a.object_type = $2 AND a.object_id = $3
		  ORDER BY d.name, d.id`,
		scope.TenantID, objectType, objectID)
	if err != nil {
		return nil, fmt.Errorf("list tags for record: %w", err)
	}
	defer rows.Close()

	out := make([]*TagDefinition, 0, 4)
	for rows.Next() {
		record, err := scanTagDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// ListTaggedRecords returns one tag's assignments, optionally narrowed to one
// object type, ordered by id — the same simple id-keyset pagination
// ListLeads/ListOpportunities use for an unfiltered list.
func (s *PgStore) ListTaggedRecords(
	ctx context.Context, scope Scope, tagID, objectType string, page Page,
) ([]*TagAssignment, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if tagID == "" {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+tagAssignmentColumns+` FROM tag_assignments
		  WHERE tenant_id = $1 AND tag_id = $2 AND id > $3
		    AND ($4::text = '' OR object_type = $4)
		  ORDER BY id LIMIT $5`,
		scope.TenantID, tagID, page.Token, objectType, page.size()+1)
	if err != nil {
		return nil, "", fmt.Errorf("list tagged records: %w", err)
	}
	defer rows.Close()

	out := make([]*TagAssignment, 0, page.size())
	for rows.Next() {
		record, err := scanTagAssignment(rows)
		if err != nil {
			return nil, "", fmt.Errorf("scan tag assignment: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list tagged records: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
