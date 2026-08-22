package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Task and Note behaviour (task.md §5.1).
//
// Every test here runs against both stores. MemStore and PgStore implement
// the same rules in completely different ways — Go conditionals against SQL
// and CHECK constraints — and the failure mode of letting them drift is the
// worst kind: the suite is green, and the behaviour changes when the service
// is deployed with a real database.

// storeUnderTest pairs a name with a store, plus the account both need for a
// subject.
type storeUnderTest struct {
	name    string
	store   Store
	scope   Scope
	account string
}

// bothStores returns MemStore and, when a database is reachable, PgStore.
func bothStores(t *testing.T) []storeUnderTest {
	t.Helper()
	var out []storeUnderTest

	stamp := time.Now().UnixNano()
	mem := NewMemStore()
	memScope := Scope{TenantID: fmt.Sprintf("tenant-mem-%d", stamp), OrgID: "org-mem"}
	memAccount, err := mem.CreateAccount(context.Background(), memScope,
		&Account{Name: "Subject Co", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed mem account: %v", err)
	}
	out = append(out, storeUnderTest{"MemStore", mem, memScope, memAccount.ID})

	pg := livePgStore(t)
	if pg != nil {
		pgScope := Scope{TenantID: fmt.Sprintf("tenant-pg-%d", stamp), OrgID: "org-pg"}
		pgAccount, err := pg.CreateAccount(context.Background(), pgScope,
			&Account{Name: "Subject Co", OwnerID: "owner-1"})
		if err != nil {
			t.Fatalf("seed pg account: %v", err)
		}
		out = append(out, storeUnderTest{"PgStore", pg, pgScope, pgAccount.ID})
	}
	return out
}

// eachStore runs fn against every available store.
func eachStore(t *testing.T, fn func(t *testing.T, s storeUnderTest)) {
	t.Helper()
	for _, s := range bothStores(t) {
		t.Run(s.name, func(t *testing.T) { fn(t, s) })
	}
}

func TestTaskCompletionIsStampedAndReversible(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		created, err := s.store.CreateTask(ctx, s.scope, &Task{Title: "Send the quote", CreatedBy: "u1"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if created.Status != TaskOpen {
			t.Errorf("new task status = %q, want open", created.Status)
		}
		if !created.CompletedAt.IsZero() {
			t.Error("a new task already has a completion time")
		}

		done, err := s.store.CompleteTask(ctx, s.scope, created.ID, "u2")
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if done.Status != TaskCompleted {
			t.Errorf("status = %q, want completed", done.Status)
		}
		if done.CompletedAt.IsZero() || done.CompletedBy != "u2" {
			t.Errorf("completion not recorded: at=%v by=%q", done.CompletedAt, done.CompletedBy)
		}

		// Completing again must not restamp: a double-tap or a retried
		// request would otherwise rewrite who finished the work.
		again, err := s.store.CompleteTask(ctx, s.scope, created.ID, "u3")
		if err != nil {
			t.Fatalf("complete twice: %v", err)
		}
		if !again.CompletedAt.Equal(done.CompletedAt) || again.CompletedBy != "u2" {
			t.Errorf("a second completion rewrote the record: at=%v by=%q", again.CompletedAt, again.CompletedBy)
		}

		reopened, err := s.store.ReopenTask(ctx, s.scope, created.ID)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if reopened.Status != TaskOpen {
			t.Errorf("reopened status = %q, want open", reopened.Status)
		}
		// The completion record has to go with it, or the task reports as
		// finished in every "completed this week" query while sitting in
		// somebody's open list.
		if !reopened.CompletedAt.IsZero() || reopened.CompletedBy != "" {
			t.Errorf("reopening left completion metadata: at=%v by=%q",
				reopened.CompletedAt, reopened.CompletedBy)
		}
	})
}

func TestTaskCompletionMetadataIsNotCallerWritable(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		backdated := time.Now().UTC().Add(-30 * 24 * time.Hour)

		// A client that could assert these could backdate work and name
		// somebody else as having done it.
		created, err := s.store.CreateTask(ctx, s.scope, &Task{
			Title: "Claimed", CompletedAt: backdated, CompletedBy: "someone-else",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if !created.CompletedAt.IsZero() || created.CompletedBy != "" {
			t.Errorf("create accepted caller-supplied completion: at=%v by=%q",
				created.CompletedAt, created.CompletedBy)
		}

		created.CompletedAt = backdated
		created.CompletedBy = "someone-else"
		updated, err := s.store.UpdateTask(ctx, s.scope, created)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if !updated.CompletedAt.IsZero() || updated.CompletedBy != "" {
			t.Errorf("update accepted caller-supplied completion: at=%v by=%q",
				updated.CompletedAt, updated.CompletedBy)
		}
	})
}

// TestUpdatingStatusKeepsCompletionConsistent covers the path around
// CompleteTask: a plain edit that changes status must leave the record
// self-consistent, or the two ways of completing a task disagree.
func TestUpdatingStatusKeepsCompletionConsistent(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		created, err := s.store.CreateTask(ctx, s.scope, &Task{Title: "Edit path"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		created.Status = TaskCompleted
		done, err := s.store.UpdateTask(ctx, s.scope, created)
		if err != nil {
			t.Fatalf("update to completed: %v", err)
		}
		if done.CompletedAt.IsZero() {
			t.Error("updating status to completed did not stamp a completion time")
		}

		done.Status = TaskInProgress
		back, err := s.store.UpdateTask(ctx, s.scope, done)
		if err != nil {
			t.Fatalf("update away from completed: %v", err)
		}
		if !back.CompletedAt.IsZero() || back.CompletedBy != "" {
			t.Errorf("a task moved out of completed kept its completion: at=%v by=%q",
				back.CompletedAt, back.CompletedBy)
		}
	})
}

func TestTaskRejectsHalfASubject(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		for name, task := range map[string]*Task{
			"type without id": {Title: "T", SubjectType: SubjectAccount},
			"id without type": {Title: "T", SubjectID: s.account},
		} {
			if _, err := s.store.CreateTask(ctx, s.scope, task); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid — half a subject is a dangling reference", name, err)
			}
		}

		// A whole subject is fine, and so is none at all: "call the vendor
		// back" is a real task that attaches to nothing.
		if _, err := s.store.CreateTask(ctx, s.scope, &Task{
			Title: "Attached", SubjectType: SubjectAccount, SubjectID: s.account,
		}); err != nil {
			t.Errorf("a task with a real subject was rejected: %v", err)
		}
		if _, err := s.store.CreateTask(ctx, s.scope, &Task{Title: "Standalone"}); err != nil {
			t.Errorf("a task with no subject was rejected: %v", err)
		}
	})
}

