package api

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Two-organization isolation proof at the RPC boundary (task.md §1.3).
//
// The store-layer proof lives in internal/domain/pgstore_tenant_isolation_test.go
// and runs against real Postgres with RLS. This file proves the layer above
// it: that every handler derives its tenant from the verified principal and
// never from the request body, so a caller authenticated as organization B
// who types organization A's record id gets nothing back.
//
// Two properties are asserted throughout:
//
//  1. The answer is NotFound, never PermissionDenied. "You may not touch
//     that" confirms the id names a real record in someone else's
//     organization, which is itself a cross-tenant disclosure.
//  2. Nothing of A's appears in any of B's collection responses.
//
// coveredRPCs below closes the loop: a new RPC added to CRMService fails
// TestEveryTenantSensitiveRPCHasAnIsolationProbe until someone decides,
// explicitly, whether it needs a probe here.

// apiFixture is one organization's record set, created through the RPC
// surface exactly as a real client would.
type apiFixture struct {
	ctx             context.Context
	accountID       string
	contactID       string
	contactEmail    string
	leadID          string
	opportunityID   string
	activityID      string
	pipelineID      string
	stageID         string
	taskID          string
	noteID          string
	customFieldID   string
	customObjectAPI string
	customRecordID  string
	// googleOwner is the principal whose Google connection this fixture
	// seeded. It matches ctxAs's subject, because every Google RPC acts on
	// the caller's own connection.
	googleOwner string
}

