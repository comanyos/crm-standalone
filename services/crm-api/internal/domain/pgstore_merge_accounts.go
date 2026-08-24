package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of account merges (merge_accounts.go).
//
// The whole operation runs inside one transaction via scopedDB.begin — the
// same seam pgstore_lead_conversion.go uses for ConvertLead — so a failure
// partway through (a duplicate constraint, a connection drop) leaves neither
// account touched rather than half-migrated. Both rows are locked with
// SELECT ... FOR UPDATE for the duration, so two concurrent merges naming
// either account serialize instead of racing.

const accountMergeColumns = `id, survivor_account_id, duplicate_account_id, merged_by,
	field_resolutions, relationships_migrated, merged_at`

func scanAccountMerge(row pgx.Row) (*AccountMergeRecord, error) {
	var (
		v                     AccountMergeRecord
		fieldResolutions      []byte
		relationshipsMigrated []byte
	)
	if err := row.Scan(&v.ID, &v.SurvivorAccountID, &v.DuplicateAccountID, &v.MergedBy,
		&fieldResolutions, &relationshipsMigrated, &v.MergedAt); err != nil {
		return nil, err
	}
	if len(fieldResolutions) > 0 {
		if err := json.Unmarshal(fieldResolutions, &v.FieldResolutions); err != nil {
			return nil, fmt.Errorf("decode merge field resolutions: %w", err)
		}
	}
	if len(relationshipsMigrated) > 0 {
		if err := json.Unmarshal(relationshipsMigrated, &v.RelationshipsMigrated); err != nil {
			return nil, fmt.Errorf("decode merge relationships migrated: %w", err)
		}
	}
	return &v, nil
}

// MergeAccounts folds DuplicateID's relationships into SurvivorID inside one
// transaction. See this file's and merge_accounts.go's doc comments.
func (s *PgStore) MergeAccounts(
	ctx context.Context, scope Scope, in MergeAccountsInput,
) (*Account, *AccountMergeRecord, error) {
	if !scope.Valid() {
		return nil, nil, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}

	tx, err := s.db.begin(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	survivor := &Account{}
	err = scanAccount(tx.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		scope.TenantID, in.SurvivorID), survivor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get survivor account: %w", err)
	}
	duplicate := &Account{}
	err = scanAccount(tx.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		scope.TenantID, in.DuplicateID), duplicate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get duplicate account: %w", err)
	}

	resolveAccountFields(survivor, duplicate, in.FieldResolutions)

	migrated := map[string]int{}
	migrate := func(key, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", key, err)
		}
		if n := int(tag.RowsAffected()); n > 0 {
			migrated[key] = n
		}
		return nil
	}

	if err := migrate("contacts",
		`UPDATE contacts SET account_id = $3 WHERE tenant_id = $1 AND account_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("opportunities",
		`UPDATE opportunities SET account_id = $3 WHERE tenant_id = $1 AND account_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("child_accounts",
		`UPDATE accounts SET parent_id = $3 WHERE tenant_id = $1 AND parent_id = $2 AND id <> $3`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("activities",
		`UPDATE activities SET subject_id = $3 WHERE tenant_id = $1 AND subject_type = 'account' AND subject_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("tasks",
		`UPDATE tasks SET subject_id = $3 WHERE tenant_id = $1 AND subject_type = 'account' AND subject_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("notes",
		`UPDATE notes SET subject_id = $3 WHERE tenant_id = $1 AND subject_type = 'account' AND subject_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}
	if err := migrate("ownership_history",
		`UPDATE account_ownership_history SET account_id = $3 WHERE tenant_id = $1 AND account_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}

	// Tags: drop the duplicate's assignment wherever the survivor already
	// carries the same tag (the same "no-op, not a conflict" idempotency
	// AssignTag itself gives a direct caller), then repoint whatever is left.
	if _, err := tx.Exec(ctx,
		`DELETE FROM tag_assignments
		  WHERE tenant_id = $1 AND object_type = 'account' AND object_id = $2
		    AND tag_id IN (
		        SELECT tag_id FROM tag_assignments
		         WHERE tenant_id = $1 AND object_type = 'account' AND object_id = $3)`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, fmt.Errorf("drop duplicate tag assignments: %w", err)
	}
	if err := migrate("tags",
		`UPDATE tag_assignments SET object_id = $3
		  WHERE tenant_id = $1 AND object_type = 'account' AND object_id = $2`,
		scope.TenantID, duplicate.ID, survivor.ID); err != nil {
		return nil, nil, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM accounts WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, duplicate.ID); err != nil {
		return nil, nil, fmt.Errorf("delete duplicate account: %w", err)
	}

	now := nowStamp()
	updatedSurvivor := &Account{}
	err = scanAccount(tx.QueryRow(ctx,
		`UPDATE accounts SET name = $3, industry = $4, tier = $5, owner_id = $6, website = $7,
		        annual_revenue = $8, parent_id = $9, updated_at = $10, website_domain = $11
		  WHERE tenant_id = $1 AND id = $2
		RETURNING `+accountColumns,
		scope.TenantID, survivor.ID, survivor.Name, survivor.Industry, survivor.Tier, survivor.OwnerID,
		survivor.Website, survivor.AnnualRevenue, survivor.ParentID, now, websiteDomain(survivor.Website)),
		updatedSurvivor)
	if err != nil {
		return nil, nil, fmt.Errorf("update survivor account: %w", err)
	}

	fieldResolutions, err := json.Marshal(in.FieldResolutions)
	if err != nil {
		return nil, nil, err
	}
	relationshipsMigrated, err := json.Marshal(migrated)
	if err != nil {
		return nil, nil, err
	}
	record, err := scanAccountMerge(tx.QueryRow(ctx,
		`INSERT INTO account_merges
		     (tenant_id, id, survivor_account_id, duplicate_account_id, merged_by,
		      field_resolutions, relationships_migrated, merged_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING `+accountMergeColumns,
		scope.TenantID, newID(), survivor.ID, duplicate.ID, in.MergedBy,
		fieldResolutions, relationshipsMigrated, now))
	if err != nil {
		return nil, nil, fmt.Errorf("record account merge: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit account merge: %w", err)
	}
	return updatedSurvivor, record, nil
}

// ListAccountMerges returns every merge recorded for one account, either as
// the kept id or the retired one, newest first.
func (s *PgStore) ListAccountMerges(ctx context.Context, scope Scope, accountID string) ([]*AccountMergeRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+accountMergeColumns+` FROM account_merges
		  WHERE tenant_id = $1 AND (survivor_account_id = $2 OR duplicate_account_id = $2)
		  ORDER BY merged_at DESC, id`,
		scope.TenantID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list account merges: %w", err)
	}
	defer rows.Close()

	out := make([]*AccountMergeRecord, 0, 4)
	for rows.Next() {
		record, err := scanAccountMerge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account merge: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
