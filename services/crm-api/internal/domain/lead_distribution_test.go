package domain

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func distributionScope() Scope { return Scope{TenantID: "tenant-dist", OrgID: "org-dist"} }

// seedLeads creates n leads and returns their ids.
func seedLeads(ctx context.Context, t *testing.T, s Store, scope Scope, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lead, err := s.CreateLead(ctx, scope, &Lead{
			Company: fmt.Sprintf("Prospect %d", i), Source: "inbound",
		})
		if err != nil {
			t.Fatalf("create lead %d: %v", i, err)
		}
		ids = append(ids, lead.ID)
	}
	return ids
}

// TestRotationDistributesEvenly is the acceptance criterion in task_crm.md
// §8: ten leads across three members come out 4/3/3, with every lead assigned
// to somebody and nobody assigned out of turn.
func TestRotationDistributesEvenly(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := distributionScope()
	members := []string{"rep-a", "rep-b", "rep-c"}

	leads := seedLeads(ctx, t, s, scope, 10)
	counts := map[string]int{}
	for i, leadID := range leads {
		assignment, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
			ExecutionID: fmt.Sprintf("exec-%d", i), RuleVersionID: "ver-1",
			SubjectType: SubjectLead, SubjectID: leadID, Members: members,
		})
		if err != nil {
			t.Fatalf("assign %d: %v", i, err)
		}
		counts[assignment.MemberID]++

		// The turn number is the position, monotonically: a ledger that
		// repeated or skipped one could not be read as a rotation.
		if assignment.Position != int64(i) {
			t.Errorf("assignment %d took position %d, want %d", i, assignment.Position, i)
		}

		lead, err := s.GetLead(ctx, scope, leadID)
		if err != nil {
			t.Fatalf("read lead back: %v", err)
		}
		if lead.OwnerID != assignment.MemberID {
			t.Errorf("lead owner = %q but the assignment said %q", lead.OwnerID, assignment.MemberID)
		}
	}

	if counts["rep-a"] != 4 || counts["rep-b"] != 3 || counts["rep-c"] != 3 {
		t.Errorf("distribution = %v, want rep-a 4 and the others 3 each", counts)
	}
}

// TestRotationIsIdempotentPerExecution covers the window the whole
// idempotency record exists for: the engine performs an action, then persists
// actions_completed separately, and a crash in between re-runs the action.
// Consuming a second turn there would skip a member permanently.
func TestRotationIsIdempotentPerExecution(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := distributionScope()
	members := []string{"rep-a", "rep-b", "rep-c"}
	leads := seedLeads(ctx, t, s, scope, 2)

	in := AssignNextOwnerInput{
		ExecutionID: "exec-1", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: leads[0], Members: members,
	}
	first, err := s.AssignNextOwner(ctx, scope, in)
	if err != nil {
		t.Fatalf("first assign: %v", err)
	}
	if first.Replayed {
		t.Error("the first assignment reported itself as a replay")
	}

	again, err := s.AssignNextOwner(ctx, scope, in)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !again.Replayed {
		t.Error("a re-run of the same execution did not report itself as a replay")
	}
	if again.MemberID != first.MemberID || again.Position != first.Position {
		t.Errorf("re-run assigned %q at %d, want the original %q at %d — a re-run must "+
			"repeat its decision, not consume another turn",
			again.MemberID, again.Position, first.MemberID, first.Position)
	}

	// And the rotation is where it was, so the next lead gets the second
	// member rather than the third.
	next, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-2", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: leads[1], Members: members,
	})
	if err != nil {
		t.Fatalf("second assign: %v", err)
	}
	if next.Position != 1 || next.MemberID != "rep-b" {
		t.Errorf("after a replay the next assignment was %q at %d, want rep-b at 1 — "+
			"the replay burned a turn", next.MemberID, next.Position)
	}
}

// TestConcurrentRotationHandsOutEachTurnOnce is the property MemStore's lock
// and PgStore's SELECT ... FOR UPDATE both exist for. Without either, two
// assignments read the same position and one member gets two leads while
// another gets none.
func TestConcurrentRotationHandsOutEachTurnOnce(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := distributionScope()
	members := []string{"rep-a", "rep-b", "rep-c"}

	const n = 9
	leads := seedLeads(ctx, t, s, scope, n)

	var wg sync.WaitGroup
	results := make([]*OwnerAssignment, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
				ExecutionID: fmt.Sprintf("exec-%d", i), RuleVersionID: "ver-1",
				SubjectType: SubjectLead, SubjectID: leads[i], Members: members,
			})
		}(i)
	}
	wg.Wait()

	seen := map[int64]bool{}
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent assign %d: %v", i, errs[i])
		}
		if seen[results[i].Position] {
			t.Errorf("position %d was handed out twice; two leads took the same turn",
				results[i].Position)
		}
		seen[results[i].Position] = true
		counts[results[i].MemberID]++
	}
	for position := int64(0); position < n; position++ {
		if !seen[position] {
			t.Errorf("position %d was never used; a turn was skipped", position)
		}
	}
	for _, member := range members {
		if counts[member] != n/len(members) {
			t.Errorf("%s got %d of %d leads, want %d", member, counts[member], n, n/len(members))
		}
	}
}

// TestRotationRefusesUnassignableSubjects pins the one entity deliberately
// left out. An Opportunity's owner is its Account's, so assigning "the
// opportunity" would reassign the account and every other opportunity on it.
func TestRotationRefusesUnassignableSubjects(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := distributionScope()

	for _, subjectType := range []string{SubjectOpportunity, SubjectContact, "invoice", ""} {
		_, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
			ExecutionID: "exec-x", RuleVersionID: "ver-1",
			SubjectType: subjectType, SubjectID: "id-1", Members: []string{"rep-a"},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("assigning a %q: %v, want ErrInvalid", subjectType, err)
		}
	}

	// A record that is gone is a different failure, and the engine treats it
	// as terminal rather than retrying.
	_, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-y", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: "no-such-lead", Members: []string{"rep-a"},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("assigning a deleted lead: %v, want ErrNotFound", err)
	}
}