func TestTaskFiltersAndOrdering(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)

		mk := func(title, status, assignee string, due time.Time) *Task {
			v, err := s.store.CreateTask(ctx, s.scope, &Task{
				Title: title, Status: status, AssigneeID: assignee, DueAt: due,
			})
			if err != nil {
				t.Fatalf("create %s: %v", title, err)
			}
			return v
		}
		overdue := mk("overdue", TaskOpen, "alice", now.Add(-48*time.Hour))
		soon := mk("soon", TaskInProgress, "alice", now.Add(24*time.Hour))
		undated := mk("undated", TaskOpen, "alice", time.Time{})
		_ = mk("someone else's", TaskOpen, "bob", now.Add(-72*time.Hour))
		finished := mk("finished", TaskOpen, "alice", now.Add(-96*time.Hour))
		if _, err := s.store.CompleteTask(ctx, s.scope, finished.ID, "alice"); err != nil {
			t.Fatalf("complete: %v", err)
		}

		ids := func(rows []*Task) []string {
			out := make([]string, 0, len(rows))
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		list := func(f TaskFilter, sortBy TaskSort) []*Task {
			t.Helper()
			rows, _, err := s.store.ListTasks(ctx, s.scope, Page{Size: 100}, f, sortBy)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			return rows
		}

		// open_only spans open and in_progress, and excludes the completed
		// one — which is exactly why it exists separately from status.
		open := ids(list(TaskFilter{AssigneeID: "alice", OpenOnly: true}, SortTaskDue))
		want := []string{overdue.ID, soon.ID, undated.ID}
		if len(open) != len(want) {
			t.Fatalf("alice's open tasks = %v, want %v", open, want)
		}
		for i := range want {
			if open[i] != want[i] {
				t.Fatalf("alice's open tasks = %v, want %v (due ascending, undated last)", open, want)
			}
		}

		// The overdue list: open, and due before now. The undated task must
		// not appear — a task with no due date is never overdue, and treating
		// its zero time as "long ago" would put it at the top.
		od := ids(list(TaskFilter{AssigneeID: "alice", OpenOnly: true, DueBefore: now}, SortTaskDue))
		if len(od) != 1 || od[0] != overdue.ID {
			t.Errorf("overdue = %v, want exactly the overdue task", od)
		}

		// status filters exactly, unlike open_only.
		inProgress := ids(list(TaskFilter{Status: TaskInProgress}, SortTaskDue))
		if len(inProgress) != 1 || inProgress[0] != soon.ID {
			t.Errorf("in_progress = %v, want exactly the in-progress task", inProgress)
		}

		if got := list(TaskFilter{SubjectType: SubjectAccount, SubjectID: s.account}, SortTaskDue); len(got) != 0 {
			t.Errorf("subject filter matched %d unattached tasks", len(got))
		}

		if _, _, err := s.store.ListTasks(ctx, s.scope, Page{}, TaskFilter{Status: "nonsense"}, SortTaskDue); !errors.Is(err, ErrInvalid) {
			t.Errorf("an unknown status filter was accepted: %v", err)
		}
	})
}

