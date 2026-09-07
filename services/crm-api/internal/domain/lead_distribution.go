package domain

import (
	"fmt"
	"strings"
	"time"
)

// Round-robin owner assignment (task_crm.md §8,
// migrations/0029_lead_distribution.sql).
//
// The whole feature is one store operation, and it is one operation because
// of what has to be true at once: the record's owner changes, the rotation
// advances, and the execution records what it decided. Split across
// transactions, any failure between them either burns a turn on an assignment
// that never happened (a member silently skipped) or reassigns a record
// nobody's turn covered.

// OwnerAssignment is one execution's decision: who got the record, and which
// turn of the rotation it consumed.
type OwnerAssignment struct {
	ExecutionID   string    `json:"execution_id"`
	RuleVersionID string    `json:"rule_version_id"`
	Position      int64     `json:"position"`
	MemberID      string    `json:"member_id"`
	SubjectType   string    `json:"subject_type"`
	SubjectID     string    `json:"subject_id"`
	AssignedAt    time.Time `json:"assigned_at"`
	// Replayed reports that this execution had already assigned, and this is
	// the decision it made the first time rather than a new one. The engine
	// re-runs an action when it crashes between performing it and persisting
	// actions_completed; without this the rotation would advance twice for
	// one lead and skip a member.
	Replayed bool `json:"replayed"`
}

// assignableSubjects are the record types that carry an owner of their own.
//
// An Opportunity is deliberately absent. Its owner is its Account's
// (internal/integration/workflow/actions.go resolves it that way), so
// "assign this opportunity" would reassign the account and, with it, every
// other opportunity hanging off it — a side effect nobody authoring a rule
// about one deal would expect.
var assignableSubjects = map[string]bool{
	SubjectLead:    true,
	SubjectAccount: true,
}

// AssignNextOwnerInput names one assignment to make.
type AssignNextOwnerInput struct {
	ExecutionID   string
	RuleVersionID string
	SubjectType   string
	SubjectID     string
	// Members is the rotation, in order, as the rule version stored it.
	Members []string
}

// Validate rejects an assignment that cannot be made.
func (in AssignNextOwnerInput) Validate() error {
	if strings.TrimSpace(in.ExecutionID) == "" {
		return fmt.Errorf("%w: an assignment needs the execution that decided it", ErrInvalid)
	}
	if strings.TrimSpace(in.RuleVersionID) == "" {
		return fmt.Errorf("%w: an assignment needs a rule version to rotate against", ErrInvalid)
	}
	if !assignableSubjects[in.SubjectType] {
		return fmt.Errorf("%w: %q does not carry an owner that can be assigned "+
			"(assignable: %s)", ErrInvalid, in.SubjectType, strings.Join(SortedAssignableSubjects(), ", "))
	}
	if strings.TrimSpace(in.SubjectID) == "" {
		return fmt.Errorf("%w: an assignment needs a record", ErrInvalid)
	}
	if len(in.Members) == 0 {
		return fmt.Errorf("%w: an assignment needs a rotation", ErrInvalid)
	}
	for _, member := range in.Members {
		if strings.TrimSpace(member) == "" {
			return fmt.Errorf("%w: a rotation member cannot be blank", ErrInvalid)
		}
	}
	return nil
}

// SortedAssignableSubjects returns the assignable record types, sorted.
func SortedAssignableSubjects() []string {
	return []string{SubjectAccount, SubjectLead}
}

// memberAt picks the member whose turn `position` is.
//
// The modulo is what makes the cursor monotonic rather than wrapping: the
// stored position only ever counts up, so the ledger reads as a sequence of
// turns and a shortened member list does not renumber history.
func memberAt(members []string, position int64) string {
	if len(members) == 0 {
		return ""
	}
	idx := position % int64(len(members))
	if idx < 0 {
		idx += int64(len(members))
	}
	return strings.TrimSpace(members[idx])
}
