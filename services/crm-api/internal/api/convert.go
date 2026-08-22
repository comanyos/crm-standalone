// Package api adapts the domain layer to the module's RPC surface: proto
// conversion, scope extraction, and error mapping.
package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// scopeFrom derives the store scope from the verified principal. The tenant is never
// read from the request body — a caller must not be able to name someone else's
// tenant.
func scopeFrom(ctx context.Context) (domain.Scope, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return domain.Scope{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("no authenticated principal"))
	}
	if p.TenantID == "" {
		return domain.Scope{}, connect.NewError(connect.CodePermissionDenied,
			errors.New("principal carries no tenant"))
	}
	return domain.Scope{TenantID: p.TenantID, OrgID: p.OrgID}, nil
}

// connectErr maps domain errors onto Connect status codes. Unknown errors stay
// internal so storage details never leak to the caller.
func connectErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalidTransition):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrNoTenant):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

func accountToProto(v *domain.Account) *crmv1.Account {
	if v == nil {
		return nil
	}
	return &crmv1.Account{
		Id:                   v.ID,
		Name:                 v.Name,
		Industry:             v.Industry,
		Tier:                 v.Tier,
		OwnerId:              v.OwnerID,
		Website:              v.Website,
		AnnualRevenue:        v.AnnualRevenue,
		ParentId:             v.ParentID,
		CreatedAt:            timestamppb.New(v.CreatedAt),
		UpdatedAt:            timestamppb.New(v.UpdatedAt),
		DuplicateOfAccountId: v.DuplicateOfAccountID,
	}
}

func accountFromProto(m *crmv1.Account) *domain.Account {
	if m == nil {
		return &domain.Account{}
	}
	return &domain.Account{
		ID:                   m.GetId(),
		Name:                 m.GetName(),
		Industry:             m.GetIndustry(),
		Tier:                 m.GetTier(),
		OwnerID:              m.GetOwnerId(),
		Website:              m.GetWebsite(),
		AnnualRevenue:        m.GetAnnualRevenue(),
		ParentID:             m.GetParentId(),
		DuplicateOfAccountID: m.GetDuplicateOfAccountId(),
		// UpdatedAt round-trips the caller's last-read version for
		// UpdateAccount's optimistic-concurrency check (domain/memstore.go,
		// domain/pgstore.go) — an absent field must produce the true Go zero
		// Time (so the check is skipped for a caller that never sends it),
		// not (*timestamppb.Timestamp)(nil).AsTime()'s Unix epoch, which
		// IsZero() does not consider zero and would fail every such update.
		UpdatedAt: protoTimestamp(m.GetUpdatedAt()),
	}
}

