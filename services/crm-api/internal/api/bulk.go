package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Bulk operations (M4 — data productivity).
//
// None of these are transactions: a batch is a bounded loop over the same
// per-record store methods a single-record RPC would call, and it reports
// one BulkItemResult per id rather than succeeding or failing as a unit.
// Tag assign/remove and task complete/reopen are naturally idempotent, so a
// caller may retry a partial batch — resending an id that already succeeded
// costs nothing. Owner/assignee reassignment is not idempotent in the same
// sense (each item re-reads and re-validates under optimistic concurrency),
// so a retry either succeeds again harmlessly or reports the same outcome.

// maxBulkBatch bounds every bulk RPC. It exists so a batch cannot become an
// unbounded loop over the whole tenant — large operations belong to the
// import/export surface (task.md Phase 7.1), which is built for that shape
// of work (background jobs, progress, partial retry), not a synchronous RPC.
const maxBulkBatch = 200

// tooManyBulkItems is returned once, at the top of a handler, rather than as
// a per-item result: a batch that is simply too large is a request-shape
// error, not 201 individual failures.
func tooManyBulkItems() error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New("a bulk request may name at most 200 ids"))
}

// bulkErrorMessage renders an error the way BulkItemResult.error may safely
// carry it back to a caller: a domain sentinel's own bounded text, or a
// generic fallback for anything else — the same posture connectErr takes for
// a single-record RPC, so a bulk failure never leaks more than a direct call
// to the same operation would.
func bulkErrorMessage(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "not found"
	case errors.Is(err, domain.ErrConflict):
		return "changed by someone else; retry"
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrInvalidTransition):
		return err.Error()
	default:
		return "internal error"
	}
}

func bulkOK(id string) *crmv1.BulkItemResult { return &crmv1.BulkItemResult{Id: id, Success: true} }
func bulkFail(id string, err error) *crmv1.BulkItemResult {
	return &crmv1.BulkItemResult{Id: id, Success: false, Error: bulkErrorMessage(err)}
}

// BulkAssignTag assigns one tag to every named record.
func (s *CRMService) BulkAssignTag(
	ctx context.Context, req *connect.Request[crmv1.BulkAssignTagRequest],
) (*connect.Response[crmv1.BulkAssignTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetObjectIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		if _, err := s.store.AssignTag(ctx, scope, &domain.TagAssignment{
			TagID: req.Msg.GetTagId(), ObjectType: req.Msg.GetObjectType(), ObjectID: id,
			CreatedBy: principalSubject(ctx),
		}); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkAssignTagResponse{Results: results}), nil
}

// BulkRemoveTag removes one tag from every named record.
func (s *CRMService) BulkRemoveTag(
	ctx context.Context, req *connect.Request[crmv1.BulkRemoveTagRequest],
) (*connect.Response[crmv1.BulkRemoveTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetObjectIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		if err := s.store.RemoveTag(ctx, scope, req.Msg.GetObjectType(), id, req.Msg.GetTagId()); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkRemoveTagResponse{Results: results}), nil
}

// BulkCompleteTasks completes every named task. CompleteTask is itself
// idempotent, so a task already completed reports success rather than an
// error.
func (s *CRMService) BulkCompleteTasks(
	ctx context.Context, req *connect.Request[crmv1.BulkCompleteTasksRequest],
) (*connect.Response[crmv1.BulkCompleteTasksResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	actor := principalSubject(ctx)
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		if _, err := s.store.CompleteTask(ctx, scope, id, actor); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkCompleteTasksResponse{Results: results}), nil
}

// BulkReopenTasks reopens every named task.
func (s *CRMService) BulkReopenTasks(
	ctx context.Context, req *connect.Request[crmv1.BulkReopenTasksRequest],
) (*connect.Response[crmv1.BulkReopenTasksResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		if _, err := s.store.ReopenTask(ctx, scope, id); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkReopenTasksResponse{Results: results}), nil
}

// BulkReassignTaskAssignee reassigns every named task to a new assignee.
//
// Each task is re-read immediately before the write and the write carries
// that read's UpdatedAt, so UpdateTask's optimistic-concurrency check is
// live per item: a task edited by someone else between the batch starting
// and reaching that item reports a conflict rather than silently
// overwriting the concurrent change.
func (s *CRMService) BulkReassignTaskAssignee(
	ctx context.Context, req *connect.Request[crmv1.BulkReassignTaskAssigneeRequest],
) (*connect.Response[crmv1.BulkReassignTaskAssigneeResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		task, err := s.store.GetTask(ctx, scope, id)
		if err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		task.AssigneeID = req.Msg.GetNewAssigneeId()
		if _, err := s.store.UpdateTask(ctx, scope, task); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkReassignTaskAssigneeResponse{Results: results}), nil
}

// BulkReassignAccountOwner reassigns every named account to a new owner.
//
// UpdateAccount already appends an AccountOwnershipHistory row whenever a
// write actually changes owner_id (account_hierarchy.go), so this bulk path
// gets the same durable "who owned this, and when" audit trail a
// single-record reassignment would, with no extra work here.
func (s *CRMService) BulkReassignAccountOwner(
	ctx context.Context, req *connect.Request[crmv1.BulkReassignAccountOwnerRequest],
) (*connect.Response[crmv1.BulkReassignAccountOwnerResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		account, err := s.store.GetAccount(ctx, scope, id)
		if err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		account.OwnerID = req.Msg.GetNewOwnerId()
		if _, err := s.store.UpdateAccount(ctx, scope, account); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkReassignAccountOwnerResponse{Results: results}), nil
}

// BulkReassignLeadOwner reassigns every named lead to a new owner.
//
// Unlike accounts, leads have no ownership-history table today, so this
// bulk path carries the same audit gap a single-record UpdateLead owner
// change already has — it is not a regression this bulk op introduces, but
// it is a real limitation worth naming: "who owned this lead, and when" is
// not answerable for leads the way it is for accounts.
func (s *CRMService) BulkReassignLeadOwner(
	ctx context.Context, req *connect.Request[crmv1.BulkReassignLeadOwnerRequest],
) (*connect.Response[crmv1.BulkReassignLeadOwnerResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.Msg.GetIds()
	if len(ids) > maxBulkBatch {
		return nil, tooManyBulkItems()
	}
	results := make([]*crmv1.BulkItemResult, 0, len(ids))
	for _, id := range ids {
		lead, err := s.store.GetLead(ctx, scope, id)
		if err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		lead.OwnerID = req.Msg.GetNewOwnerId()
		if _, err := s.store.UpdateLead(ctx, scope, lead); err != nil {
			results = append(results, bulkFail(id, err))
			continue
		}
		results = append(results, bulkOK(id))
	}
	return connect.NewResponse(&crmv1.BulkReassignLeadOwnerResponse{Results: results}), nil
}
