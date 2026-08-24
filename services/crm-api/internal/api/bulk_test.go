package api

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// TestBulkOperationsReportPerItemResults is the core contract every bulk RPC
// must meet: a batch is not a transaction, so one bad id must not fail the
// whole call, and a good id in the same batch must still succeed.
func TestBulkOperationsReportPerItemResults(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-bulk", "org-bulk")

	tag, err := s.CreateTag(ctx, connect.NewRequest(&crmv1.CreateTagRequest{Name: "Hot"}))
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	res, err := s.BulkAssignTag(ctx, connect.NewRequest(&crmv1.BulkAssignTagRequest{
		TagId: tag.Msg.GetTag().GetId(), ObjectType: "account",
		ObjectIds: []string{account.Msg.GetAccount().GetId(), "does-not-exist"},
	}))
	if err != nil {
		t.Fatalf("BulkAssignTag: %v", err)
	}
	results := res.Msg.GetResults()
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].GetSuccess() {
		t.Errorf("valid account: success = false, want true (error %q)", results[0].GetError())
	}
	if results[1].GetSuccess() {
		t.Error("nonexistent account: success = true, want false")
	}
	if results[1].GetError() == "" {
		t.Error("nonexistent account: error is empty, want a reason")
	}

	// The valid id's tag really landed.
	tagsForRecord, err := s.ListTagsForRecord(ctx, connect.NewRequest(&crmv1.ListTagsForRecordRequest{
		ObjectType: "account", ObjectId: account.Msg.GetAccount().GetId(),
	}))
	if err != nil {
		t.Fatalf("ListTagsForRecord: %v", err)
	}
	if len(tagsForRecord.Msg.GetTags()) != 1 {
		t.Fatalf("account has %d tags, want 1", len(tagsForRecord.Msg.GetTags()))
	}
}

// TestBulkAssignAndRemoveTagAreIdempotent mirrors AssignTag/RemoveTag's own
// idempotency contract at the bulk layer: retrying a batch that partially
// succeeded must not error or double-apply.
func TestBulkAssignAndRemoveTagAreIdempotent(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-bulk-idem", "org-bulk-idem")

	tag, err := s.CreateTag(ctx, connect.NewRequest(&crmv1.CreateTagRequest{Name: "Hot"}))
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	req := connect.NewRequest(&crmv1.BulkAssignTagRequest{
		TagId: tag.Msg.GetTag().GetId(), ObjectType: "account",
		ObjectIds: []string{account.Msg.GetAccount().GetId()},
	})
	for i := 0; i < 2; i++ {
		res, err := s.BulkAssignTag(ctx, req)
		if err != nil {
			t.Fatalf("BulkAssignTag attempt %d: %v", i, err)
		}
		if !res.Msg.GetResults()[0].GetSuccess() {
			t.Fatalf("BulkAssignTag attempt %d: not successful", i)
		}
	}
	tagsForRecord, err := s.ListTagsForRecord(ctx, connect.NewRequest(&crmv1.ListTagsForRecordRequest{
		ObjectType: "account", ObjectId: account.Msg.GetAccount().GetId(),
	}))
	if err != nil {
		t.Fatalf("ListTagsForRecord: %v", err)
	}
	if len(tagsForRecord.Msg.GetTags()) != 1 {
		t.Fatalf("account has %d tags after two assigns, want 1 (no duplicate row)", len(tagsForRecord.Msg.GetTags()))
	}

	removeReq := connect.NewRequest(&crmv1.BulkRemoveTagRequest{
		TagId: tag.Msg.GetTag().GetId(), ObjectType: "account",
		ObjectIds: []string{account.Msg.GetAccount().GetId()},
	})
	for i := 0; i < 2; i++ {
		res, err := s.BulkRemoveTag(ctx, removeReq)
		if err != nil {
			t.Fatalf("BulkRemoveTag attempt %d: %v", i, err)
		}
		if !res.Msg.GetResults()[0].GetSuccess() {
			t.Fatalf("BulkRemoveTag attempt %d: not successful", i)
		}
	}
}

