package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

// CRMService implements the module's domain API over a tenant-scoped Store.
type CRMService struct {
	cfg   config.Config
	store domain.Store
}

// NewCRMService wires the domain service.
func NewCRMService(cfg config.Config, store domain.Store) *CRMService {
	return &CRMService{cfg: cfg, store: store}
}

// CreateAccount stores a new Account.
func (s *CRMService) CreateAccount(
	ctx context.Context, req *connect.Request[crmv1.CreateAccountRequest],
) (*connect.Response[crmv1.CreateAccountResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateAccount(ctx, scope, accountFromProto(req.Msg.GetAccount()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateAccountResponse{Account: accountToProto(out)}), nil
}

// GetAccount returns one Account.
func (s *CRMService) GetAccount(
	ctx context.Context, req *connect.Request[crmv1.GetAccountRequest],
) (*connect.Response[crmv1.GetAccountResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetAccount(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetAccountResponse{Account: accountToProto(out)}), nil
}

// ListAccounts returns one page of Accounts. sort is validated against
// domain's allowlist here, at the RPC boundary, rather than passed through
// as a free-form string.
func (s *CRMService) ListAccounts(
	ctx context.Context, req *connect.Request[crmv1.ListAccountsRequest],
) (*connect.Response[crmv1.ListAccountsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	sortBy, err := domain.ParseAccountSort(req.Msg.GetSort())
	if err != nil {
		return nil, connectErr(err)
	}
	rows, next, err := s.store.ListAccounts(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	}, domain.AccountFilter{
		NameContains: req.Msg.GetNameContains(),
		Tier:         req.Msg.GetTier(),
		OwnerID:      req.Msg.GetOwnerId(),
		ParentID:     req.Msg.GetParentId(),
	}, sortBy)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, accountToProto(r))
	}
	return connect.NewResponse(&crmv1.ListAccountsResponse{
		Accounts:      out,
		NextPageToken: next,
	}), nil
}

// UpdateAccount replaces an existing Account.
func (s *CRMService) UpdateAccount(
	ctx context.Context, req *connect.Request[crmv1.UpdateAccountRequest],
) (*connect.Response[crmv1.UpdateAccountResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateAccount(ctx, scope, accountFromProto(req.Msg.GetAccount()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateAccountResponse{Account: accountToProto(out)}), nil
}

// DeleteAccount removes an Account.
func (s *CRMService) DeleteAccount(
	ctx context.Context, req *connect.Request[crmv1.DeleteAccountRequest],
) (*connect.Response[crmv1.DeleteAccountResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteAccount(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteAccountResponse{}), nil
}

// CreateContact stores a new Contact.
func (s *CRMService) CreateContact(
	ctx context.Context, req *connect.Request[crmv1.CreateContactRequest],
) (*connect.Response[crmv1.CreateContactResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateContact(ctx, scope, contactFromProto(req.Msg.GetContact()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateContactResponse{Contact: contactToProto(out)}), nil
}

// GetContact returns one Contact.
func (s *CRMService) GetContact(
	ctx context.Context, req *connect.Request[crmv1.GetContactRequest],
) (*connect.Response[crmv1.GetContactResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetContact(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetContactResponse{Contact: contactToProto(out)}), nil
}

// ListContacts returns one page of Contacts. sort is validated against
// domain's allowlist here, at the RPC boundary.
func (s *CRMService) ListContacts(
	ctx context.Context, req *connect.Request[crmv1.ListContactsRequest],
) (*connect.Response[crmv1.ListContactsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	sortBy, err := domain.ParseContactSort(req.Msg.GetSort())
	if err != nil {
		return nil, connectErr(err)
	}
	rows, next, err := s.store.ListContacts(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	}, domain.ContactFilter{
		AccountID:     req.Msg.GetAccountId(),
		NameContains:  req.Msg.GetNameContains(),
		EmailContains: req.Msg.GetEmailContains(),
	}, sortBy)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Contact, 0, len(rows))
	for _, r := range rows {
		out = append(out, contactToProto(r))
	}
	return connect.NewResponse(&crmv1.ListContactsResponse{
		Contacts:      out,
		NextPageToken: next,
	}), nil
}

// UpdateContact replaces an existing Contact.
func (s *CRMService) UpdateContact(
	ctx context.Context, req *connect.Request[crmv1.UpdateContactRequest],
) (*connect.Response[crmv1.UpdateContactResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateContact(ctx, scope, contactFromProto(req.Msg.GetContact()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateContactResponse{Contact: contactToProto(out)}), nil
}

// DeleteContact removes an Contact.
func (s *CRMService) DeleteContact(
	ctx context.Context, req *connect.Request[crmv1.DeleteContactRequest],
) (*connect.Response[crmv1.DeleteContactResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteContact(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteContactResponse{}), nil
}

// CreateLead stores a new Lead.
func (s *CRMService) CreateLead(
	ctx context.Context, req *connect.Request[crmv1.CreateLeadRequest],
) (*connect.Response[crmv1.CreateLeadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateLead(ctx, scope, leadFromProto(req.Msg.GetLead()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateLeadResponse{Lead: leadToProto(out)}), nil
}

// GetLead returns one Lead.
func (s *CRMService) GetLead(
	ctx context.Context, req *connect.Request[crmv1.GetLeadRequest],
) (*connect.Response[crmv1.GetLeadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetLead(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetLeadResponse{Lead: leadToProto(out)}), nil
}

// ListLeads returns one page of Leads.
func (s *CRMService) ListLeads(
	ctx context.Context, req *connect.Request[crmv1.ListLeadsRequest],
) (*connect.Response[crmv1.ListLeadsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.store.ListLeads(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Lead, 0, len(rows))
	for _, r := range rows {
		out = append(out, leadToProto(r))
	}
	return connect.NewResponse(&crmv1.ListLeadsResponse{
		Leads:         out,
		NextPageToken: next,
	}), nil
}

// UpdateLead replaces an existing Lead.
func (s *CRMService) UpdateLead(
	ctx context.Context, req *connect.Request[crmv1.UpdateLeadRequest],
) (*connect.Response[crmv1.UpdateLeadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateLead(ctx, scope, leadFromProto(req.Msg.GetLead()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateLeadResponse{Lead: leadToProto(out)}), nil
}

// DeleteLead removes an Lead.
func (s *CRMService) DeleteLead(
	ctx context.Context, req *connect.Request[crmv1.DeleteLeadRequest],
) (*connect.Response[crmv1.DeleteLeadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteLead(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteLeadResponse{}), nil
}

// ConvertLead performs the B1 conversion: in one transaction it creates (or
// reuses) a Contact, attaches it to an Account, optionally creates an
// Opportunity, and marks the Lead converted. Retrying with the same lead id
// is a no-op that returns the ids the first successful call produced.
func (s *CRMService) ConvertLead(
	ctx context.Context, req *connect.Request[crmv1.ConvertLeadRequest],
) (*connect.Response[crmv1.ConvertLeadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.ConvertLead(ctx, scope, domain.ConvertLeadInput{
		LeadID:            req.Msg.GetLeadId(),
		CreateOpportunity: req.Msg.GetCreateOpportunity(),
		OpportunityName:   req.Msg.GetOpportunityName(),
		OpportunityAmount: req.Msg.GetOpportunityAmount(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ConvertLeadResponse{
		Lead:        leadToProto(out.Lead),
		Contact:     contactToProto(out.Contact),
		Account:     accountToProto(out.Account),
		Opportunity: opportunityToProto(out.Opportunity),
	}), nil
}

// CreateOpportunity stores a new Opportunity.
func (s *CRMService) CreateOpportunity(
	ctx context.Context, req *connect.Request[crmv1.CreateOpportunityRequest],
) (*connect.Response[crmv1.CreateOpportunityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateOpportunity(ctx, scope, opportunityFromProto(req.Msg.GetOpportunity()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateOpportunityResponse{Opportunity: opportunityToProto(out)}), nil
}

// GetOpportunity returns one Opportunity.
func (s *CRMService) GetOpportunity(
	ctx context.Context, req *connect.Request[crmv1.GetOpportunityRequest],
) (*connect.Response[crmv1.GetOpportunityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetOpportunity(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetOpportunityResponse{Opportunity: opportunityToProto(out)}), nil
}

// ListOpportunities returns one page of Opportunities.
func (s *CRMService) ListOpportunities(
	ctx context.Context, req *connect.Request[crmv1.ListOpportunitiesRequest],
) (*connect.Response[crmv1.ListOpportunitiesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.store.ListOpportunities(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Opportunity, 0, len(rows))
	for _, r := range rows {
		out = append(out, opportunityToProto(r))
	}
	return connect.NewResponse(&crmv1.ListOpportunitiesResponse{
		Opportunities: out,
		NextPageToken: next,
	}), nil
}

// UpdateOpportunity replaces an existing Opportunity.
func (s *CRMService) UpdateOpportunity(
	ctx context.Context, req *connect.Request[crmv1.UpdateOpportunityRequest],
) (*connect.Response[crmv1.UpdateOpportunityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateOpportunity(ctx, scope, opportunityFromProto(req.Msg.GetOpportunity()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateOpportunityResponse{Opportunity: opportunityToProto(out)}), nil
}

// DeleteOpportunity removes an Opportunity.
func (s *CRMService) DeleteOpportunity(
	ctx context.Context, req *connect.Request[crmv1.DeleteOpportunityRequest],
) (*connect.Response[crmv1.DeleteOpportunityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteOpportunity(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteOpportunityResponse{}), nil
}

// ListOpportunityStageHistory returns the tenant-scoped, append-only movement
// history for one opportunity, oldest first.
func (s *CRMService) ListOpportunityStageHistory(
	ctx context.Context, req *connect.Request[crmv1.ListOpportunityStageHistoryRequest],
) (*connect.Response[crmv1.ListOpportunityStageHistoryResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListStageHistory(ctx, scope, req.Msg.GetOpportunityId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.OpportunityStageChange, 0, len(rows))
	for _, row := range rows {
		out = append(out, opportunityStageChangeToProto(row))
	}
	return connect.NewResponse(&crmv1.ListOpportunityStageHistoryResponse{Changes: out}), nil
}

func (s *CRMService) CreatePipeline(ctx context.Context, req *connect.Request[crmv1.CreatePipelineRequest]) (*connect.Response[crmv1.CreatePipelineResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreatePipeline(ctx, scope, pipelineFromProto(req.Msg.GetPipeline()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreatePipelineResponse{Pipeline: pipelineToProto(out)}), nil
}

func (s *CRMService) GetPipeline(ctx context.Context, req *connect.Request[crmv1.GetPipelineRequest]) (*connect.Response[crmv1.GetPipelineResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetPipeline(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetPipelineResponse{Pipeline: pipelineToProto(out)}), nil
}

func (s *CRMService) ListPipelines(ctx context.Context, _ *connect.Request[crmv1.ListPipelinesRequest]) (*connect.Response[crmv1.ListPipelinesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListPipelines(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Pipeline, 0, len(rows))
	for _, row := range rows {
		out = append(out, pipelineToProto(row))
	}
	return connect.NewResponse(&crmv1.ListPipelinesResponse{Pipelines: out}), nil
}

func (s *CRMService) UpdatePipeline(ctx context.Context, req *connect.Request[crmv1.UpdatePipelineRequest]) (*connect.Response[crmv1.UpdatePipelineResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdatePipeline(ctx, scope, pipelineFromProto(req.Msg.GetPipeline()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdatePipelineResponse{Pipeline: pipelineToProto(out)}), nil
}

func (s *CRMService) CreatePipelineStage(ctx context.Context, req *connect.Request[crmv1.CreatePipelineStageRequest]) (*connect.Response[crmv1.CreatePipelineStageResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreatePipelineStage(ctx, scope, pipelineStageFromProto(req.Msg.GetStage()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreatePipelineStageResponse{Stage: pipelineStageToProto(out)}), nil
}

func (s *CRMService) ListPipelineStages(ctx context.Context, req *connect.Request[crmv1.ListPipelineStagesRequest]) (*connect.Response[crmv1.ListPipelineStagesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListPipelineStages(ctx, scope, req.Msg.GetPipelineId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.PipelineStage, 0, len(rows))
	for _, row := range rows {
		out = append(out, pipelineStageToProto(row))
	}
	return connect.NewResponse(&crmv1.ListPipelineStagesResponse{Stages: out}), nil
}

func (s *CRMService) UpdatePipelineStage(ctx context.Context, req *connect.Request[crmv1.UpdatePipelineStageRequest]) (*connect.Response[crmv1.UpdatePipelineStageResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdatePipelineStage(ctx, scope, pipelineStageFromProto(req.Msg.GetStage()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdatePipelineStageResponse{Stage: pipelineStageToProto(out)}), nil
}

// CreateActivity stores a new Activity.
func (s *CRMService) CreateActivity(
	ctx context.Context, req *connect.Request[crmv1.CreateActivityRequest],
) (*connect.Response[crmv1.CreateActivityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateActivity(ctx, scope, activityFromProto(req.Msg.GetActivity()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateActivityResponse{Activity: activityToProto(out)}), nil
}

// GetActivity returns one Activity.
func (s *CRMService) GetActivity(
	ctx context.Context, req *connect.Request[crmv1.GetActivityRequest],
) (*connect.Response[crmv1.GetActivityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetActivity(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetActivityResponse{Activity: activityToProto(out)}), nil
}

// ListActivities returns one page of Activities.
func (s *CRMService) ListActivities(
	ctx context.Context, req *connect.Request[crmv1.ListActivitiesRequest],
) (*connect.Response[crmv1.ListActivitiesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.store.ListActivities(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Activity, 0, len(rows))
	for _, r := range rows {
		out = append(out, activityToProto(r))
	}
	return connect.NewResponse(&crmv1.ListActivitiesResponse{
		Activities:    out,
		NextPageToken: next,
	}), nil
}

// UpdateActivity replaces an existing Activity.
func (s *CRMService) UpdateActivity(
	ctx context.Context, req *connect.Request[crmv1.UpdateActivityRequest],
) (*connect.Response[crmv1.UpdateActivityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateActivity(ctx, scope, activityFromProto(req.Msg.GetActivity()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateActivityResponse{Activity: activityToProto(out)}), nil
}

// DeleteActivity removes an Activity.
func (s *CRMService) DeleteActivity(
	ctx context.Context, req *connect.Request[crmv1.DeleteActivityRequest],
) (*connect.Response[crmv1.DeleteActivityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteActivity(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteActivityResponse{}), nil
}

// ListActivitiesForSubject returns one polymorphic subject's timeline,
// newest first by occurred_at (B5 — docs/customer-os/PLAN.md).
func (s *CRMService) ListActivitiesForSubject(
	ctx context.Context, req *connect.Request[crmv1.ListActivitiesForSubjectRequest],
) (*connect.Response[crmv1.ListActivitiesForSubjectResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListActivitiesForSubject(ctx, scope, req.Msg.GetSubjectType(), req.Msg.GetSubjectId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Activity, 0, len(rows))
	for _, r := range rows {
		out = append(out, activityToProto(r))
	}
	return connect.NewResponse(&crmv1.ListActivitiesForSubjectResponse{Activities: out}), nil
}

// GetSummary returns per-entity record counts for the calling tenant.
func (s *CRMService) GetSummary(
	ctx context.Context, _ *connect.Request[crmv1.GetSummaryRequest],
) (*connect.Response[crmv1.GetSummaryResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	sum, err := s.store.Summary(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetSummaryResponse{
		Total:  sum.Total,
		Counts: sum.Counts,
	}), nil
}