// TestTaskPaginationCoversEveryRowExactlyOnce is the property keyset
// pagination exists for, and the one an off-by-one in the cursor predicate
// breaks silently: pages that skip a row, or return one twice.
func TestTaskPaginationCoversEveryRowExactlyOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)

		const total = 9
		created := map[string]bool{}
		for i := range total {
			due := time.Time{}
			// A mix of dated and undated, so the page boundary has to cross
			// the dated/undated split that the ordering rule introduces.
			if i%3 != 0 {
				due = now.Add(time.Duration(i) * time.Hour)
			}
			v, err := s.store.CreateTask(ctx, s.scope, &Task{
				Title: fmt.Sprintf("task-%d", i), AssigneeID: "paginator", DueAt: due,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			created[v.ID] = true
		}

		for _, sortBy := range []TaskSort{SortTaskDue, SortTaskCreatedDesc} {
			t.Run(string("sort="+sortBy), func(t *testing.T) {
				seen := map[string]int{}
				token := ""
				for pages := 0; ; pages++ {
					if pages > total+2 {
						t.Fatal("pagination did not terminate")
					}
					rows, next, err := s.store.ListTasks(ctx, s.scope,
						Page{Size: 2, Token: token}, TaskFilter{AssigneeID: "paginator"}, sortBy)
					if err != nil {
						t.Fatalf("page %d: %v", pages, err)
					}
					for _, r := range rows {
						seen[r.ID]++
					}
					if next == "" {
						break
					}
					token = next
				}
				if len(seen) != total {
					t.Errorf("saw %d distinct tasks across all pages, want %d", len(seen), total)
				}
				for id, n := range seen {
					if n != 1 {
						t.Errorf("task %s appeared on %d pages", id, n)
					}
					if !created[id] {
						t.Errorf("pagination returned an unknown task %s", id)
					}
				}
			})
		}
	})
}

func TestNoteRequiresASubjectThatResolves(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()

		if _, err := s.store.CreateNote(ctx, s.scope, &Note{Body: "orphan"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("a note with no subject was accepted: %v", err)
		}
		if _, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "dangling", SubjectType: SubjectAccount, SubjectID: "no-such-account",
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("a note naming a nonexistent account was accepted: %v", err)
		}
		if _, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "bad type", SubjectType: "invoice", SubjectID: s.account,
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("a note with an unknown subject type was accepted: %v", err)
		}
		if _, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "good", SubjectType: SubjectAccount, SubjectID: s.account,
		}); err != nil {
			t.Errorf("a well-formed note was rejected: %v", err)
		}
	})
}