// TestBulkTaskLifecycle covers complete/reopen/reassign together, including
// a task belonging to another tenant, which must fail that item without
// touching the batch's other items.
func TestBulkTaskLifecycle(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-bulk-task", "org-bulk-task")
	other := ctxAs("tenant-bulk-task-other", "org-bulk-task-other")

	task, err := s.CreateTask(ctx, connect.NewRequest(&crmv1.CreateTaskRequest{
		Task: &crmv1.Task{Title: "Follow up"},
	}))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	foreignTask, err := s.CreateTask(other, connect.NewRequest(&crmv1.CreateTaskRequest{
		Task: &crmv1.Task{Title: "Someone else's task"},
	}))
	if err != nil {
		t.Fatalf("create foreign task: %v", err)
	}

	complete, err := s.BulkCompleteTasks(ctx, connect.NewRequest(&crmv1.BulkCompleteTasksRequest{
		Ids: []string{task.Msg.GetTask().GetId(), foreignTask.Msg.GetTask().GetId()},
	}))
	if err != nil {
		t.Fatalf("BulkCompleteTasks: %v", err)
	}
	if !complete.Msg.GetResults()[0].GetSuccess() {
		t.Error("own task: completion failed")
	}
	if complete.Msg.GetResults()[1].GetSuccess() {
		t.Error("foreign task: completion succeeded, want failure")
	}

	reopen, err := s.BulkReopenTasks(ctx, connect.NewRequest(&crmv1.BulkReopenTasksRequest{
		Ids: []string{task.Msg.GetTask().GetId()},
	}))
	if err != nil {
		t.Fatalf("BulkReopenTasks: %v", err)
	}
	if !reopen.Msg.GetResults()[0].GetSuccess() {
		t.Error("own task: reopen failed")
	}

	reassign, err := s.BulkReassignTaskAssignee(ctx, connect.NewRequest(&crmv1.BulkReassignTaskAssigneeRequest{
		Ids: []string{task.Msg.GetTask().GetId()}, NewAssigneeId: "new-owner",
	}))
	if err != nil {
		t.Fatalf("BulkReassignTaskAssignee: %v", err)
	}
	if !reassign.Msg.GetResults()[0].GetSuccess() {
		t.Error("own task: reassignment failed")
	}
	got, err := s.GetTask(ctx, connect.NewRequest(&crmv1.GetTaskRequest{Id: task.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Msg.GetTask().GetAssigneeId() != "new-owner" {
		t.Errorf("assignee_id = %q, want %q", got.Msg.GetTask().GetAssigneeId(), "new-owner")
	}
}

// TestBulkReassignOwnerAppliesAndAudits proves BulkReassignAccountOwner both
// changes owner_id and that the existing ownership-history mechanism
// (UpdateAccount, account_hierarchy.go) records it — a bulk reassignment
// gets the same audit trail a single-record one would, for free.
func TestBulkReassignOwnerAppliesAndAudits(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-bulk-owner", "org-bulk-owner")

	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	lead, err := s.CreateLead(ctx, connect.NewRequest(&crmv1.CreateLeadRequest{
		Lead: &crmv1.Lead{Company: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}

	if _, err := s.BulkReassignAccountOwner(ctx, connect.NewRequest(&crmv1.BulkReassignAccountOwnerRequest{
		Ids: []string{account.Msg.GetAccount().GetId()}, NewOwnerId: "owner-2",
	})); err != nil {
		t.Fatalf("BulkReassignAccountOwner: %v", err)
	}
	if _, err := s.BulkReassignLeadOwner(ctx, connect.NewRequest(&crmv1.BulkReassignLeadOwnerRequest{
		Ids: []string{lead.Msg.GetLead().GetId()}, NewOwnerId: "owner-2",
	})); err != nil {
		t.Fatalf("BulkReassignLeadOwner: %v", err)
	}

	gotAccount, err := s.GetAccount(ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: account.Msg.GetAccount().GetId()}))
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if gotAccount.Msg.GetAccount().GetOwnerId() != "owner-2" {
		t.Errorf("account owner_id = %q, want owner-2", gotAccount.Msg.GetAccount().GetOwnerId())
	}
	gotLead, err := s.GetLead(ctx, connect.NewRequest(&crmv1.GetLeadRequest{Id: lead.Msg.GetLead().GetId()}))
	if err != nil {
		t.Fatalf("GetLead: %v", err)
	}
	if gotLead.Msg.GetLead().GetOwnerId() != "owner-2" {
		t.Errorf("lead owner_id = %q, want owner-2", gotLead.Msg.GetLead().GetOwnerId())
	}

	scope := domain.Scope{TenantID: "tenant-bulk-owner", OrgID: "org-bulk-owner"}
	history, err := store.ListOwnershipHistory(ctx, scope, account.Msg.GetAccount().GetId())
	if err != nil {
		t.Fatalf("ListOwnershipHistory: %v", err)
	}
	if len(history) != 1 || history[0].ToOwnerID != "owner-2" {
		t.Fatalf("ownership history = %+v, want one entry to owner-2", history)
	}
}

// TestBulkRequestIsBounded proves a batch larger than the documented limit
// is refused outright rather than looping 100000 times.
func TestBulkRequestIsBounded(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-bulk-bound", "org-bulk-bound")

	ids := make([]string, maxBulkBatch+1)
	for i := range ids {
		ids[i] = "x"
	}
	_, err := s.BulkCompleteTasks(ctx, connect.NewRequest(&crmv1.BulkCompleteTasksRequest{Ids: ids}))
	if err == nil {
		t.Fatal("expected an error for a batch over the limit")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("error %q does not name the limit", err.Error())
	}
}