// TestRotationIsPerRuleVersion pins why the cursor is keyed on the version
// and not the rule: editing a rule publishes a new version, and a changed
// member list is a new rotation. Carrying the old position across would start
// the new list at an arbitrary point.
func TestRotationIsPerRuleVersion(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := distributionScope()
	leads := seedLeads(ctx, t, s, scope, 3)

	for i, leadID := range leads[:2] {
		if _, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
			ExecutionID: fmt.Sprintf("exec-v1-%d", i), RuleVersionID: "ver-1",
			SubjectType: SubjectLead, SubjectID: leadID, Members: []string{"rep-a", "rep-b"},
		}); err != nil {
			t.Fatalf("assign under version 1: %v", err)
		}
	}

	fresh, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-v2-0", RuleVersionID: "ver-2",
		SubjectType: SubjectLead, SubjectID: leads[2], Members: []string{"rep-c", "rep-d"},
	})
	if err != nil {
		t.Fatalf("assign under version 2: %v", err)
	}
	if fresh.Position != 0 || fresh.MemberID != "rep-c" {
		t.Errorf("a new rule version started at %q/%d, want rep-c at position 0",
			fresh.MemberID, fresh.Position)
	}
}

func TestRotationIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	a := Scope{TenantID: "tenant-a", OrgID: "org-a"}
	b := Scope{TenantID: "tenant-b", OrgID: "org-b"}

	leadA := seedLeads(ctx, t, s, a, 1)[0]
	leadB := seedLeads(ctx, t, s, b, 1)[0]

	if _, err := s.AssignNextOwner(ctx, a, AssignNextOwnerInput{
		ExecutionID: "exec-1", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: leadA, Members: []string{"rep-a", "rep-b"},
	}); err != nil {
		t.Fatalf("tenant a assign: %v", err)
	}

	// The same execution id and rule version in another tenant is a different
	// rotation entirely, and must not be mistaken for a replay of tenant a's.
	other, err := s.AssignNextOwner(ctx, b, AssignNextOwnerInput{
		ExecutionID: "exec-1", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: leadB, Members: []string{"rep-x", "rep-y"},
	})
	if err != nil {
		t.Fatalf("tenant b assign: %v", err)
	}
	if other.Replayed {
		t.Error("tenant b's assignment was treated as a replay of tenant a's")
	}
	if other.MemberID != "rep-x" || other.Position != 0 {
		t.Errorf("tenant b got %q at %d, want its own rotation to start at rep-x/0",
			other.MemberID, other.Position)
	}

	// Neither tenant can read the other's ledger.
	rows, err := s.ListOwnerAssignments(ctx, b, "ver-1", 100)
	if err != nil {
		t.Fatalf("list for tenant b: %v", err)
	}
	if len(rows) != 1 || rows[0].SubjectID != leadB {
		t.Errorf("tenant b sees %d assignments, want only its own", len(rows))
	}
}

// TestAssignOwnerActionValidation pins the authoring rules. A rule that will
// not save is better than one the author believes is working.
func TestAssignOwnerActionValidation(t *testing.T) {
	valid := WorkflowAction{Type: ActionAssignOwner, RotationMembers: []string{"a", "b"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a two-member rotation was refused: %v", err)
	}

	bad := []struct {
		name   string
		action WorkflowAction
	}{
		{"no members", WorkflowAction{Type: ActionAssignOwner}},
		{"blank member", WorkflowAction{Type: ActionAssignOwner, RotationMembers: []string{"a", "  "}}},
		{
			// Not a preference — almost always a mistake, and honouring it
			// would give one member twice the work while looking even.
			"duplicate member",
			WorkflowAction{Type: ActionAssignOwner, RotationMembers: []string{"a", "b", "a"}},
		},
		{
			// The action decides the assignee; accepting one too would give
			// it two answers to the same question.
			"also names an assignee",
			WorkflowAction{Type: ActionAssignOwner, RotationMembers: []string{"a"}, Assignee: AssigneeRecordOwner},
		},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.action.Validate(); err == nil {
				t.Fatal("accepted a rotation that should have been refused")
			}
		})
	}

	// Too many members: the bound exists because a list nobody reviews is the
	// real problem, not the storage.
	over := make([]string, maxRotationMembers+1)
	for i := range over {
		over[i] = fmt.Sprintf("rep-%d", i)
	}
	if err := (&WorkflowAction{Type: ActionAssignOwner, RotationMembers: over}).Validate(); err == nil {
		t.Error("accepted a rotation over the member limit")
	}
}

// TestAssignOwnerIsInTheAuthoringCatalog stops the action existing in the
// engine while no editor can offer it — the catalog is what the authoring UI
// builds itself from.
func TestAssignOwnerIsInTheAuthoringCatalog(t *testing.T) {
	found := false
	for _, actionType := range WorkflowActionTypes() {
		if actionType == ActionAssignOwner {
			found = true
		}
	}
	if !found {
		t.Errorf("assign_owner is missing from WorkflowActionTypes() %v", WorkflowActionTypes())
	}
}

// TestAssignOwnerCannotLoop re-asserts the structural property after adding
// an action. assign_owner writes an owner onto a lead or account; if either
// "changed" event were ever a trigger, a rule could fire on its own output.
func TestAssignOwnerCannotLoop(t *testing.T) {
	if err := AssertTriggersCannotLoop(); err != nil {
		t.Fatalf("adding assign_owner made a trigger reachable from an action: %v", err)
	}
}