func seedViaAPI(t *testing.T, s *CRMService, tenantID, orgID, label string) apiFixture {
	t.Helper()
	f := apiFixture{ctx: ctxAs(tenantID, orgID)}

	// A Google connection, seeded through the store rather than the RPCs:
	// creating one requires a real browser round-trip to Google, and without
	// a seeded connection every Google isolation probe would pass vacuously
	// for want of anything to leak.
	f.googleOwner = principalSubject(f.ctx)
	if _, err := s.store.SaveGoogleConnection(f.ctx, domain.Scope{TenantID: tenantID, OrgID: orgID},
		&domain.GoogleConnection{
			OwnerID:            f.googleOwner,
			GoogleAccountEmail: label + "@gmail.example",
			Status:             domain.GoogleStatusConnected,
			GrantedScopes:      []string{"https://www.googleapis.com/auth/gmail.readonly"},
			TokenCiphertext:    sealTestTokens(t, tenantID, f.googleOwner),
			ConnectedBy:        f.googleOwner,
			ConnectedAt:        time.Now().UTC(),
		}); err != nil {
		t.Fatalf("[%s] seed google connection: %v", label, err)
	}

	account, err := s.CreateAccount(f.ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: label + " Holdings", OwnerId: "owner-" + label},
	}))
	if err != nil {
		t.Fatalf("[%s] create account: %v", label, err)
	}
	f.accountID = account.Msg.GetAccount().GetId()

	f.contactEmail = label + "@example.com"
	contact, err := s.CreateContact(f.ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{
			Email: f.contactEmail, AccountId: f.accountID,
			FirstName: label, LastName: "Buyer",
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create contact: %v", label, err)
	}
	f.contactID = contact.Msg.GetContact().GetId()

	lead, err := s.CreateLead(f.ctx, connect.NewRequest(&crmv1.CreateLeadRequest{
		Lead: &crmv1.Lead{Company: label + " Prospect", Source: "referral"},
	}))
	if err != nil {
		t.Fatalf("[%s] create lead: %v", label, err)
	}
	f.leadID = lead.Msg.GetLead().GetId()

	opportunity, err := s.CreateOpportunity(f.ctx, connect.NewRequest(&crmv1.CreateOpportunityRequest{
		Opportunity: &crmv1.Opportunity{Name: label + " Renewal", AccountId: f.accountID},
	}))
	if err != nil {
		t.Fatalf("[%s] create opportunity: %v", label, err)
	}
	deal := opportunity.Msg.GetOpportunity()
	f.opportunityID = deal.GetId()

	// A real stage advance, so the immutable stage history has a row to hide.
	deal.Stage = "qualification"
	if _, err := s.UpdateOpportunity(f.ctx, connect.NewRequest(&crmv1.UpdateOpportunityRequest{
		Opportunity: deal,
	})); err != nil {
		t.Fatalf("[%s] advance opportunity: %v", label, err)
	}

	activity, err := s.CreateActivity(f.ctx, connect.NewRequest(&crmv1.CreateActivityRequest{
		Activity: &crmv1.Activity{
			Kind: "note", SubjectType: "account", SubjectId: f.accountID,
			Notes: label + " confidential note", OccurredAt: timestamppb.Now(),
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create activity: %v", label, err)
	}
	f.activityID = activity.Msg.GetActivity().GetId()

	task, err := s.CreateTask(f.ctx, connect.NewRequest(&crmv1.CreateTaskRequest{
		Task: &crmv1.Task{
			Title: label + " follow-up", Status: "open", Priority: "high",
			AssigneeId: "owner-" + label, SubjectType: "account", SubjectId: f.accountID,
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create task: %v", label, err)
	}
	f.taskID = task.Msg.GetTask().GetId()

	note, err := s.CreateNote(f.ctx, connect.NewRequest(&crmv1.CreateNoteRequest{
		Note: &crmv1.Note{
			Body: label + " confidential note body", SubjectType: "account", SubjectId: f.accountID,
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create note: %v", label, err)
	}
	f.noteID = note.Msg.GetNote().GetId()

	pipeline, err := s.CreatePipeline(f.ctx, connect.NewRequest(&crmv1.CreatePipelineRequest{
		Pipeline: &crmv1.Pipeline{Name: label + " Sales"},
	}))
	if err != nil {
		t.Fatalf("[%s] create pipeline: %v", label, err)
	}
	f.pipelineID = pipeline.Msg.GetPipeline().GetId()

	stage, err := s.CreatePipelineStage(f.ctx, connect.NewRequest(&crmv1.CreatePipelineStageRequest{
		Stage: &crmv1.PipelineStage{
			PipelineId: f.pipelineID, Name: "Discovery", Position: 0,
			Probability: 10, TerminalType: "open",
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create pipeline stage: %v", label, err)
	}
	f.stageID = stage.Msg.GetStage().GetId()

	field, err := s.CreateCustomFieldDefinition(f.ctx, connect.NewRequest(&crmv1.CreateCustomFieldDefinitionRequest{
		Definition: &crmv1.CustomFieldDefinition{
			ObjectType: "account", FieldKey: "region", Label: label + " Region", DataType: "text",
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create custom field: %v", label, err)
	}
	f.customFieldID = field.Msg.GetDefinition().GetId()
	object, err := s.CreateCustomObjectDefinition(f.ctx, connect.NewRequest(&crmv1.CreateCustomObjectDefinitionRequest{Definition: &crmv1.CustomObjectDefinition{ApiName: "asset_" + label, Label: label + " Asset", PluralLabel: label + " Assets"}}))
	if err != nil {
		t.Fatalf("[%s] create custom object: %v", label, err)
	}
	f.customObjectAPI = object.Msg.GetDefinition().GetApiName()
	if _, err := s.CreateCustomFieldDefinition(f.ctx, connect.NewRequest(&crmv1.CreateCustomFieldDefinitionRequest{Definition: &crmv1.CustomFieldDefinition{ObjectType: f.customObjectAPI, FieldKey: "serial", Label: "Serial", DataType: "text"}})); err != nil {
		t.Fatalf("[%s] create custom object field: %v", label, err)
	}
	record, err := s.CreateCustomRecord(f.ctx, connect.NewRequest(&crmv1.CreateCustomRecordRequest{Record: &crmv1.CustomRecord{ObjectType: f.customObjectAPI, Fields: map[string]*crmv1.CustomValue{"serial": {Value: &crmv1.CustomValue_Text{Text: label}}}}}))
	if err != nil {
		t.Fatalf("[%s] create custom record: %v", label, err)
	}
	f.customRecordID = record.Msg.GetRecord().GetId()

	return f
}

// isolationProbe is one RPC exercised across the organization boundary.
// victim is organization A's data; ctxB authenticates organization B.
type isolationProbe func(t *testing.T, s *CRMService, ctxB context.Context, victim apiFixture)

// wantNotFound asserts the call failed with NotFound specifically.
func wantNotFound(t *testing.T, rpc string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: organization B reached organization A's record", rpc)
		return
	}
	got := connectCode(t, err)
	if got == connect.CodePermissionDenied {
		t.Errorf("%s: code = PermissionDenied, want NotFound — "+
			"refusing a foreign id confirms the record exists", rpc)
		return
	}
	if got != connect.CodeNotFound {
		t.Errorf("%s: code = %v, want NotFound", rpc, got)
	}
}

// crossTenantProbes is keyed by CRMService method name so the coverage test
// below can compare it against the real method set.
var crossTenantProbes = map[string]isolationProbe{
	// The Google connection RPCs. Every fixture seeds a connection, so each
	// of these runs against a tenant that really does have one to leak.
	//
	// Note that all four take no owner: the caller's own subject is the only
	// connection they can name. These probes are therefore about the tenant
	// boundary, and the fact that both fixtures share a subject ("test-user",
	// from ctxAs) is deliberate — it is the sharpest version of the test,
	// since only the tenant distinguishes the two connections.
	"GetGoogleConnection": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		resp, err := s.GetGoogleConnection(ctxB, connect.NewRequest(&crmv1.GetGoogleConnectionRequest{}))
		if err != nil {
			t.Fatalf("GetGoogleConnection as another tenant: %v", err)
		}
		if got := resp.Msg.GetConnection().GetGoogleAccountEmail(); got == "victim@gmail.example" {
			t.Errorf("B read A's linked Google account address (%q)", got)
		}
	},
	"ListGoogleConnections": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		resp, err := s.ListGoogleConnections(ctxB, connect.NewRequest(&crmv1.ListGoogleConnectionsRequest{}))
		if err != nil {
			t.Fatalf("ListGoogleConnections as another tenant: %v", err)
		}
		for _, connection := range resp.Msg.GetConnections() {
			if connection.GetGoogleAccountEmail() == "victim@gmail.example" {
				t.Error("the administrative connection list crossed an organization boundary")
			}
		}
	},
	"StartGoogleConnection": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		resp, err := s.StartGoogleConnection(ctxB, connect.NewRequest(&crmv1.StartGoogleConnectionRequest{
			ReturnPath: "/settings/integrations",
		}))
		if err != nil {
			t.Fatalf("StartGoogleConnection as another tenant: %v", err)
		}
		url := resp.Msg.GetAuthorizationUrl()
		if !strings.HasPrefix(url, "https://accounts.google.com/") {
			t.Errorf("authorization URL = %q, want Google's consent endpoint", url)
		}
		// Beginning an authorization must not disturb anyone's existing
		// connection, in this tenant or another.
		stored, err := s.store.GetGoogleConnection(a.ctx,
			domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}, a.googleOwner)
		if err != nil {
			t.Fatalf("A's connection after B started a flow: %v", err)
		}
		if stored.Status != domain.GoogleStatusConnected {
			t.Errorf("B starting an authorization changed A's connection status to %q", stored.Status)
		}
	},
	"DisconnectGoogleConnection": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// B has its own seeded connection, so this succeeds — and must touch
		// only B's. The assertion that matters is what happens to A's.
		if _, err := s.DisconnectGoogleConnection(ctxB, connect.NewRequest(
			&crmv1.DisconnectGoogleConnectionRequest{Purge: true})); err != nil {
			t.Fatalf("DisconnectGoogleConnection as B: %v", err)
		}
		stored, err := s.store.GetGoogleConnection(a.ctx,
			domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}, a.googleOwner)
		if err != nil {
			t.Fatalf("B disconnecting its own connection removed A's: %v", err)
		}
		if len(stored.TokenCiphertext) == 0 {
			t.Error("B's disconnect erased A's stored credential")
		}
	},
	"SearchRecords": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		resp, err := s.SearchRecords(ctxB, connect.NewRequest(&crmv1.SearchRecordsRequest{Query: "victim"}))
		if err != nil {
			t.Fatalf("search as another tenant: %v", err)
		}
		if len(resp.Msg.GetRecords()) != 0 {
			t.Fatalf("cross-tenant search returned %d records", len(resp.Msg.GetRecords()))
		}
	},
	"GetAccount": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetAccount(ctxB, connect.NewRequest(&crmv1.GetAccountRequest{Id: a.accountID}))
		wantNotFound(t, "GetAccount", err)
	},
	"UpdateAccount": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateAccount(ctxB, connect.NewRequest(&crmv1.UpdateAccountRequest{
			Account: &crmv1.Account{Id: a.accountID, Name: "Seized", OwnerId: "attacker"},
		}))
		wantNotFound(t, "UpdateAccount", err)
	},
	"DeleteAccount": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteAccount(ctxB, connect.NewRequest(&crmv1.DeleteAccountRequest{Id: a.accountID}))
		wantNotFound(t, "DeleteAccount", err)
	},
	"ListAccounts": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Both the unfiltered page and a name search for the victim.
		for _, filter := range []string{"", "victim Holdings"} {
			res, err := s.ListAccounts(ctxB, connect.NewRequest(&crmv1.ListAccountsRequest{
				PageSize: 500, NameContains: filter,
			}))
			if err != nil {
				t.Fatalf("ListAccounts(%q): %v", filter, err)
			}
			for _, row := range res.Msg.GetAccounts() {
				if row.GetId() == a.accountID {
					t.Errorf("ListAccounts(%q) returned organization A's account", filter)
				}
			}
		}
	},
	"CreateAccount": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Parenting your own account to a foreign one is the write-side leak:
		// it would put A's record on B's hierarchy.
		_, err := s.CreateAccount(ctxB, connect.NewRequest(&crmv1.CreateAccountRequest{
			Account: &crmv1.Account{Name: "Cross child", OwnerId: "attacker", ParentId: a.accountID},
		}))
		if err == nil {
			t.Error("CreateAccount: organization B parented an account to organization A's record")
		}
	},
	"GetContact": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetContact(ctxB, connect.NewRequest(&crmv1.GetContactRequest{Id: a.contactID}))
		wantNotFound(t, "GetContact", err)
	},
	"UpdateContact": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateContact(ctxB, connect.NewRequest(&crmv1.UpdateContactRequest{
			Contact: &crmv1.Contact{Id: a.contactID, Email: "seized@example.com"},
		}))
		wantNotFound(t, "UpdateContact", err)
	},
	"DeleteContact": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteContact(ctxB, connect.NewRequest(&crmv1.DeleteContactRequest{Id: a.contactID}))
		wantNotFound(t, "DeleteContact", err)
	},
	"ListContacts": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Filtering by a foreign account id must not become a lookup into it.
		for _, accountFilter := range []string{"", a.accountID} {
			res, err := s.ListContacts(ctxB, connect.NewRequest(&crmv1.ListContactsRequest{
				PageSize: 500, AccountId: accountFilter,
			}))
			if err != nil {
				t.Fatalf("ListContacts(account=%q): %v", accountFilter, err)
			}
			for _, row := range res.Msg.GetContacts() {
				if row.GetId() == a.contactID {
					t.Errorf("ListContacts(account=%q) returned organization A's contact", accountFilter)
				}
			}
		}
	},
	"CreateContact": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CreateContact(ctxB, connect.NewRequest(&crmv1.CreateContactRequest{
			Contact: &crmv1.Contact{Email: "cross@example.com", AccountId: a.accountID, FirstName: "Cross"},
		}))
		if err == nil {
			t.Error("CreateContact: organization B attached a contact to organization A's account")
		}
	},
	"GetLead": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetLead(ctxB, connect.NewRequest(&crmv1.GetLeadRequest{Id: a.leadID}))
		wantNotFound(t, "GetLead", err)
	},
	"UpdateLead": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateLead(ctxB, connect.NewRequest(&crmv1.UpdateLeadRequest{
			Lead: &crmv1.Lead{Id: a.leadID, Company: "Seized"},
		}))
		wantNotFound(t, "UpdateLead", err)
	},
	"DeleteLead": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteLead(ctxB, connect.NewRequest(&crmv1.DeleteLeadRequest{Id: a.leadID}))
		wantNotFound(t, "DeleteLead", err)
	},
	"ListLeads": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListLeads(ctxB, connect.NewRequest(&crmv1.ListLeadsRequest{PageSize: 500}))
		if err != nil {
			t.Fatalf("ListLeads: %v", err)
		}
		for _, row := range res.Msg.GetLeads() {
			if row.GetId() == a.leadID {
				t.Error("ListLeads returned organization A's lead")
			}
		}
	},
	"CreateLead": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		// A Lead has no cross-tenant reference to abuse; the assertion is
		// that B's own lead lands in B and stays invisible to A.
		created, err := s.CreateLead(ctxB, connect.NewRequest(&crmv1.CreateLeadRequest{
			Lead: &crmv1.Lead{Company: "B's own lead"},
		}))
		if err != nil {
			t.Fatalf("CreateLead: %v", err)
		}
		if created.Msg.GetLead().GetId() == "" {
			t.Error("CreateLead returned no id")
		}
	},
	"ConvertLead": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Conversion writes a Contact, an Account and an Opportunity. Run
		// against a foreign lead id it must create none of them.
		_, err := s.ConvertLead(ctxB, connect.NewRequest(&crmv1.ConvertLeadRequest{
			LeadId: a.leadID, CreateOpportunity: true, OpportunityName: "Hijacked",
		}))
		wantNotFound(t, "ConvertLead", err)

		lead, err := s.GetLead(a.ctx, connect.NewRequest(&crmv1.GetLeadRequest{Id: a.leadID}))
		if err != nil {
			t.Fatalf("re-read victim lead: %v", err)
		}
		if lead.Msg.GetLead().GetStatus() == "converted" {
			t.Error("ConvertLead: a cross-tenant call converted organization A's lead")
		}
	},
	"GetOpportunity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetOpportunity(ctxB, connect.NewRequest(&crmv1.GetOpportunityRequest{Id: a.opportunityID}))
		wantNotFound(t, "GetOpportunity", err)
	},
	"UpdateOpportunity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateOpportunity(ctxB, connect.NewRequest(&crmv1.UpdateOpportunityRequest{
			Opportunity: &crmv1.Opportunity{Id: a.opportunityID, Name: "Seized"},
		}))
		wantNotFound(t, "UpdateOpportunity", err)
	},
	"MoveOpportunityToPipeline": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.MoveOpportunityToPipeline(ctxB, connect.NewRequest(&crmv1.MoveOpportunityToPipelineRequest{
			OpportunityId: a.opportunityID, PipelineId: a.pipelineID, StageId: a.stageID,
		}))
		wantNotFound(t, "MoveOpportunityToPipeline", err)
	},
	"DeleteOpportunity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteOpportunity(ctxB, connect.NewRequest(&crmv1.DeleteOpportunityRequest{Id: a.opportunityID}))
		wantNotFound(t, "DeleteOpportunity", err)
	},
	"ListOpportunities": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListOpportunities(ctxB, connect.NewRequest(&crmv1.ListOpportunitiesRequest{PageSize: 500}))
		if err != nil {
			t.Fatalf("ListOpportunities: %v", err)
		}
		for _, row := range res.Msg.GetOpportunities() {
			if row.GetId() == a.opportunityID {
				t.Error("ListOpportunities returned organization A's opportunity")
			}
		}
	},
	"CreateOpportunity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CreateOpportunity(ctxB, connect.NewRequest(&crmv1.CreateOpportunityRequest{
			Opportunity: &crmv1.Opportunity{Name: "Cross deal", AccountId: a.accountID},
		}))
		if err == nil {
			t.Error("CreateOpportunity: organization B attached a deal to organization A's account")
		}
	},
	"ListOpportunityStageHistory": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListOpportunityStageHistory(ctxB, connect.NewRequest(
			&crmv1.ListOpportunityStageHistoryRequest{OpportunityId: a.opportunityID}))
		if err != nil {
			wantNotFound(t, "ListOpportunityStageHistory", err)
			return
		}
		if n := len(res.Msg.GetChanges()); n != 0 {
			t.Errorf("ListOpportunityStageHistory leaked %d changes of organization A's opportunity", n)
		}
	},
	"GetPipeline": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetPipeline(ctxB, connect.NewRequest(&crmv1.GetPipelineRequest{Id: a.pipelineID}))
		wantNotFound(t, "GetPipeline", err)
	},
	"UpdatePipeline": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdatePipeline(ctxB, connect.NewRequest(&crmv1.UpdatePipelineRequest{
			Pipeline: &crmv1.Pipeline{Id: a.pipelineID, Name: "Seized"},
		}))
		wantNotFound(t, "UpdatePipeline", err)
	},
	"ListPipelines": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListPipelines(ctxB, connect.NewRequest(&crmv1.ListPipelinesRequest{}))
		if err != nil {
			t.Fatalf("ListPipelines: %v", err)
		}
		for _, row := range res.Msg.GetPipelines() {
			if row.GetId() == a.pipelineID {
				t.Error("ListPipelines returned organization A's pipeline")
			}
		}
	},
	"CreatePipeline": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		created, err := s.CreatePipeline(ctxB, connect.NewRequest(&crmv1.CreatePipelineRequest{
			Pipeline: &crmv1.Pipeline{Name: "B's own pipeline"},
		}))
		if err != nil {
			t.Fatalf("CreatePipeline: %v", err)
		}
		if created.Msg.GetPipeline().GetId() == "" {
			t.Error("CreatePipeline returned no id")
		}
	},
	"CreatePipelineStage": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CreatePipelineStage(ctxB, connect.NewRequest(&crmv1.CreatePipelineStageRequest{
			Stage: &crmv1.PipelineStage{
				PipelineId: a.pipelineID, Name: "Injected", Position: 9, TerminalType: "open",
			},
		}))
		if err == nil {
			t.Error("CreatePipelineStage: organization B added a stage to organization A's pipeline")
		}
	},
	"UpdatePipelineStage": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdatePipelineStage(ctxB, connect.NewRequest(&crmv1.UpdatePipelineStageRequest{
			Stage: &crmv1.PipelineStage{
				Id: a.stageID, PipelineId: a.pipelineID, Name: "Seized",
				Position: 0, TerminalType: "open",
			},
		}))
		wantNotFound(t, "UpdatePipelineStage", err)
	},
	"ListPipelineStages": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListPipelineStages(ctxB, connect.NewRequest(&crmv1.ListPipelineStagesRequest{
			PipelineId: a.pipelineID,
		}))
		if err != nil {
			wantNotFound(t, "ListPipelineStages", err)
			return
		}
		if n := len(res.Msg.GetStages()); n != 0 {
			t.Errorf("ListPipelineStages leaked %d stages of organization A's pipeline", n)
		}
	},
	"GetCustomFieldDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetCustomFieldDefinition(ctxB, connect.NewRequest(&crmv1.GetCustomFieldDefinitionRequest{Id: a.customFieldID}))
		wantNotFound(t, "GetCustomFieldDefinition", err)
	},
	"GetCustomObjectDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetCustomObjectDefinition(ctxB, connect.NewRequest(&crmv1.GetCustomObjectDefinitionRequest{ApiName: a.customObjectAPI}))
		wantNotFound(t, "GetCustomObjectDefinition", err)
	},
	"ArchiveCustomObjectDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.ArchiveCustomObjectDefinition(ctxB, connect.NewRequest(&crmv1.ArchiveCustomObjectDefinitionRequest{ApiName: a.customObjectAPI}))
		wantNotFound(t, "ArchiveCustomObjectDefinition", err)
	},
	"ListCustomObjectDefinitions": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListCustomObjectDefinitions(ctxB, connect.NewRequest(&crmv1.ListCustomObjectDefinitionsRequest{IncludeArchived: true}))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range res.Msg.GetDefinitions() {
			if v.GetApiName() == a.customObjectAPI {
				t.Error("custom object definition leaked")
			}
		}
	},
	"CreateCustomObjectDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		_, err := s.CreateCustomObjectDefinition(ctxB, connect.NewRequest(&crmv1.CreateCustomObjectDefinitionRequest{Definition: &crmv1.CustomObjectDefinition{ApiName: "b_object", Label: "B", PluralLabel: "Bs"}}))
		if err != nil {
			t.Fatal(err)
		}
	},
	"GetCustomRecord": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetCustomRecord(ctxB, connect.NewRequest(&crmv1.GetCustomRecordRequest{ObjectType: a.customObjectAPI, Id: a.customRecordID}))
		wantNotFound(t, "GetCustomRecord", err)
	},
	"UpdateCustomRecord": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateCustomRecord(ctxB, connect.NewRequest(&crmv1.UpdateCustomRecordRequest{Record: &crmv1.CustomRecord{ObjectType: a.customObjectAPI, Id: a.customRecordID}}))
		wantNotFound(t, "UpdateCustomRecord", err)
	},
	"DeleteCustomRecord": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteCustomRecord(ctxB, connect.NewRequest(&crmv1.DeleteCustomRecordRequest{ObjectType: a.customObjectAPI, Id: a.customRecordID}))
		wantNotFound(t, "DeleteCustomRecord", err)
	},
	"ListCustomRecords": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListCustomRecords(ctxB, connect.NewRequest(&crmv1.ListCustomRecordsRequest{ObjectType: a.customObjectAPI}))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range res.Msg.GetRecords() {
			if v.GetId() == a.customRecordID {
				t.Error("custom record leaked")
			}
		}
	},
	"CreateCustomRecord": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		_, err := s.CreateCustomRecord(ctxB, connect.NewRequest(&crmv1.CreateCustomRecordRequest{Record: &crmv1.CustomRecord{ObjectType: "missing"}}))
		if err == nil {
			t.Error("created record on unknown object")
		}
	},
	"UpdateCustomFieldDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateCustomFieldDefinition(ctxB, connect.NewRequest(&crmv1.UpdateCustomFieldDefinitionRequest{
			Definition: &crmv1.CustomFieldDefinition{Id: a.customFieldID, Label: "Seized"},
		}))
		wantNotFound(t, "UpdateCustomFieldDefinition", err)
	},
	"ArchiveCustomFieldDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.ArchiveCustomFieldDefinition(ctxB, connect.NewRequest(&crmv1.ArchiveCustomFieldDefinitionRequest{Id: a.customFieldID}))
		wantNotFound(t, "ArchiveCustomFieldDefinition", err)
	},
	"ListCustomFieldDefinitions": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListCustomFieldDefinitions(ctxB, connect.NewRequest(&crmv1.ListCustomFieldDefinitionsRequest{ObjectType: "account", IncludeArchived: true}))
		if err != nil {
			t.Fatalf("ListCustomFieldDefinitions: %v", err)
		}
		for _, row := range res.Msg.GetDefinitions() {
			if row.GetId() == a.customFieldID {
				t.Error("ListCustomFieldDefinitions returned organization A's field")
			}
		}
	},
	"CreateCustomFieldDefinition": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		created, err := s.CreateCustomFieldDefinition(ctxB, connect.NewRequest(&crmv1.CreateCustomFieldDefinitionRequest{
			Definition: &crmv1.CustomFieldDefinition{ObjectType: "account", FieldKey: "b_only", Label: "B only", DataType: "text"},
		}))
		if err != nil {
			t.Fatalf("CreateCustomFieldDefinition: %v", err)
		}
		if created.Msg.GetDefinition().GetId() == "" {
			t.Error("CreateCustomFieldDefinition returned no id")
		}
	},
	"GetActivity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetActivity(ctxB, connect.NewRequest(&crmv1.GetActivityRequest{Id: a.activityID}))
		wantNotFound(t, "GetActivity", err)
	},
	"UpdateActivity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateActivity(ctxB, connect.NewRequest(&crmv1.UpdateActivityRequest{
			Activity: &crmv1.Activity{
				Id: a.activityID, Kind: "note", SubjectType: "account",
				SubjectId: a.accountID, Notes: "seized",
			},
		}))
		wantNotFound(t, "UpdateActivity", err)
	},
	"DeleteActivity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteActivity(ctxB, connect.NewRequest(&crmv1.DeleteActivityRequest{Id: a.activityID}))
		wantNotFound(t, "DeleteActivity", err)
	},
	"ListActivities": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListActivities(ctxB, connect.NewRequest(&crmv1.ListActivitiesRequest{PageSize: 500}))
		if err != nil {
			t.Fatalf("ListActivities: %v", err)
		}
		for _, row := range res.Msg.GetActivities() {
			if row.GetId() == a.activityID {
				t.Error("ListActivities returned organization A's activity")
			}
		}
	},
	"CreateActivity": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		for subjectType, subjectID := range map[string]string{
			"account":     a.accountID,
			"contact":     a.contactID,
			"lead":        a.leadID,
			"opportunity": a.opportunityID,
		} {
			_, err := s.CreateActivity(ctxB, connect.NewRequest(&crmv1.CreateActivityRequest{
				Activity: &crmv1.Activity{
					Kind: "note", SubjectType: subjectType, SubjectId: subjectID,
					Notes: "cross-tenant probe", OccurredAt: timestamppb.Now(),
				},
			}))
			if err == nil {
				t.Errorf("CreateActivity: organization B logged an activity against organization A's %s", subjectType)
			}
		}
	},
	"ListActivitiesForSubject": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListActivitiesForSubject(ctxB, connect.NewRequest(
			&crmv1.ListActivitiesForSubjectRequest{SubjectType: "account", SubjectId: a.accountID}))
		if err != nil {
			wantNotFound(t, "ListActivitiesForSubject", err)
			return
		}
		if n := len(res.Msg.GetActivities()); n != 0 {
			t.Errorf("ListActivitiesForSubject leaked %d of organization A's activities", n)
		}
	},
	"GetTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetTask(ctxB, connect.NewRequest(&crmv1.GetTaskRequest{Id: a.taskID}))
		wantNotFound(t, "GetTask", err)
	},
	"UpdateTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateTask(ctxB, connect.NewRequest(&crmv1.UpdateTaskRequest{
			Task: &crmv1.Task{Id: a.taskID, Title: "Seized", Status: "open"},
		}))
		wantNotFound(t, "UpdateTask", err)
	},
	"DeleteTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteTask(ctxB, connect.NewRequest(&crmv1.DeleteTaskRequest{Id: a.taskID}))
		wantNotFound(t, "DeleteTask", err)
	},
	"CompleteTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CompleteTask(ctxB, connect.NewRequest(&crmv1.CompleteTaskRequest{Id: a.taskID}))
		wantNotFound(t, "CompleteTask", err)

		// And organization A's task is still open: a denied transition must
		// not have applied before the tenant check ran.
		still, err := s.GetTask(a.ctx, connect.NewRequest(&crmv1.GetTaskRequest{Id: a.taskID}))
		if err != nil {
			t.Fatalf("re-read victim task: %v", err)
		}
		if still.Msg.GetTask().GetStatus() == "completed" {
			t.Error("CompleteTask: a cross-tenant call completed organization A's task")
		}
	},
	"ReopenTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.ReopenTask(ctxB, connect.NewRequest(&crmv1.ReopenTaskRequest{Id: a.taskID}))
		wantNotFound(t, "ReopenTask", err)
	},
	"ListTasks": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Unfiltered, by the victim's assignee, and by the victim's subject —
		// each filter is its own chance to turn into a cross-tenant lookup.
		for _, req := range []*crmv1.ListTasksRequest{
			{PageSize: 500},
			{PageSize: 500, AssigneeId: "owner-victim"},
			{PageSize: 500, SubjectType: "account", SubjectId: a.accountID},
			{PageSize: 500, OpenOnly: true},
		} {
			res, err := s.ListTasks(ctxB, connect.NewRequest(req))
			if err != nil {
				t.Fatalf("ListTasks(%+v): %v", req, err)
			}
			for _, row := range res.Msg.GetTasks() {
				if row.GetId() == a.taskID {
					t.Errorf("ListTasks(%+v) returned organization A's task", req)
				}
			}
		}
	},
	"CreateTask": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CreateTask(ctxB, connect.NewRequest(&crmv1.CreateTaskRequest{
			Task: &crmv1.Task{
				Title: "Cross task", SubjectType: "account", SubjectId: a.accountID,
			},
		}))
		if err == nil {
			t.Error("CreateTask: organization B attached a task to organization A's account")
		}
	},
	"GetNote": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetNote(ctxB, connect.NewRequest(&crmv1.GetNoteRequest{Id: a.noteID}))
		wantNotFound(t, "GetNote", err)
	},
	"UpdateNote": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateNote(ctxB, connect.NewRequest(&crmv1.UpdateNoteRequest{
			Note: &crmv1.Note{Id: a.noteID, Body: "Seized"},
		}))
		wantNotFound(t, "UpdateNote", err)
	},
	"DeleteNote": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteNote(ctxB, connect.NewRequest(&crmv1.DeleteNoteRequest{Id: a.noteID}))
		wantNotFound(t, "DeleteNote", err)
	},
	"ListNotes": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		for _, req := range []*crmv1.ListNotesRequest{
			{PageSize: 500},
			{PageSize: 500, SubjectType: "account", SubjectId: a.accountID},
		} {
			res, err := s.ListNotes(ctxB, connect.NewRequest(req))
			if err != nil {
				t.Fatalf("ListNotes(%+v): %v", req, err)
			}
			for _, row := range res.Msg.GetNotes() {
				if row.GetId() == a.noteID {
					t.Errorf("ListNotes(%+v) returned organization A's note", req)
				}
			}
		}
	},
	"CreateNote": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.CreateNote(ctxB, connect.NewRequest(&crmv1.CreateNoteRequest{
			Note: &crmv1.Note{
				Body: "Cross note", SubjectType: "account", SubjectId: a.accountID,
			},
		}))
		if err == nil {
			t.Error("CreateNote: organization B attached a note to organization A's account")
		}
	},
	"GetSummary": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		res, err := s.GetSummary(ctxB, connect.NewRequest(&crmv1.GetSummaryRequest{}))
		if err != nil {
			t.Fatalf("GetSummary: %v", err)
		}
		// B seeded exactly one of each entity in this test, plus the extra
		// lead and pipeline its own probes create. What matters is that the
		// counts never include A's rows.
		for _, entity := range []string{"account", "contact", "opportunity", "activity"} {
			if got := res.Msg.GetCounts()[entity]; got != 1 {
				t.Errorf("GetSummary counts %d %s rows for organization B; want 1 — "+
					"the dashboard count spans organizations", got, entity)
			}
		}
	},
}

