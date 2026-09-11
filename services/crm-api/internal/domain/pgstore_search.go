package domain

import (
	"context"
	"fmt"
)

// SearchRecords uses one bounded UNION query rather than issuing five list
// calls and asking the browser to discard most of their rows. Every branch
// carries tenant_id = $1 and runs through scopedDB, so the SQL predicate and
// RLS independently enforce the caller's tenant boundary.
func (s *PgStore) SearchRecords(ctx context.Context, scope Scope, query string, limit int) ([]SearchRecord, error) {
	query, limit, err := validateSearch(query, limit)
	if err != nil {
		return nil, err
	}
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope, `
WITH matches AS (
  SELECT 'account'::text AS object_type, id, name AS title,
    COALESCE(NULLIF(industry, ''), NULLIF(website, ''), '') AS subtitle
  FROM accounts
  WHERE tenant_id = $1 AND (name ILIKE '%' || $2 || '%' OR website ILIKE '%' || $2 || '%')
  UNION ALL
  SELECT 'contact', id, btrim(first_name || ' ' || last_name), email
  FROM contacts
  WHERE tenant_id = $1 AND (first_name ILIKE '%' || $2 || '%' OR last_name ILIKE '%' || $2 || '%' OR email ILIKE '%' || $2 || '%')
  UNION ALL
  SELECT 'lead', id,
    COALESCE(NULLIF(btrim(first_name || ' ' || last_name), ''), NULLIF(company, ''),
             NULLIF(contact_email, ''), NULLIF(phone, ''), 'Lead'),
    COALESCE(NULLIF(company, ''), NULLIF(contact_email, ''), NULLIF(phone, ''), NULLIF(source, ''), '')
  FROM leads
  WHERE tenant_id = $1 AND (first_name ILIKE '%' || $2 || '%' OR last_name ILIKE '%' || $2 || '%'
    OR company ILIKE '%' || $2 || '%' OR contact_email ILIKE '%' || $2 || '%'
    OR phone ILIKE '%' || $2 || '%' OR source ILIKE '%' || $2 || '%')
  UNION ALL
  SELECT 'opportunity', id, name, stage
  FROM opportunities
  WHERE tenant_id = $1 AND (name ILIKE '%' || $2 || '%' OR stage ILIKE '%' || $2 || '%')
  UNION ALL
  SELECT 'task', id, title, description
  FROM tasks
  WHERE tenant_id = $1 AND (title ILIKE '%' || $2 || '%' OR description ILIKE '%' || $2 || '%')
)
SELECT object_type, id, title, subtitle
FROM matches
ORDER BY CASE WHEN lower(title) LIKE $2 || '%' THEN 0 ELSE 1 END, lower(title), id
LIMIT $3`, scope.TenantID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search records: %w", err)
	}
	defer rows.Close()

	results := make([]SearchRecord, 0, limit)
	for rows.Next() {
		var result SearchRecord
		if err := rows.Scan(&result.ObjectType, &result.ID, &result.Title, &result.Subtitle); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search records: %w", err)
	}
	return results, nil
}
