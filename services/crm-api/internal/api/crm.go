package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/calendar"
	"github.com/santoshguru/crm/internal/integration/gmail"
	"github.com/santoshguru/crm/internal/integration/google"
	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
	"github.com/santoshguru/crm/internal/integration/privacy"
	"github.com/santoshguru/crm/internal/secrets"
)

// CRMService implements the module's domain API over a tenant-scoped Store.
type CRMService struct {
	cfg   config.Config
	store domain.Store
	// google is nil when the deployment supplied no Google credentials. The
	// RPCs report that as a precondition failure rather than an error: a CRM
	// without GOOGLE_OAUTH_* is running correctly and simply cannot offer the
	// feature.
	google *google.Service
	meta   *metaintegration.Service
	// gmail is nil for the same reason google is: without provider
	// credentials there is no mailbox to synchronise.
	gmail    *gmail.Syncer
	calendar *calendar.Syncer
	// privacy is never nil: erasure must work for content collected before
	// the integration was switched off.
	privacy *privacy.Service
	// capture seals lead-capture signing secrets, and is nil when the
	// deployment set no LEAD_CAPTURE_SECRET_KEY. Only the two RPCs that mint
	// a secret need it; listing endpoints and reading the delivery log work
	// either way, so an administrator can still see what exists.
	capture *secrets.Sealer
	// gmailSender is nil unless Google is configured. A nil sender is how
	// SendOutboundMessage answers "can this deployment send at all" — it
	// reports a precondition failure and tells the user to send it themselves.
	gmailSender *gmail.Sender
}

// NewCRMService wires the domain service. The integration arguments may all
// be nil, which is what a deployment without Google credentials looks like.
func NewCRMService(
	cfg config.Config, store domain.Store, googleSvc *google.Service,
	gmailSyncer *gmail.Syncer, calendarSyncer *calendar.Syncer,
	privacySvc *privacy.Service, captureSealer *secrets.Sealer,
	gmailSender *gmail.Sender,
	metaSvc *metaintegration.Service,
) *CRMService {
	if privacySvc == nil {
		// Unlike the Google services, this one is not optional: a
		// data-subject request must be servable for content collected before
		// the integration was switched off, and there is no honest way to
		// answer "we cannot delete your data because the feature that
		// collected it is disabled". Defaulting here means no call site can
		// leave it nil and turn an erasure into a panic. Retention stays off,
		// which is the safe half to default.
		privacySvc = privacy.New(store, 0, nil)
	}
	return &CRMService{
		cfg: cfg, store: store, google: googleSvc,
		gmail: gmailSyncer, calendar: calendarSyncer, privacy: privacySvc,
		capture: captureSealer, gmailSender: gmailSender, meta: metaSvc,
	}
}

// CreateAccount stores a new Account.
func (s *CRMService) CreateAccount(
	ctx context.Context, req *connect.Request[crmv1.CreateAccountRequest],
) (*connect.Response[crmv1.CreateAccountResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := accountFromProto(req.Msg.GetAccount())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "account", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateAccount(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.accountForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateAccountResponse{Account: result}), nil
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
	result, err := s.accountForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetAccountResponse{Account: result}), nil
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
		result, err := s.accountForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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
	in := accountFromProto(req.Msg.GetAccount())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "account", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateAccount(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.accountForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateAccountResponse{Account: result}), nil
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
	in := contactFromProto(req.Msg.GetContact())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "contact", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateContact(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.contactForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateContactResponse{Contact: result}), nil
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
	result, err := s.contactForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetContactResponse{Contact: result}), nil
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
		result, err := s.contactForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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
	in := contactFromProto(req.Msg.GetContact())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "contact", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateContact(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.contactForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateContactResponse{Contact: result}), nil
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
	in := leadFromProto(req.Msg.GetLead())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "lead", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateLead(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.leadForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateLeadResponse{Lead: result}), nil
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
	result, err := s.leadForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetLeadResponse{Lead: result}), nil
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
		result, err := s.leadForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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
	in := leadFromProto(req.Msg.GetLead())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "lead", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateLead(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.leadForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateLeadResponse{Lead: result}), nil
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
	lead, err := s.leadForRead(ctx, scope, out.Lead)
	if err != nil {
		return nil, err
	}
	contact, err := s.contactForRead(ctx, scope, out.Contact)
	if err != nil {
		return nil, err
	}
	account, err := s.accountForRead(ctx, scope, out.Account)
	if err != nil {
		return nil, err
	}
	opportunity, err := s.opportunityForRead(ctx, scope, out.Opportunity)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.ConvertLeadResponse{Lead: lead, Contact: contact, Account: account, Opportunity: opportunity}), nil
}

