package domain

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The PostgreSQL tier for round-robin owner assignment.
//
// The MemStore tests prove the contract. This tier proves the two things only
// a real database can be wrong about: that SELECT ... FOR UPDATE on the
// cursor actually serializes concurrent assignments, and that the whole
// operation is one transaction — the owner change, the ledger row and the
// advance either all happen or none do.

func livePgDistScope(label string) Scope {
	stamp := time.Now().UnixNano()
	return Scope{
		TenantID: fmt.Sprintf("tenant-dist-%s-%d", label, stamp),
		OrgID:    fmt.Sprintf("org-dist-%s-%d", label, stamp),
	}
}

// TestPgRotationMatchesMemStore runs the same rotation against both stores.
// A divergence here would mean lead distribution behaved differently
// depending on which store a deployment happens to have.
func TestPgRotationMatchesMemStore(t *testing.T) {
	pg := livePgStore(t)
	ctx := context.Background()
	members := []string{"rep-a", "rep-b", "rep-c"}

	for name, s := range map[string]Store{"pg": pg, "mem": NewMemStore()} {
		t.Run(name, func(t *testing.T) {
			scope := livePgDistScope(name)
			leads := seedLeads(ctx, t, s, scope, 10)

			counts := map[string]int{}
			for i, leadID := range leads {
				got, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
					ExecutionID: fmt.Sprintf("exec-%d", i), RuleVersionID: "ver-1",
					SubjectType: SubjectLead, SubjectID: leadID, Members: members,
				})
				if err != nil {
					t.Fatalf("assign %d: %v", i, err)
				}
				if got.Position != int64(i) {
					t.Errorf("assignment %d took position %d, want %d", i, got.Position, i)
				}
				counts[got.MemberID]++

				lead, err := s.GetLead(ctx, scope, leadID)
				if err != nil {
					t.Fatalf("read lead back: %v", err)
				}
				if lead.OwnerID != got.MemberID {
					t.Errorf("lead owner = %q but the assignment said %q", lead.OwnerID, got.MemberID)
				}
			}
			if counts["rep-a"] != 4 || counts["rep-b"] != 3 || counts["rep-c"] != 3 {
				t.Errorf("distribution = %v, want 4/3/3", counts)
			}

			// A re-run repeats rather than advancing.
			replay, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
				ExecutionID: "exec-0", RuleVersionID: "ver-1",
				SubjectType: SubjectLead, SubjectID: leads[0], Members: members,
			})
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if !replay.Replayed || replay.Position != 0 || replay.MemberID != "rep-a" {
				t.Errorf("replay = %+v, want the original decision repeated", replay)
			}

			ledger, err := s.ListOwnerAssignments(ctx, scope, "ver-1", 100)
			if err != nil {
				t.Fatalf("list assignments: %v", err)
			}
			if len(ledger) != 10 {
				t.Errorf("ledger has %d rows, want 10 — a replay must not add one", len(ledger))
			}
		})
	}
}

// TestPgConcurrentRotationSerializesOnTheCursor is why the cursor row exists.
// Ten goroutines assign at once through a real connection pool; the FOR
// UPDATE is the only thing stopping two of them reading the same position and
// handing two leads to one member while another gets none.
//
// The unique index on (tenant, rule_version, position) is the backstop: if
// the locking were ever weakened, this fails as a constraint violation rather
// than as a quietly lopsided distribution.
func TestPgConcurrentRotationSerializesOnTheCursor(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := livePgDistScope("concurrent")
	members := []string{"rep-a", "rep-b", "rep-c"}

	const n = 9
	leads := seedLeads(ctx, t, s, scope, n)

	var wg sync.WaitGroup
	results := make([]*OwnerAssignment, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, so they actually contend
			results[i], errs[i] = s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
				ExecutionID: fmt.Sprintf("exec-%d", i), RuleVersionID: "ver-1",
				SubjectType: SubjectLead, SubjectID: leads[i], Members: members,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[int64]bool{}
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent assign %d: %v", i, errs[i])
		}
		if seen[results[i].Position] {
			t.Errorf("position %d handed out twice under real row locking", results[i].Position)
		}
		seen[results[i].Position] = true
		counts[results[i].MemberID]++
	}
	for position := int64(0); position < n; position++ {
		if !seen[position] {
			t.Errorf("position %d never used; a turn was skipped", position)
		}
	}
	for _, member := range members {
		if counts[member] != n/len(members) {
			t.Errorf("%s got %d of %d, want %d — the rotation was not even", member, counts[member], n, n/len(members))
		}
	}
}