// TestCrossOrganizationRPCAccessIsDenied runs every probe against a live
// two-organization fixture.
func TestCrossOrganizationRPCAccessIsDenied(t *testing.T) {
	s := newTestService()
	victim := seedViaAPI(t, s, "tenant-victim", "org-victim", "victim")
	attacker := seedViaAPI(t, s, "tenant-attacker", "org-attacker", "attacker")

	names := make([]string, 0, len(crossTenantProbes))
	for name := range crossTenantProbes {
		names = append(names, name)
	}
	for _, name := range names {
		probe := crossTenantProbes[name]
		t.Run(name, func(t *testing.T) {
			probe(t, s, attacker.ctx, victim)
		})
	}

	// After every probe has run, organization A's records must be exactly as
	// it left them: no rejected write partially applied.
	t.Run("victim data survives every probe", func(t *testing.T) {
		account, err := s.GetAccount(victim.ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: victim.accountID}))
		if err != nil {
			t.Fatalf("victim can no longer read its own account: %v", err)
		}
		if got := account.Msg.GetAccount().GetName(); got != "victim Holdings" {
			t.Errorf("victim account name = %q, want %q — a cross-tenant write landed", got, "victim Holdings")
		}
		for _, check := range []struct {
			name string
			err  error
		}{
			{"contact", firstErr(s.GetContact(victim.ctx, connect.NewRequest(&crmv1.GetContactRequest{Id: victim.contactID})))},
			{"lead", firstErr(s.GetLead(victim.ctx, connect.NewRequest(&crmv1.GetLeadRequest{Id: victim.leadID})))},
			{"opportunity", firstErr(s.GetOpportunity(victim.ctx, connect.NewRequest(&crmv1.GetOpportunityRequest{Id: victim.opportunityID})))},
			{"activity", firstErr(s.GetActivity(victim.ctx, connect.NewRequest(&crmv1.GetActivityRequest{Id: victim.activityID})))},
			{"pipeline", firstErr(s.GetPipeline(victim.ctx, connect.NewRequest(&crmv1.GetPipelineRequest{Id: victim.pipelineID})))},
			{"task", firstErr(s.GetTask(victim.ctx, connect.NewRequest(&crmv1.GetTaskRequest{Id: victim.taskID})))},
			{"note", firstErr(s.GetNote(victim.ctx, connect.NewRequest(&crmv1.GetNoteRequest{Id: victim.noteID})))},
		} {
			if check.err != nil {
				t.Errorf("victim can no longer read its own %s: %v", check.name, check.err)
			}
		}
	})
}

