package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of round-robin owner assignment
// (lead_distribution.go, migrations/0029_lead_distribution.sql).

const ownerAssignmentColumns = `execution_id, rule_version_id, position, member_id, subject_type, subject_id, assigned_at`

func scanOwnerAssignment(row pgx.Row) (*OwnerAssignment, error) {
	var v OwnerAssignment
	if err := row.Scan(&v.ExecutionID, &v.RuleVersionID, &v.Position,
		&v.MemberID, &v.SubjectType, &v.SubjectID, &v.AssignedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// AssignNextOwner hands one record to the next member of a rotation.
//
// Everything happens in one transaction, and the order matters:
//
//  1. The execution's own earlier decision is looked up first. Finding one
//     means this is a re-run — the engine crashed between performing the
//     action and persisting actions_completed — so it repeats that decision
//     rather than consuming another turn. Without this a crash would skip a
//     member, quietly and permanently.
//  2. The cursor row is locked FOR UPDATE. This is what makes ten leads
//     arriving at once distribute evenly instead of several of them being
//     handed to whoever read the cursor first.
//  3. The owner is written, the assignment is recorded, and the cursor
//     advances — together, so a failure anywhere leaves the rotation exactly
//     where it was.
func (s *PgStore) AssignNextOwner(
	ctx context.Context, scope Scope, in AssignNextOwnerInput,
) (*OwnerAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}

	tx, err := s.db.begin(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Has this execution already decided?
	existing, err := scanOwnerAssignment(tx.QueryRow(ctx,
		`SELECT `+ownerAssignmentColumns+` FROM workflow_owner_assignments
		  WHERE tenant_id = $1 AND execution_id = $2`,
		scope.TenantID, in.ExecutionID))
	switch {
	case err == nil:
		existing.Replayed = true
		return existing, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("read prior assignment: %w", err)
	}

	// 2. Claim this rule version's turn. The INSERT creates the cursor on
	// first use; the SELECT ... FOR UPDATE that follows is what serializes
	// concurrent assignments, so it must run even when the insert did
	// nothing.
	if _, err := tx.Exec(ctx,
		`INSERT INTO workflow_assignment_cursors (tenant_id, rule_version_id, next_position)
		 VALUES ($1,$2,0) ON CONFLICT DO NOTHING`,
		scope.TenantID, in.RuleVersionID); err != nil {
		return nil, fmt.Errorf("create assignment cursor: %w", err)
	}
	var position int64
	if err := tx.QueryRow(ctx,
		`SELECT next_position FROM workflow_assignment_cursors
		  WHERE tenant_id = $1 AND rule_version_id = $2 FOR UPDATE`,
		scope.TenantID, in.RuleVersionID).Scan(&position); err != nil {
		return nil, fmt.Errorf("claim assignment cursor: %w", err)
	}

	member := memberAt(in.Members, position)
	if member == "" {
		return nil, fmt.Errorf("%w: the rotation named no member for position %d", ErrInvalid, position)
	}

	// 3. The owner change, the record of it, and the advance.
	if err := assignOwnerTx(ctx, tx, scope, in.SubjectType, in.SubjectID, member); err != nil {
		return nil, err
	}

	assignment, err := scanOwnerAssignment(tx.QueryRow(ctx,
		`INSERT INTO workflow_owner_assignments
		   (tenant_id, execution_id, rule_version_id, position, member_id, subject_type, subject_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 RETURNING `+ownerAssignmentColumns,
		scope.TenantID, in.ExecutionID, in.RuleVersionID, position, member, in.SubjectType, in.SubjectID))
	if err != nil {
		return nil, fmt.Errorf("record assignment: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE workflow_assignment_cursors
		    SET next_position = $3, updated_at = now()
		  WHERE tenant_id = $1 AND rule_version_id = $2`,
		scope.TenantID, in.RuleVersionID, position+1); err != nil {
		return nil, fmt.Errorf("advance assignment cursor: %w", err)
	}

	return assignment, tx.Commit(ctx)
}

// assignOwnerTx writes the new owner inside the caller's transaction.
//
// It updates the owner column directly rather than going through
// UpdateLead/UpdateAccount, for two reasons that both matter here. Those take
// a whole record and compare updated_at for optimistic concurrency, so a lead
// edited between the event and the execution would fail the assignment rather
// than assign it — and the assignment is not in conflict with that edit, it
// concerns a different field. And they open their own transactions, which
// from inside this one would mean taking a second pool connection while
// holding the first: with as many concurrent assignments as the pool has
// connections, every one waits for another. That deadlock is not
// hypothetical — it is the same one fixed in ConvertLead (docs/ARCHITECTURE.md,
// "Write concurrency").
//
// The account path still appends ownership history, because that history is
// what makes a reassignment auditable and an automatic one is exactly the
// kind nobody remembers making.
func assignOwnerTx(
	ctx context.Context, tx pgx.Tx, scope Scope, subjectType, subjectID, member string,
) error {
	switch subjectType {
	case SubjectLead:
		tag, err := tx.Exec(ctx,
			`UPDATE leads SET owner_id = $3, updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			scope.TenantID, subjectID, member)
		if err != nil {
			return fmt.Errorf("assign lead owner: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	case SubjectAccount:
		var previous string
		err := tx.QueryRow(ctx,
			`SELECT owner_id FROM accounts WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			scope.TenantID, subjectID).Scan(&previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read account owner: %w", err)
		}
		if previous == member {
			// Already theirs. The turn is still consumed: the rotation is
			// about whose turn it was, not about whether the write changed
			// anything, and skipping the advance here would hand the next
			// record to the same person again.
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE accounts SET owner_id = $3, updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			scope.TenantID, subjectID, member); err != nil {
			return fmt.Errorf("assign account owner: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO account_ownership_history
			   (tenant_id, id, account_id, from_owner_id, to_owner_id, changed_at)
			 VALUES ($1,$2,$3,$4,$5,now())`,
			scope.TenantID, newID(), subjectID, previous, member); err != nil {
			return fmt.Errorf("record ownership change: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q does not carry an assignable owner", ErrInvalid, subjectType)
	}
}

// ListOwnerAssignments returns what one rule version has assigned, newest
// first — the read a manager checking fairness makes.
func (s *PgStore) ListOwnerAssignments(
	ctx context.Context, scope Scope, ruleVersionID string, limit int,
) ([]*OwnerAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+ownerAssignmentColumns+` FROM workflow_owner_assignments
		  WHERE tenant_id = $1 AND rule_version_id = $2
		  ORDER BY assigned_at DESC, position DESC
		  LIMIT $3`,
		scope.TenantID, ruleVersionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list owner assignments: %w", err)
	}
	defer rows.Close()

	out := make([]*OwnerAssignment, 0, 8)
	for rows.Next() {
		v, err := scanOwnerAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("list owner assignments: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list owner assignments: %w", err)
	}
	return out, nil
}