// TestNoteEditingRecordsThatItWasEdited is the piece of audit worth keeping
// once immutability is given up — and the reason a save that changes nothing
// must not count.
func TestNoteEditingRecordsThatItWasEdited(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		created, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "first draft", SubjectType: SubjectAccount, SubjectID: s.account, AuthorID: "u1",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if !created.EditedAt.IsZero() {
			t.Error("a brand-new note is marked as edited")
		}

		// A client that always sends the whole record back must not make
		// every save look like an edit.
		unchanged, err := s.store.UpdateNote(ctx, s.scope, cloneNote(created))
		if err != nil {
			t.Fatalf("no-op update: %v", err)
		}
		if !unchanged.EditedAt.IsZero() {
			t.Error("a save that changed nothing marked the note as edited")
		}

		unchanged.Body = "second draft"
		edited, err := s.store.UpdateNote(ctx, s.scope, unchanged)
		if err != nil {
			t.Fatalf("real update: %v", err)
		}
		if edited.EditedAt.IsZero() {
			t.Error("a real edit did not record that the note was edited")
		}
		if edited.Body != "second draft" {
			t.Errorf("body = %q, want the edit to have landed", edited.Body)
		}
	})
}

// TestNoteSubjectAndAuthorAreNotEditable pins the two fields a note must not
// be able to move. Reassigning authorship is the one thing an audit trail
// cannot permit — a note is evidence of who said what.
func TestNoteSubjectAndAuthorAreNotEditable(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		other, err := s.store.CreateAccount(ctx, s.scope, &Account{Name: "Elsewhere", OwnerID: "o"})
		if err != nil {
			t.Fatalf("seed second account: %v", err)
		}
		created, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "original", SubjectType: SubjectAccount, SubjectID: s.account, AuthorID: "author-1",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		created.SubjectID = other.ID
		created.AuthorID = "someone-else"
		updated, err := s.store.UpdateNote(ctx, s.scope, created)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.SubjectID != s.account {
			t.Error("a note was moved to a different record, rewriting history others have read")
		}
		if updated.AuthorID != "author-1" {
			t.Errorf("author = %q; authorship must not be reassignable", updated.AuthorID)
		}
	})
}

func TestTaskAndNoteRejectAnEmptyScope(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()
		if _, err := s.store.CreateTask(ctx, Scope{}, &Task{Title: "T"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("CreateTask with no scope: err = %v, want ErrNoTenant", err)
		}
		if _, err := s.store.CreateNote(ctx, Scope{}, &Note{
			Body: "N", SubjectType: SubjectAccount, SubjectID: s.account,
		}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("CreateNote with no scope: err = %v, want ErrNoTenant", err)
		}
		if _, _, err := s.store.ListTasks(ctx, Scope{}, Page{}, TaskFilter{}, SortTaskDue); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListTasks with no scope: err = %v, want ErrNoTenant", err)
		}
		if _, _, err := s.store.ListNotes(ctx, Scope{}, Page{}, NoteFilter{}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListNotes with no scope: err = %v, want ErrNoTenant", err)
		}
	})
}

func TestTaskAndNoteOptimisticConcurrency(t *testing.T) {
	eachStore(t, func(t *testing.T, s storeUnderTest) {
		ctx := context.Background()

		task, err := s.store.CreateTask(ctx, s.scope, &Task{Title: "Contested"})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		stale := cloneTask(task)
		task.Title = "Won"
		if _, err := s.store.UpdateTask(ctx, s.scope, task); err != nil {
			t.Fatalf("first update: %v", err)
		}
		stale.Title = "Lost"
		if _, err := s.store.UpdateTask(ctx, s.scope, stale); !errors.Is(err, ErrConflict) {
			t.Errorf("a stale task update: err = %v, want ErrConflict", err)
		}

		note, err := s.store.CreateNote(ctx, s.scope, &Note{
			Body: "Contested", SubjectType: SubjectAccount, SubjectID: s.account,
		})
		if err != nil {
			t.Fatalf("create note: %v", err)
		}
		staleNote := cloneNote(note)
		note.Body = "Won"
		if _, err := s.store.UpdateNote(ctx, s.scope, note); err != nil {
			t.Fatalf("first note update: %v", err)
		}
		staleNote.Body = "Lost"
		if _, err := s.store.UpdateNote(ctx, s.scope, staleNote); !errors.Is(err, ErrConflict) {
			t.Errorf("a stale note update: err = %v, want ErrConflict", err)
		}
	})
}
