package domain

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// The in-memory half of round-robin owner assignment
// (lead_distribution.go, migrations/0029_lead_distribution.sql).

type assignmentTables struct {
	// mu serializes the whole assign operation, which is what PgStore gets
	// from SELECT ... FOR UPDATE on the cursor row. Without it two concurrent
	// assignments read the same position and hand two records to one member —
	// the same class of bug MemStore's `compound` lock exists for, and the
	// reason store parity is tested rather than assumed.
	mu sync.Mutex
	// cursors[tenant][ruleVersionID] = next position.
	cursors map[string]map[string]int64
	// assignments[tenant][executionID] = what that execution decided.
	assignments map[string]map[string]*OwnerAssignment
}

func newAssignmentTables() *assignmentTables {
	return &assignmentTables{
		cursors:     make(map[string]map[string]int64),
		assignments: make(map[string]map[string]*OwnerAssignment),
	}
}

func cloneOwnerAssignment(v *OwnerAssignment) *OwnerAssignment {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// AssignNextOwner hands one record to the next member of a rotation. See
// PgStore's implementation for why the three writes are one operation.
func (s *MemStore) AssignNextOwner(
	ctx context.Context, scope Scope, in AssignNextOwnerInput,
) (*OwnerAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}

	s.assignment.mu.Lock()
	defer s.assignment.mu.Unlock()

	// A re-run repeats its own earlier decision rather than consuming another
	// turn, which is what stops an engine crash between the action and its
	// bookkeeping from silently skipping a member.
	if prior, ok := s.assignment.assignments[scope.TenantID][in.ExecutionID]; ok {
		replay := cloneOwnerAssignment(prior)
		replay.Replayed = true
		return replay, nil
	}

	position := s.assignment.cursors[scope.TenantID][in.RuleVersionID]
	member := memberAt(in.Members, position)
	if member == "" {
		return nil, fmt.Errorf("%w: the rotation named no member for position %d", ErrInvalid, position)
	}

	if err := s.assignOwner(scope, in.SubjectType, in.SubjectID, member); err != nil {
		return nil, err
	}

	assignment := &OwnerAssignment{
		ExecutionID: in.ExecutionID, RuleVersionID: in.RuleVersionID,
		Position: position, MemberID: member,
		SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		AssignedAt: nowStamp(),
	}
	if s.assignment.assignments[scope.TenantID] == nil {
		s.assignment.assignments[scope.TenantID] = make(map[string]*OwnerAssignment)
	}
	s.assignment.assignments[scope.TenantID][in.ExecutionID] = cloneOwnerAssignment(assignment)

	if s.assignment.cursors[scope.TenantID] == nil {
		s.assignment.cursors[scope.TenantID] = make(map[string]int64)
	}
	s.assignment.cursors[scope.TenantID][in.RuleVersionID] = position + 1

	return cloneOwnerAssignment(assignment), nil
}

// assignOwner writes the new owner onto the record.
//
// It goes through the memTable directly rather than UpdateLead/UpdateAccount
// for the same reason PgStore's assignOwnerTx uses a targeted UPDATE: those
// compare updated_at for optimistic concurrency, and a record edited between
// the event and the execution would fail the assignment over a field the
// assignment does not touch.
func (s *MemStore) assignOwner(scope Scope, subjectType, subjectID, member string) error {
	switch subjectType {
	case SubjectLead:
		lead, err := s.lead.get(scope, subjectID)
		if err != nil {
			return err
		}
		lead.OwnerID = member
		// memTable.update, not UpdateLead: the Store method compares
		// updated_at for optimistic concurrency, so a lead edited between the
		// event and the execution would fail the assignment over a field the
		// assignment does not touch. PgStore's targeted UPDATE has the same
		// property for the same reason.
		_, err = s.lead.update(scope, lead)
		return err
	case SubjectAccount:
		account, err := s.account.get(scope, subjectID)
		if err != nil {
			return err
		}
		if account.OwnerID == member {
			// Already theirs; the turn is still consumed. See PgStore.
			return nil
		}
		previous := account.OwnerID
		account.OwnerID = member
		if _, err := s.account.update(scope, account); err != nil {
			return err
		}
		_, err = s.ownershipHistory.create(scope, &AccountOwnershipHistory{
			AccountID: subjectID, FromOwnerID: previous, ToOwnerID: member, ChangedAt: nowStamp(),
		})
		return err
	default:
		return fmt.Errorf("%w: %q does not carry an assignable owner", ErrInvalid, subjectType)
	}
}

// ListOwnerAssignments returns what one rule version has assigned, newest
// first.
func (s *MemStore) ListOwnerAssignments(
	_ context.Context, scope Scope, ruleVersionID string, limit int,
) ([]*OwnerAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	s.assignment.mu.Lock()
	defer s.assignment.mu.Unlock()

	out := make([]*OwnerAssignment, 0, 8)
	for _, assignment := range s.assignment.assignments[scope.TenantID] {
		if assignment.RuleVersionID == ruleVersionID {
			out = append(out, cloneOwnerAssignment(assignment))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AssignedAt.Equal(out[j].AssignedAt) {
			return out[i].AssignedAt.After(out[j].AssignedAt)
		}
		return out[i].Position > out[j].Position
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
