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
		CustomFields:         customValuesToProto(v.CustomFields),
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
		CustomFields:         customValuesFromProto(m.GetCustomFields()),
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

// customValuesToProto transports dynamic values without using Struct: a Struct
// turns every number into a float and would silently round exact decimals.
// Definitions remain the authority for the meaning of a string (text, date,
// decimal, or lookup); the oneof preserves the remaining JSON-native shapes.
func customValuesToProto(values domain.CustomValues) map[string]*crmv1.CustomValue {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]*crmv1.CustomValue, len(values))
	for key, value := range values {
		switch v := value.(type) {
		case string:
			out[key] = &crmv1.CustomValue{Value: &crmv1.CustomValue_Text{Text: v}}
		case float64:
			out[key] = &crmv1.CustomValue{Value: &crmv1.CustomValue_Number{Number: v}}
		case bool:
			out[key] = &crmv1.CustomValue{Value: &crmv1.CustomValue_Boolean{Boolean: v}}
		case []string:
			out[key] = &crmv1.CustomValue{Value: &crmv1.CustomValue_TextList{TextList: &crmv1.StringList{Values: append([]string(nil), v...)}}}
		}
	}
	return out
}

// customValuesFromProto flattens a typed custom value into the JSON-native
// representation validated by domain.CoerceCustomValues. The domain layer
// looks up the tenant's definition and therefore decides whether a string is
// text, an exact decimal, a date, a timestamp, or a lookup.
func customValuesFromProto(values map[string]*crmv1.CustomValue) domain.CustomValues {
	if len(values) == 0 {
		return nil
	}
	out := make(domain.CustomValues, len(values))
	for key, value := range values {
		if value == nil {
			out[key] = nil
			continue
		}
		switch v := value.GetValue().(type) {
		case *crmv1.CustomValue_Text:
			out[key] = v.Text
		case *crmv1.CustomValue_Number:
			out[key] = v.Number
		case *crmv1.CustomValue_Decimal:
			out[key] = v.Decimal
		case *crmv1.CustomValue_Boolean:
			out[key] = v.Boolean
		case *crmv1.CustomValue_Timestamp:
			if v.Timestamp == nil {
				out[key] = nil
			} else {
				out[key] = v.Timestamp.AsTime().UTC().Format(time.RFC3339Nano)
			}
		case *crmv1.CustomValue_Date:
			out[key] = v.Date
		case *crmv1.CustomValue_Reference:
			out[key] = v.Reference
		case *crmv1.CustomValue_TextList:
			if v.TextList == nil {
				out[key] = nil
			} else {
				out[key] = append([]string(nil), v.TextList.Values...)
			}
		default:
			// An unset oneof is a deliberate clear. CoerceCustomValues applies
			// requiredness and rejects the clear where that is not allowed.
			out[key] = nil
		}
	}
	return out
}

func customFieldDefinitionToProto(v *domain.CustomFieldDefinition) *crmv1.CustomFieldDefinition {
	if v == nil {
		return nil
	}
	return &crmv1.CustomFieldDefinition{
		Id: v.ID, ObjectType: v.ObjectType, FieldKey: v.FieldKey, Label: v.Label,
		DataType: v.DataType, Required: v.Required, ArchivedAt: timestampOrNil(v.ArchivedAt),
		CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt),
		Config: &crmv1.CustomFieldConfig{
			Options: append([]string(nil), v.Config.Options...), LookupObjectType: v.Config.LookupObjectType,
			Scale: int32(v.Config.Scale), Expression: v.Config.Expression,
			VisibleToRoles: append([]string(nil), v.Config.VisibleToRoles...), EditableToRoles: append([]string(nil), v.Config.EditableToRoles...), ValidationExpression: v.Config.ValidationExpression,
		},
	}
}

func customFieldDefinitionFromProto(m *crmv1.CustomFieldDefinition) *domain.CustomFieldDefinition {
	if m == nil {
		return &domain.CustomFieldDefinition{}
	}
	c := m.GetConfig()
	return &domain.CustomFieldDefinition{
		ID: m.GetId(), ObjectType: m.GetObjectType(), FieldKey: m.GetFieldKey(), Label: m.GetLabel(),
		DataType: m.GetDataType(), Required: m.GetRequired(),
		Config: domain.FieldConfig{Options: append([]string(nil), c.GetOptions()...), LookupObjectType: c.GetLookupObjectType(), Scale: int(c.GetScale()), Expression: c.GetExpression(), VisibleToRoles: append([]string(nil), c.GetVisibleToRoles()...), EditableToRoles: append([]string(nil), c.GetEditableToRoles()...), ValidationExpression: c.GetValidationExpression()},
	}
}