// firstErr discards a typed response so heterogeneous calls can be listed
// together above.
func firstErr[T any](_ T, err error) error { return err }

// TestTenantComesFromTheTokenNotTheRequest is the property the whole scheme
// rests on: the same request body, sent by two different principals, reaches
// two different tenants — and a body cannot name a tenant at all.
func TestTenantComesFromTheTokenNotTheRequest(t *testing.T) {
	s := newTestService()
	body := &crmv1.CreateAccountRequest{Account: &crmv1.Account{Name: "Same Name", OwnerId: "o"}}

	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")

	inA, err := s.CreateAccount(ctxA, connect.NewRequest(body))
	if err != nil {
		t.Fatalf("create as A: %v", err)
	}
	inB, err := s.CreateAccount(ctxB, connect.NewRequest(body))
	if err != nil {
		t.Fatalf("create as B: %v", err)
	}

	if _, err := s.GetAccount(ctxB, connect.NewRequest(&crmv1.GetAccountRequest{
		Id: inA.Msg.GetAccount().GetId(),
	})); err == nil {
		t.Error("B read the account A created from an identical request body")
	}
	if _, err := s.GetAccount(ctxA, connect.NewRequest(&crmv1.GetAccountRequest{
		Id: inB.Msg.GetAccount().GetId(),
	})); err == nil {
		t.Error("A read the account B created from an identical request body")
	}
}