// protoTimestamp converts an optional proto Timestamp to time.Time, mapping
// an absent field to the true Go zero Time rather than
// (*timestamppb.Timestamp)(nil).AsTime()'s Unix epoch — the two are not the
// same value, and callers here (optimistic-concurrency checks) rely on
// time.Time.IsZero() to detect "not sent".
func protoTimestamp(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func contactToProto(v *domain.Contact) *crmv1.Contact {
	if v == nil {
		return nil
	}
	return &crmv1.Contact{
		Id:                   v.ID,
		Email:                v.Email,
		AccountId:            v.AccountID,
		FirstName:            v.FirstName,
		LastName:             v.LastName,
		Phone:                v.Phone,
		Title:                v.Title,
		CreatedAt:            timestamppb.New(v.CreatedAt),
		UpdatedAt:            timestamppb.New(v.UpdatedAt),
		Role:                 v.Role,
		DuplicateOfContactId: v.DuplicateOfContactID,
	}
}

func contactFromProto(m *crmv1.Contact) *domain.Contact {
	if m == nil {
		return &domain.Contact{}
	}
	return &domain.Contact{
		ID:                   m.GetId(),
		Email:                m.GetEmail(),
		AccountID:            m.GetAccountId(),
		FirstName:            m.GetFirstName(),
		LastName:             m.GetLastName(),
		Phone:                m.GetPhone(),
		Title:                m.GetTitle(),
		Role:                 m.GetRole(),
		DuplicateOfContactID: m.GetDuplicateOfContactId(),
		// See accountFromProto's UpdatedAt comment: protoTimestamp, not
		// m.GetUpdatedAt().AsTime(), for a caller that omits the field.
		UpdatedAt: protoTimestamp(m.GetUpdatedAt()),
	}
}

func leadToProto(v *domain.Lead) *crmv1.Lead {
	if v == nil {
		return nil
	}
	return &crmv1.Lead{
		Id:                     v.ID,
		Company:                v.Company,
		Source:                 v.Source,
		ContactEmail:           v.ContactEmail,
		Status:                 v.Status,
		Score:                  v.Score,
		OwnerId:                v.OwnerID,
		CreatedAt:              timestamppb.New(v.CreatedAt),
		UpdatedAt:              timestamppb.New(v.UpdatedAt),
		DuplicateOfContactId:   v.DuplicateOfContactID,
		ConvertedContactId:     v.ConvertedContactID,
		ConvertedAccountId:     v.ConvertedAccountID,
		ConvertedOpportunityId: v.ConvertedOpportunityID,
	}
}

func leadFromProto(m *crmv1.Lead) *domain.Lead {
	if m == nil {
		return &domain.Lead{}
	}
	return &domain.Lead{
		ID:                     m.GetId(),
		Company:                m.GetCompany(),
		Source:                 m.GetSource(),
		ContactEmail:           m.GetContactEmail(),
		Status:                 m.GetStatus(),
		Score:                  m.GetScore(),
		OwnerID:                m.GetOwnerId(),
		DuplicateOfContactID:   m.GetDuplicateOfContactId(),
		ConvertedContactID:     m.GetConvertedContactId(),
		ConvertedAccountID:     m.GetConvertedAccountId(),
		ConvertedOpportunityID: m.GetConvertedOpportunityId(),
	}
}

func opportunityToProto(v *domain.Opportunity) *crmv1.Opportunity {
	if v == nil {
		return nil
	}
	return &crmv1.Opportunity{
		Id:          v.ID,
		Name:        v.Name,
		AccountId:   v.AccountID,
		Stage:       v.Stage,
		PipelineId:  v.PipelineID,
		StageId:     v.StageID,
		Amount:      v.Amount,
		Probability: v.Probability,
		CloseDate:   timestamppb.New(v.CloseDate),
		CloseReason: v.CloseReason,
		CreatedAt:   timestamppb.New(v.CreatedAt),
		UpdatedAt:   timestamppb.New(v.UpdatedAt),
	}
}

func opportunityFromProto(m *crmv1.Opportunity) *domain.Opportunity {
	if m == nil {
		return &domain.Opportunity{}
	}
	return &domain.Opportunity{
		ID:          m.GetId(),
		Name:        m.GetName(),
		AccountID:   m.GetAccountId(),
		Stage:       m.GetStage(),
		PipelineID:  m.GetPipelineId(),
		StageID:     m.GetStageId(),
		Amount:      m.GetAmount(),
		Probability: m.GetProbability(),
		// An absent close_date must remain Go's zero Time. An update that moves
		// a deal into a closed stage deliberately omits this value because the
		// domain stamps it atomically. Timestamp.AsTime on an absent protobuf
		// value represents the Unix epoch, which is non-zero and would be
		// rejected as a client-supplied closing date.
		CloseDate:   protoTimestamp(m.GetCloseDate()),
		CloseReason: m.GetCloseReason(),
	}
}

func opportunityStageChangeToProto(v *domain.OpportunityStageChange) *crmv1.OpportunityStageChange {
	if v == nil {
		return nil
	}
	return &crmv1.OpportunityStageChange{
		Id: v.ID, OpportunityId: v.OpportunityID, FromStage: v.FromStage, ToStage: v.ToStage,
		ChangedAt: timestamppb.New(v.ChangedAt),
	}
}

func pipelineToProto(v *domain.Pipeline) *crmv1.Pipeline {
	if v == nil {
		return nil
	}
	return &crmv1.Pipeline{
		Id: v.ID, Name: v.Name, IsDefault: v.IsDefault,
		ArchivedAt: timestampOrNil(v.ArchivedAt), CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt),
	}
}

func pipelineFromProto(m *crmv1.Pipeline) *domain.Pipeline {
	if m == nil {
		return &domain.Pipeline{}
	}
	return &domain.Pipeline{
		ID: m.GetId(), Name: m.GetName(), IsDefault: m.GetIsDefault(),
		ArchivedAt: protoTimestamp(m.GetArchivedAt()),
	}
}

func pipelineStageToProto(v *domain.PipelineStage) *crmv1.PipelineStage {
	if v == nil {
		return nil
	}
	return &crmv1.PipelineStage{
		Id: v.ID, PipelineId: v.PipelineID, Name: v.Name, Position: int32(v.Position), Probability: v.Probability,
		TerminalType: v.TerminalType, RequiresCloseReason: v.RequiresCloseReason, ArchivedAt: timestampOrNil(v.ArchivedAt),
		CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt),
	}
}

func pipelineStageFromProto(m *crmv1.PipelineStage) *domain.PipelineStage {
	if m == nil {
		return &domain.PipelineStage{}
	}
	return &domain.PipelineStage{
		ID: m.GetId(), PipelineID: m.GetPipelineId(), Name: m.GetName(), Position: int(m.GetPosition()), Probability: m.GetProbability(),
		TerminalType: m.GetTerminalType(), RequiresCloseReason: m.GetRequiresCloseReason(), ArchivedAt: protoTimestamp(m.GetArchivedAt()),
	}
}