func customObjectDefinitionToProto(v *domain.CustomObjectDefinition) *crmv1.CustomObjectDefinition {
	if v == nil {
		return nil
	}
	return &crmv1.CustomObjectDefinition{Id: v.ID, ApiName: v.APIName, Label: v.Label, PluralLabel: v.PluralLabel, ArchivedAt: timestampOrNil(v.ArchivedAt), CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
}
func customObjectDefinitionFromProto(v *crmv1.CustomObjectDefinition) *domain.CustomObjectDefinition {
	if v == nil {
		return &domain.CustomObjectDefinition{}
	}
	return &domain.CustomObjectDefinition{ID: v.GetId(), APIName: v.GetApiName(), Label: v.GetLabel(), PluralLabel: v.GetPluralLabel()}
}
func customRecordToProto(v *domain.CustomRecord) *crmv1.CustomRecord {
	if v == nil {
		return nil
	}
	relations := make(map[string]string, len(v.Relations))
	for k, value := range v.Relations {
		relations[k] = value
	}
	return &crmv1.CustomRecord{Id: v.ID, ObjectType: v.ObjectType, Fields: customValuesToProto(v.Fields), Relations: relations, CreatedAt: timestamppb.New(v.CreatedAt), UpdatedAt: timestamppb.New(v.UpdatedAt)}
}
func customRecordFromProto(v *crmv1.CustomRecord) *domain.CustomRecord {
	if v == nil {
		return &domain.CustomRecord{}
	}
	relations := make(map[string]string, len(v.GetRelations()))
	for k, value := range v.GetRelations() {
		relations[k] = value
	}
	return &domain.CustomRecord{ID: v.GetId(), ObjectType: v.GetObjectType(), Fields: customValuesFromProto(v.GetFields()), Relations: relations}
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
		CustomFields:         customValuesToProto(v.CustomFields),
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
		CustomFields:         customValuesFromProto(m.GetCustomFields()),
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
		CustomFields:           customValuesToProto(v.CustomFields),
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
		CustomFields:           customValuesFromProto(m.GetCustomFields()),
	}
}

func opportunityToProto(v *domain.Opportunity) *crmv1.Opportunity {
	if v == nil {
		return nil
	}
	return &crmv1.Opportunity{
		Id:           v.ID,
		Name:         v.Name,
		AccountId:    v.AccountID,
		Stage:        v.Stage,
		PipelineId:   v.PipelineID,
		StageId:      v.StageID,
		Amount:       v.Amount,
		Probability:  v.Probability,
		CloseDate:    timestamppb.New(v.CloseDate),
		CloseReason:  v.CloseReason,
		CreatedAt:    timestamppb.New(v.CreatedAt),
		UpdatedAt:    timestamppb.New(v.UpdatedAt),
		CustomFields: customValuesToProto(v.CustomFields),
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
		CloseDate:    protoTimestamp(m.GetCloseDate()),
		CloseReason:  m.GetCloseReason(),
		CustomFields: customValuesFromProto(m.GetCustomFields()),
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
		Id:           v.ID,
		Kind:         v.Kind,
		SubjectType:  v.SubjectType,
		SubjectId:    v.SubjectID,
		Notes:        v.Notes,
		ActorId:      v.ActorID,
		OccurredAt:   timestamppb.New(v.OccurredAt),
		CreatedAt:    timestamppb.New(v.CreatedAt),
		UpdatedAt:    timestamppb.New(v.UpdatedAt),
		CustomFields: customValuesToProto(v.CustomFields),
	}
}

func activityFromProto(m *crmv1.Activity) *domain.Activity {
	if m == nil {
		return &domain.Activity{}
	}
	return &domain.Activity{
		ID:           m.GetId(),
		Kind:         m.GetKind(),
		SubjectType:  m.GetSubjectType(),
		SubjectID:    m.GetSubjectId(),
		Notes:        m.GetNotes(),
		ActorID:      m.GetActorId(),
		OccurredAt:   m.GetOccurredAt().AsTime(),
		CustomFields: customValuesFromProto(m.GetCustomFields()),
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
		Id:           v.ID,
		Title:        v.Title,
		Description:  v.Description,
		Status:       v.Status,
		Priority:     v.Priority,
		AssigneeId:   v.AssigneeID,
		DueAt:        nullableTimestamp(v.DueAt),
		SubjectType:  v.SubjectType,
		SubjectId:    v.SubjectID,
		CompletedAt:  nullableTimestamp(v.CompletedAt),
		CompletedBy:  v.CompletedBy,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    timestamppb.New(v.CreatedAt),
		UpdatedAt:    timestamppb.New(v.UpdatedAt),
		CustomFields: customValuesToProto(v.CustomFields),
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
		ID:           m.GetId(),
		Title:        m.GetTitle(),
		Description:  m.GetDescription(),
		Status:       m.GetStatus(),
		Priority:     m.GetPriority(),
		AssigneeID:   m.GetAssigneeId(),
		DueAt:        protoTimestamp(m.GetDueAt()),
		SubjectType:  m.GetSubjectType(),
		SubjectID:    m.GetSubjectId(),
		CreatedBy:    m.GetCreatedBy(),
		UpdatedAt:    protoTimestamp(m.GetUpdatedAt()),
		CustomFields: customValuesFromProto(m.GetCustomFields()),
	}
}

func noteToProto(v *domain.Note) *crmv1.Note {
	if v == nil {
		return nil
	}
	return &crmv1.Note{
		Id:           v.ID,
		Body:         v.Body,
		SubjectType:  v.SubjectType,
		SubjectId:    v.SubjectID,
		AuthorId:     v.AuthorID,
		CreatedAt:    timestamppb.New(v.CreatedAt),
		UpdatedAt:    timestamppb.New(v.UpdatedAt),
		EditedAt:     nullableTimestamp(v.EditedAt),
		CustomFields: customValuesToProto(v.CustomFields),
	}
}

func noteFromProto(m *crmv1.Note) *domain.Note {
	if m == nil {
		return &domain.Note{}
	}
	// EditedAt is the store's to set, the same way CompletedAt is a Task's.
	return &domain.Note{
		ID:           m.GetId(),
		Body:         m.GetBody(),
		SubjectType:  m.GetSubjectType(),
		SubjectID:    m.GetSubjectId(),
		AuthorID:     m.GetAuthorId(),
		UpdatedAt:    protoTimestamp(m.GetUpdatedAt()),
		CustomFields: customValuesFromProto(m.GetCustomFields()),
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