// TestPgAssignmentIsAtomic proves the operation is one transaction by making
// it fail at its last possible moment: the subject is gone, so the owner
// write finds no row. Neither the ledger nor the cursor may move.
func TestPgAssignmentIsAtomic(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := livePgDistScope("atomic")
	members := []string{"rep-a", "rep-b"}

	good := seedLeads(ctx, t, s, scope, 1)[0]

	if _, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-ok", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: good, Members: members,
	}); err != nil {
		t.Fatalf("first assign: %v", err)
	}

	// Now one that cannot complete.
	if _, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-doomed", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: "no-such-lead", Members: members,
	}); err == nil {
		t.Fatal("assigning a lead that does not exist succeeded")
	}

	ledger, err := s.ListOwnerAssignments(ctx, scope, "ver-1", 100)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	if len(ledger) != 1 {
		t.Errorf("ledger has %d rows after a failed assignment, want 1", len(ledger))
	}

	// The turn the failed attempt would have taken must still be available.
	// If the cursor had advanced, this next lead would be rep-a again and
	// rep-b would have been skipped entirely.
	next := seedLeads(ctx, t, s, scope, 1)[0]
	got, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-after", RuleVersionID: "ver-1",
		SubjectType: SubjectLead, SubjectID: next, Members: members,
	})
	if err != nil {
		t.Fatalf("assign after failure: %v", err)
	}
	if got.Position != 1 || got.MemberID != "rep-b" {
		t.Errorf("after a failed assignment the next took %q at %d, want rep-b at 1 — "+
			"a failure burned a turn", got.MemberID, got.Position)
	}
}

// TestPgAccountAssignmentRecordsOwnershipHistory checks the audit trail on
// the other assignable type. An automatic reassignment is exactly the kind
// nobody remembers making, so it has to be the kind that is written down.
func TestPgAccountAssignmentRecordsOwnershipHistory(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := livePgDistScope("account")

	account, err := s.CreateAccount(ctx, scope, &Account{Name: "Rotating Holdings", OwnerID: "rep-original"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	got, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-1", RuleVersionID: "ver-1",
		SubjectType: SubjectAccount, SubjectID: account.ID, Members: []string{"rep-a", "rep-b"},
	})
	if err != nil {
		t.Fatalf("assign account: %v", err)
	}
	if got.MemberID != "rep-a" {
		t.Fatalf("assigned %q, want rep-a", got.MemberID)
	}

	after, err := s.GetAccount(ctx, scope, account.ID)
	if err != nil {
		t.Fatalf("read account: %v", err)
	}
	if after.OwnerID != "rep-a" {
		t.Errorf("account owner = %q, want rep-a", after.OwnerID)
	}

	history, err := s.ListOwnershipHistory(ctx, scope, account.ID)
	if err != nil {
		t.Fatalf("list ownership history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(history))
	}
	if history[0].FromOwnerID != "rep-original" || history[0].ToOwnerID != "rep-a" {
		t.Errorf("history entry = %q -> %q, want rep-original -> rep-a",
			history[0].FromOwnerID, history[0].ToOwnerID)
	}

	// Assigning the same person again still consumes the turn: the rotation
	// is about whose turn it was, not about whether the write changed
	// anything. Skipping the advance would hand the next record to them too.
	same, err := s.AssignNextOwner(ctx, scope, AssignNextOwnerInput{
		ExecutionID: "exec-2", RuleVersionID: "ver-2",
		SubjectType: SubjectAccount, SubjectID: account.ID, Members: []string{"rep-a", "rep-b"},
	})
	if err != nil {
		t.Fatalf("re-assign to the same owner: %v", err)
	}
	if same.Position != 0 || same.MemberID != "rep-a" {
		t.Fatalf("unexpected second assignment %+v", same)
	}
	history, err = s.ListOwnershipHistory(ctx, scope, account.ID)
	if err != nil {
		t.Fatalf("list ownership history again: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("history has %d entries after a no-op reassignment, want 1 — "+
			"an owner change that changed nothing is not a change", len(history))
	}
}

func TestPgRotationIsTenantIsolated(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	a, b := livePgDistScope("alpha"), livePgDistScope("beta")

	leadA := seedLeads(ctx, t, s, a, 1)[0]
	leadB := seedLeads(ctx, t, s, b, 1)[0]

	if _, err := s.AssignNextOwner(ctx, a, AssignNextOwnerInput{
		ExecutionID: "exec-shared", RuleVersionID: "ver-shared",
		SubjectType: SubjectLead, SubjectID: leadA, Members: []string{"rep-a", "rep-b"},
	}); err != nil {
		t.Fatalf("alpha assign: %v", err)
	}

	// The same execution id and rule version in another tenant is a different
	// rotation. Treating it as a replay would leak alpha's decision into
	// beta's ledger and leave beta's lead unassigned.
	other, err := s.AssignNextOwner(ctx, b, AssignNextOwnerInput{
		ExecutionID: "exec-shared", RuleVersionID: "ver-shared",
		SubjectType: SubjectLead, SubjectID: leadB, Members: []string{"rep-x", "rep-y"},
	})
	if err != nil {
		t.Fatalf("beta assign: %v", err)
	}
	if other.Replayed || other.MemberID != "rep-x" || other.Position != 0 {
		t.Errorf("beta got %+v, want its own rotation starting at rep-x/0", other)
	}

	rows, err := s.ListOwnerAssignments(ctx, b, "ver-shared", 100)
	if err != nil {
		t.Fatalf("beta list: %v", err)
	}
	if len(rows) != 1 || rows[0].SubjectID != leadB {
		t.Errorf("beta reads %d assignments, want only its own", len(rows))
	}
}