func timestampOrNil(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func activityToProto(v *domain.Activity) *crmv1.Activity {
	if v == nil {
		return nil
	}
	return &crmv1.Activity{
		Id:          v.ID,
		Kind:        v.Kind,
		SubjectType: v.SubjectType,
		SubjectId:   v.SubjectID,
		Notes:       v.Notes,
		ActorId:     v.ActorID,
		OccurredAt:  timestamppb.New(v.OccurredAt),
		CreatedAt:   timestamppb.New(v.CreatedAt),
		UpdatedAt:   timestamppb.New(v.UpdatedAt),
	}
}

func activityFromProto(m *crmv1.Activity) *domain.Activity {
	if m == nil {
		return &domain.Activity{}
	}
	return &domain.Activity{
		ID:          m.GetId(),
		Kind:        m.GetKind(),
		SubjectType: m.GetSubjectType(),
		SubjectID:   m.GetSubjectId(),
		Notes:       m.GetNotes(),
		ActorID:     m.GetActorId(),
		OccurredAt:  m.GetOccurredAt().AsTime(),
	}
}

// --- Task and Note (task.md §5.1) ---
//
// nullableTimestamp is used for the fields that are genuinely absent rather
// than zero — a task with no due date, a note never edited. timestamppb.New
// on a zero time.Time produces year one, which a client would render as
// "1 Jan 0001" rather than "no due date"; nil is what the contract means.

func nullableTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func taskToProto(v *domain.Task) *crmv1.Task {
	if v == nil {
		return nil
	}
	return &crmv1.Task{
		Id:          v.ID,
		Title:       v.Title,
		Description: v.Description,
		Status:      v.Status,
		Priority:    v.Priority,
		AssigneeId:  v.AssigneeID,
		DueAt:       nullableTimestamp(v.DueAt),
		SubjectType: v.SubjectType,
		SubjectId:   v.SubjectID,
		CompletedAt: nullableTimestamp(v.CompletedAt),
		CompletedBy: v.CompletedBy,
		CreatedBy:   v.CreatedBy,
		CreatedAt:   timestamppb.New(v.CreatedAt),
		UpdatedAt:   timestamppb.New(v.UpdatedAt),
	}
}

func taskFromProto(m *crmv1.Task) *domain.Task {
	if m == nil {
		return &domain.Task{}
	}
	// CompletedAt and CompletedBy are deliberately not read from the request.
	// They describe something the server observed, and the store overwrites
	// them anyway (sanitizeTaskLifecycle) — accepting them here would only
	// make it look as though a client could set them.
	return &domain.Task{
		ID:          m.GetId(),
		Title:       m.GetTitle(),
		Description: m.GetDescription(),
		Status:      m.GetStatus(),
		Priority:    m.GetPriority(),
		AssigneeID:  m.GetAssigneeId(),
		DueAt:       protoTimestamp(m.GetDueAt()),
		SubjectType: m.GetSubjectType(),
		SubjectID:   m.GetSubjectId(),
		CreatedBy:   m.GetCreatedBy(),
		UpdatedAt:   protoTimestamp(m.GetUpdatedAt()),
	}
}

func noteToProto(v *domain.Note) *crmv1.Note {
	if v == nil {
		return nil
	}
	return &crmv1.Note{
		Id:          v.ID,
		Body:        v.Body,
		SubjectType: v.SubjectType,
		SubjectId:   v.SubjectID,
		AuthorId:    v.AuthorID,
		CreatedAt:   timestamppb.New(v.CreatedAt),
		UpdatedAt:   timestamppb.New(v.UpdatedAt),
		EditedAt:    nullableTimestamp(v.EditedAt),
	}
}

func noteFromProto(m *crmv1.Note) *domain.Note {
	if m == nil {
		return &domain.Note{}
	}
	// EditedAt is the store's to set, the same way CompletedAt is a Task's.
	return &domain.Note{
		ID:          m.GetId(),
		Body:        m.GetBody(),
		SubjectType: m.GetSubjectType(),
		SubjectID:   m.GetSubjectId(),
		AuthorID:    m.GetAuthorId(),
		UpdatedAt:   protoTimestamp(m.GetUpdatedAt()),
	}
}

// principalSubject returns the verified caller's subject, used to default
// authorship fields (Task.created_by, Task.completed_by, Note.author_id).
//
// These default server-side rather than being taken from the request because
// they are attribution: a client that can name the author of a note can put
// words in someone else's mouth, and one that can name who completed a task
// can misreport who did the work. A caller may still pass an explicit value —
// an import or a migration needs to — but the honest default is the person
// actually making the call.
func principalSubject(ctx context.Context) string {
	if p, ok := auth.FromContext(ctx); ok {
		return p.Subject
	}
	return ""
}