// CreateOpportunity stores a new Opportunity.
func (s *CRMService) CreateOpportunity(
	ctx context.Context, req *connect.Request[crmv1.CreateOpportunityRequest],
) (*connect.Response[crmv1.CreateOpportunityResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := opportunityFromProto(req.Msg.GetOpportunity())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "opportunity", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateOpportunity(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.opportunityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateOpportunityResponse{Opportunity: result}), nil
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
	result, err := s.opportunityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetOpportunityResponse{Opportunity: result}), nil
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
		result, err := s.opportunityForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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
	in := opportunityFromProto(req.Msg.GetOpportunity())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "opportunity", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateOpportunity(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.opportunityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateOpportunityResponse{Opportunity: result}), nil
}

// MoveOpportunityToPipeline relocates an open opportunity to the caller's
// explicit destination. It intentionally accepts no replacement document, so
// an administrative move cannot overwrite a concurrently edited name,
// amount, close reason, or custom-field document.
func (s *CRMService) MoveOpportunityToPipeline(
	ctx context.Context, req *connect.Request[crmv1.MoveOpportunityToPipelineRequest],
) (*connect.Response[crmv1.MoveOpportunityToPipelineResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.MoveOpportunityToPipeline(ctx, scope, domain.MoveOpportunityToPipelineInput{
		OpportunityID: req.Msg.GetOpportunityId(),
		PipelineID:    req.Msg.GetPipelineId(),
		StageID:       req.Msg.GetStageId(),
		UpdatedAt:     protoTimestamp(req.Msg.GetUpdatedAt()),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.opportunityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.MoveOpportunityToPipelineResponse{Opportunity: result}), nil
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
	in := activityFromProto(req.Msg.GetActivity())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "activity", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateActivity(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.activityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateActivityResponse{Activity: result}), nil
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
	result, err := s.activityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetActivityResponse{Activity: result}), nil
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
		result, err := s.activityForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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
	in := activityFromProto(req.Msg.GetActivity())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "activity", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateActivity(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.activityForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateActivityResponse{Activity: result}), nil
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
		result, err := s.activityForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
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

// SearchRecords returns a small ranked set of tenant-local navigation
// results. The result intentionally contains no custom-field data: fields may
// have role-specific visibility, while a detail request already applies that
// policy before displaying a record.
func (s *CRMService) SearchRecords(
	ctx context.Context, req *connect.Request[crmv1.SearchRecordsRequest],
) (*connect.Response[crmv1.SearchRecordsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.SearchRecords(ctx, scope, req.Msg.GetQuery(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.SearchRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, &crmv1.SearchRecord{
			ObjectType: row.ObjectType,
			Id:         row.ID,
			Title:      row.Title,
			Subtitle:   row.Subtitle,
		})
	}
	return connect.NewResponse(&crmv1.SearchRecordsResponse{Records: out}), nil
}

// --- Tasks (task.md §5.1) ---

// CreateTask stores a new Task. created_by defaults to the calling principal
// so a client cannot claim someone else authored the request.
func (s *CRMService) CreateTask(
	ctx context.Context, req *connect.Request[crmv1.CreateTaskRequest],
) (*connect.Response[crmv1.CreateTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	task := taskFromProto(req.Msg.GetTask())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "task", task.CustomFields); err != nil {
		return nil, err
	}
	if task.CreatedBy == "" {
		task.CreatedBy = principalSubject(ctx)
	}
	out, err := s.store.CreateTask(ctx, scope, task)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.taskForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateTaskResponse{Task: result}), nil
}

// GetTask returns one Task.
func (s *CRMService) GetTask(
	ctx context.Context, req *connect.Request[crmv1.GetTaskRequest],
) (*connect.Response[crmv1.GetTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetTask(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.taskForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetTaskResponse{Task: result}), nil
}

// ListTasks returns one page of Tasks. sort is validated against the domain's
// allowlist here, at the RPC boundary, rather than passed through as a
// free-form string.
func (s *CRMService) ListTasks(
	ctx context.Context, req *connect.Request[crmv1.ListTasksRequest],
) (*connect.Response[crmv1.ListTasksResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	sortBy, err := domain.ParseTaskSort(req.Msg.GetSort())
	if err != nil {
		return nil, connectErr(err)
	}
	rows, next, err := s.store.ListTasks(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	}, domain.TaskFilter{
		AssigneeID:  req.Msg.GetAssigneeId(),
		Status:      req.Msg.GetStatus(),
		SubjectType: req.Msg.GetSubjectType(),
		SubjectID:   req.Msg.GetSubjectId(),
		OpenOnly:    req.Msg.GetOpenOnly(),
		DueBefore:   protoTimestamp(req.Msg.GetDueBefore()),
	}, sortBy)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Task, 0, len(rows))
	for _, r := range rows {
		result, err := s.taskForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return connect.NewResponse(&crmv1.ListTasksResponse{Tasks: out, NextPageToken: next}), nil
}

// UpdateTask replaces an existing Task.
func (s *CRMService) UpdateTask(
	ctx context.Context, req *connect.Request[crmv1.UpdateTaskRequest],
) (*connect.Response[crmv1.UpdateTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := taskFromProto(req.Msg.GetTask())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "task", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateTask(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.taskForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateTaskResponse{Task: result}), nil
}

// DeleteTask removes a Task.
func (s *CRMService) DeleteTask(
	ctx context.Context, req *connect.Request[crmv1.DeleteTaskRequest],
) (*connect.Response[crmv1.DeleteTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteTask(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteTaskResponse{}), nil
}

// CompleteTask marks a Task done. actor_id defaults to the calling principal.
func (s *CRMService) CompleteTask(
	ctx context.Context, req *connect.Request[crmv1.CompleteTaskRequest],
) (*connect.Response[crmv1.CompleteTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	actor := req.Msg.GetActorId()
	if actor == "" {
		actor = principalSubject(ctx)
	}
	out, err := s.store.CompleteTask(ctx, scope, req.Msg.GetId(), actor)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.taskForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CompleteTaskResponse{Task: result}), nil
}

// ReopenTask returns a Task to open and clears its completion record.
func (s *CRMService) ReopenTask(
	ctx context.Context, req *connect.Request[crmv1.ReopenTaskRequest],
) (*connect.Response[crmv1.ReopenTaskResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.ReopenTask(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.taskForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.ReopenTaskResponse{Task: result}), nil
}

// --- Notes (task.md §5.1) ---

// CreateNote stores a new Note. author_id defaults to the calling principal:
// a note is evidence of who said what, so the author must not be assertable
// by whoever happens to be posting.
func (s *CRMService) CreateNote(
	ctx context.Context, req *connect.Request[crmv1.CreateNoteRequest],
) (*connect.Response[crmv1.CreateNoteResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	note := noteFromProto(req.Msg.GetNote())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "note", note.CustomFields); err != nil {
		return nil, err
	}
	if note.AuthorID == "" {
		note.AuthorID = principalSubject(ctx)
	}
	out, err := s.store.CreateNote(ctx, scope, note)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.noteForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateNoteResponse{Note: result}), nil
}

// GetNote returns one Note.
func (s *CRMService) GetNote(
	ctx context.Context, req *connect.Request[crmv1.GetNoteRequest],
) (*connect.Response[crmv1.GetNoteResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetNote(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.noteForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetNoteResponse{Note: result}), nil
}

// ListNotes returns one page of Notes, newest first.
func (s *CRMService) ListNotes(
	ctx context.Context, req *connect.Request[crmv1.ListNotesRequest],
) (*connect.Response[crmv1.ListNotesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.store.ListNotes(ctx, scope, domain.Page{
		Size:  int(req.Msg.GetPageSize()),
		Token: req.Msg.GetPageToken(),
	}, domain.NoteFilter{
		SubjectType: req.Msg.GetSubjectType(),
		SubjectID:   req.Msg.GetSubjectId(),
		AuthorID:    req.Msg.GetAuthorId(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Note, 0, len(rows))
	for _, r := range rows {
		result, err := s.noteForRead(ctx, scope, r)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return connect.NewResponse(&crmv1.ListNotesResponse{Notes: out, NextPageToken: next}), nil
}

// UpdateNote edits a Note's body. Subject and author are not editable.
func (s *CRMService) UpdateNote(
	ctx context.Context, req *connect.Request[crmv1.UpdateNoteRequest],
) (*connect.Response[crmv1.UpdateNoteResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := noteFromProto(req.Msg.GetNote())
	if err := s.authorizeCustomFieldWrite(ctx, scope, "note", in.CustomFields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateNote(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.noteForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateNoteResponse{Note: result}), nil
}

// DeleteNote removes a Note.
func (s *CRMService) DeleteNote(
	ctx context.Context, req *connect.Request[crmv1.DeleteNoteRequest],
) (*connect.Response[crmv1.DeleteNoteResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteNote(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteNoteResponse{}), nil
}