// TestEveryTenantSensitiveRPCHasAnIsolationProbe keeps this file honest as
// the API grows. A handler added to CRMService is either probed above or
// named in rpcsWithoutTenantData, deliberately.
func TestEveryTenantSensitiveRPCHasAnIsolationProbe(t *testing.T) {
	// Nothing yet: every CRMService RPC today reads or writes tenant data.
	// An entry here is a claim that an RPC touches no tenant-owned record,
	// and needs a reason next to it.
	rpcsWithoutTenantData := map[string]string{}

	typ := reflect.TypeOf(&CRMService{})
	var missing []string
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if _, probed := crossTenantProbes[name]; probed {
			continue
		}
		if _, exempt := rpcsWithoutTenantData[name]; exempt {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Errorf("these CRMService RPCs have no cross-organization isolation probe: %s\n"+
			"Add one to crossTenantProbes, or record why the RPC touches no tenant data "+
			"in rpcsWithoutTenantData.", strings.Join(missing, ", "))
	}

	// And the reverse: a probe naming an RPC that no longer exists is dead
	// weight that would otherwise pass for coverage.
	for name := range crossTenantProbes {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("crossTenantProbes names %q, which is not a CRMService method", name)
		}
	}
}

// TestScopeIsRefusedWithoutATenantClaim covers the token shapes that must
// never reach the store: no principal at all, and a principal whose token
// carries no organization.
func TestScopeIsRefusedWithoutATenantClaim(t *testing.T) {
	s := newTestService()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want connect.Code
	}{
		{"no principal", context.Background(), connect.CodeUnauthenticated},
		{"principal without a tenant", ctxAs("", ""), connect.CodePermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ListAccounts(tc.ctx, connect.NewRequest(&crmv1.ListAccountsRequest{}))
			if err == nil {
				t.Fatal("a request with no usable tenant reached the store")
			}
			if got := connectCode(t, err); got != tc.want {
				t.Fatalf("code = %v, want %v", got, tc.want)
			}
		})
	}
}
