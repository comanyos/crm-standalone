package domain

import (
	"context"
	"sort"
)

// The in-memory half of account merges (merge_accounts.go).
//
// Every relationship migration below reads a table's full tenant slice with
// .all(scope) and writes back through .update(scope, ...) directly — the
// same direct-table-access style ConvertLead uses (s.lead.update) — rather
// than the exported UpdateAccount/UpdateContact/... methods, which enforce
// optimistic concurrency against a caller-supplied UpdatedAt that a
// mechanical repoint does not have.

// MergeAccounts folds DuplicateID's relationships into SurvivorID inside one
// compound-locked pass. See merge_accounts.go's package doc comment for the
// full rationale.
func (s *MemStore) MergeAccounts(
	ctx context.Context, scope Scope, in MergeAccountsInput,
) (*Account, *AccountMergeRecord, error) {
	if !scope.Valid() {
		return nil, nil, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}

	s.compound.Lock()
	defer s.compound.Unlock()

	survivor, err := s.account.get(scope, in.SurvivorID)
	if err != nil {
		return nil, nil, err
	}
	duplicate, err := s.account.get(scope, in.DuplicateID)
	if err != nil {
		return nil, nil, err
	}

	resolveAccountFields(survivor, duplicate, in.FieldResolutions)

	migrated := map[string]int{}

	contacts, err := s.contact.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range contacts {
		if c.AccountID != duplicate.ID {
			continue
		}
		c.AccountID = survivor.ID
		if _, err := s.contact.update(scope, c); err != nil {
			return nil, nil, err
		}
		migrated["contacts"]++
	}

	opportunities, err := s.opportunity.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range opportunities {
		if o.AccountID != duplicate.ID {
			continue
		}
		o.AccountID = survivor.ID
		if _, err := s.opportunity.update(scope, o); err != nil {
			return nil, nil, err
		}
		migrated["opportunities"]++
	}

	children, err := s.account.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, child := range children {
		changed := false
		if child.ParentID == duplicate.ID && child.ID != survivor.ID {
			child.ParentID = survivor.ID
			migrated["child_accounts"]++
			changed = true
		}
		// Any other account that flagged the duplicate as its own possible
		// match is repointed at the survivor, not cleared — see
		// pgstore_merge_accounts.go's identical statement for why.
		if child.DuplicateOfAccountID == duplicate.ID && child.ID != survivor.ID {
			child.DuplicateOfAccountID = survivor.ID
			migrated["dedup_flags"]++
			changed = true
		}
		if !changed {
			continue
		}
		if _, err := s.account.update(scope, child); err != nil {
			return nil, nil, err
		}
	}

	activities, err := s.activity.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range activities {
		if a.SubjectType != SubjectAccount || a.SubjectID != duplicate.ID {
			continue
		}
		a.SubjectID = survivor.ID
		if _, err := s.activity.update(scope, a); err != nil {
			return nil, nil, err
		}
		migrated["activities"]++
	}

	tasks, err := s.task.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, tsk := range tasks {
		if tsk.SubjectType != SubjectAccount || tsk.SubjectID != duplicate.ID {
			continue
		}
		tsk.SubjectID = survivor.ID
		if _, err := s.task.update(scope, tsk); err != nil {
			return nil, nil, err
		}
		migrated["tasks"]++
	}

	notes, err := s.note.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range notes {
		if n.SubjectType != SubjectAccount || n.SubjectID != duplicate.ID {
			continue
		}
		n.SubjectID = survivor.ID
		if _, err := s.note.update(scope, n); err != nil {
			return nil, nil, err
		}
		migrated["notes"]++
	}

	history, err := s.ownershipHistory.all(scope)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range history {
		if h.AccountID != duplicate.ID {
			continue
		}
		h.AccountID = survivor.ID
		if _, err := s.ownershipHistory.update(scope, h); err != nil {
			return nil, nil, err
		}
		migrated["ownership_history"]++
	}

	migrated["tags"] += s.repointAccountTagAssignments(scope, duplicate.ID, survivor.ID)

	if err := s.account.delete(scope, duplicate.ID); err != nil {
		return nil, nil, err
	}
	updatedSurvivor, err := s.account.update(scope, survivor)
	if err != nil {
		return nil, nil, err
	}

	record := &AccountMergeRecord{
		SurvivorAccountID: survivor.ID, DuplicateAccountID: duplicate.ID,
		MergedBy: in.MergedBy, FieldResolutions: in.FieldResolutions, RelationshipsMigrated: migrated,
		MergedAt: nowStamp(),
	}
	stored, err := s.accountMerge.create(scope, record)
	if err != nil {
		return nil, nil, err
	}
	return updatedSurvivor, stored, nil
}

// repointAccountTagAssignments moves the duplicate's tag assignments to the
// survivor, dropping any the survivor already carries instead of duplicating
// it — the same behaviour AssignTag's own idempotency gives a caller who
// assigns a tag the record already has.
func (s *MemStore) repointAccountTagAssignments(scope Scope, duplicateID, survivorID string) int {
	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	survivorTagIDs := make(map[string]bool)
	for _, a := range s.tags.assignments[scope.TenantID] {
		if a.ObjectType == "account" && a.ObjectID == survivorID {
			survivorTagIDs[a.TagID] = true
		}
	}

	moved := 0
	for id, a := range s.tags.assignments[scope.TenantID] {
		if a.ObjectType != "account" || a.ObjectID != duplicateID {
			continue
		}
		if survivorTagIDs[a.TagID] {
			delete(s.tags.assignments[scope.TenantID], id)
			continue
		}
		a.ObjectID = survivorID
		moved++
	}
	return moved
}

// ListAccountMerges returns every merge recorded for one account, either as
// the kept id or the retired one, newest first.
func (s *MemStore) ListAccountMerges(_ context.Context, scope Scope, accountID string) ([]*AccountMergeRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	all, err := s.accountMerge.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*AccountMergeRecord, 0, 4)
	for _, record := range all {
		if record.SurvivorAccountID == accountID || record.DuplicateAccountID == accountID {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].MergedAt.Equal(out[j].MergedAt) {
			return out[i].MergedAt.After(out[j].MergedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
