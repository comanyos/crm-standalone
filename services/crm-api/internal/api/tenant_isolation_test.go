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
	// The automation fixtures. Both organizations seed an active rule, a
	// published version, an execution and a notification, so no workflow
	// probe can pass for want of anything to leak.
	workflowRuleID    string
	workflowVersionID string
	notificationID    string
	// tagID names a tag created and assigned to accountID, so a tag probe
	// has both a definition and an assignment to try to leak.
	tagID string
	// savedViewID names a private saved view, owned by this fixture's own
	// principal (ctxAs always authenticates as "test-user" — see ctxAs in
	// crm_test.go), so a saved-view probe has something to try to leak.
	savedViewID string
	// accountMergeID names the merge that folded a throwaway duplicate
	// account into accountID, so a merge probe has an audit record and a
	// (deleted) duplicate id to try to leak or resurrect.
	accountMergeID string
	// importJobID names a committed import job, so an import probe has a
	// job (carrying the uploaded CSV content) to try to leak or resurrect.
	importJobID string
	// exportJobID names a recorded export audit row, so a list-exports
	// probe has something to try to leak.
	exportJobID string
	// templateID names a message template this organization authored, so a
	// template probe has wording to try to read, rewrite or delete.
	templateID string
	// consentID names this organization's withdrawal against contactEmail.
	// Probes compare against the id rather than counting rows, because the
	// probes share one service: another probe legitimately records
	// organization B's own decision for the same address, and a count would
	// then fail on its own side effect rather than on a leak.
	consentID string
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
			// A phone in full international form, so this contact derives
			// every messaging channel rather than only email — a local
			// number would come back unreachable and a channel probe would
			// pass for want of anything to leak.
			Phone: "+441632960001",
		},
	}))
	if err != nil {
		t.Fatalf("[%s] create contact: %v", label, err)
	}
	f.contactID = contact.Msg.GetContact().GetId()

	// A withdrawal against this contact's own email, so a consent probe has a
	// real decision to try to read across the tenant boundary. It is the most
	// sensitive row shape the ledger holds: it names a person and says they
	// refused to be contacted.
	template, err := s.CreateMessageTemplate(f.ctx, connect.NewRequest(&crmv1.CreateMessageTemplateRequest{
		Name: label + " confidential opener", ChannelKind: "whatsapp",
		Body: "Hi {{contact.first_name}}, " + label + " has a confidential offer for you.",
	}))
	if err != nil {
		t.Fatalf("[%s] seed message template: %v", label, err)
	}
	f.templateID = template.Msg.GetTemplate().GetId()

	consent, err := s.RecordMessagingConsent(f.ctx, connect.NewRequest(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: f.contactEmail, State: "withdrawn",
		Source: "manual", Evidence: label + " asked to be removed",
	}))
	if err != nil {
		t.Fatalf("[%s] seed messaging consent: %v", label, err)
	}
	f.consentID = consent.Msg.GetConsent().GetId()

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

	// One synced message on the contact, so the erasure probe has something
	// real to try to destroy.
	syncedActivity := domain.NewID()
	if _, err := s.store.RecordGmailActivity(f.ctx,
		domain.Scope{TenantID: tenantID, OrgID: orgID},
		&domain.GmailMessage{
			OwnerID: f.googleOwner, ProviderMessageID: "msg-" + label,
			SubjectType: domain.SubjectContact, SubjectID: f.contactID,
			ActivityID: syncedActivity, MessageSubject: label + " confidential terms",
			FromAddress: f.contactEmail, Direction: domain.GmailInbound,
			OccurredAt: time.Now().UTC(),
		},
		&domain.Activity{
			ID: syncedActivity, Kind: domain.GmailActivityKind,
			SubjectType: domain.SubjectContact, SubjectID: f.contactID,
			Notes:   "Email received: " + label + " confidential terms",
			ActorID: f.googleOwner, OccurredAt: time.Now().UTC(),
		}); err != nil {
		t.Fatalf("[%s] seed synced message: %v", label, err)
	}

	// An active automation rule with a published version, an execution
	// against it, and a notification for this fixture's own principal.
	rule, err := s.CreateWorkflowRule(f.ctx, connect.NewRequest(&crmv1.CreateWorkflowRuleRequest{
		Name: label + " follow-up", Description: label + " confidential playbook",
	}))
	if err != nil {
		t.Fatalf("[%s] create workflow rule: %v", label, err)
	}
	f.workflowRuleID = rule.Msg.GetRule().GetId()

	version, err := s.PublishWorkflowRuleVersion(f.ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId:           f.workflowRuleID,
			TriggerEventType: domain.EventLeadCreated,
			Conditions: []*crmv1.WorkflowCondition{
				{Field: "source", Operator: domain.OpEquals, Value: "referral"},
			},
			Actions: []*crmv1.WorkflowAction{{
				Type: domain.ActionCreateTask, Title: label + " call the referral",
				Assignee: domain.AssigneeRecordOwner,
			}},
			Activate: true,
		}))
	if err != nil {
		t.Fatalf("[%s] publish workflow version: %v", label, err)
	}
	f.workflowVersionID = version.Msg.GetVersion().GetId()

	// The execution and the notification are seeded through the store: the
	// engine writes both, and there is no RPC that creates either.
	scope := domain.Scope{TenantID: tenantID, OrgID: orgID}
	if _, _, err := s.store.ClaimWorkflowExecution(f.ctx, scope, &domain.WorkflowExecution{
		RuleID: f.workflowRuleID, RuleVersionID: f.workflowVersionID,
		SourceEventID: "event-" + label, SourceEventType: domain.EventLeadCreated,
		SubjectType: domain.SubjectLead, SubjectID: f.leadID,
		Status: domain.ExecutionSucceeded,
	}); err != nil {
		t.Fatalf("[%s] seed workflow execution: %v", label, err)
	}
	notification, err := s.store.CreateNotification(f.ctx, scope, &domain.Notification{
		RecipientID: f.googleOwner, Kind: "workflow",
		Title: label + " confidential alert", Body: label + " internal note",
		SubjectType: domain.SubjectLead, SubjectID: f.leadID, RuleID: f.workflowRuleID,
	})
	if err != nil {
		t.Fatalf("[%s] seed notification: %v", label, err)
	}
	f.notificationID = notification.ID

	tag, err := s.CreateTag(f.ctx, connect.NewRequest(&crmv1.CreateTagRequest{Name: label + " confidential tag"}))
	if err != nil {
		t.Fatalf("[%s] seed tag: %v", label, err)
	}
	f.tagID = tag.Msg.GetTag().GetId()
	if _, err := s.AssignTag(f.ctx, connect.NewRequest(&crmv1.AssignTagRequest{
		TagId: f.tagID, ObjectType: "account", ObjectId: f.accountID,
	})); err != nil {
		t.Fatalf("[%s] seed tag assignment: %v", label, err)
	}

	view, err := s.CreateSavedView(f.ctx, connect.NewRequest(&crmv1.CreateSavedViewRequest{
		ObjectType: "account", Name: label + " confidential view",
		Filters: map[string]string{"tier": "gold"},
	}))
	if err != nil {
		t.Fatalf("[%s] seed saved view: %v", label, err)
	}
	f.savedViewID = view.Msg.GetView().GetId()

	dup, err := s.CreateAccount(f.ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: label + " duplicate", OwnerId: "attacker"},
	}))
	if err != nil {
		t.Fatalf("[%s] seed duplicate account: %v", label, err)
	}
	merge, err := s.MergeAccounts(f.ctx, connect.NewRequest(&crmv1.MergeAccountsRequest{
		SurvivorId: f.accountID, DuplicateId: dup.Msg.GetAccount().GetId(),
	}))
	if err != nil {
		t.Fatalf("[%s] seed account merge: %v", label, err)
	}
	f.accountMergeID = merge.Msg.GetMerge().GetId()

	importJob, err := s.CreateImportJob(f.ctx, connect.NewRequest(&crmv1.CreateImportJobRequest{
		ObjectType: "account", FileName: label + "-confidential.csv",
		Content: "Company\n" + label + " Confidential Import\n",
		Mapping: map[string]string{"Company": "name"},
	}))
	if err != nil {
		t.Fatalf("[%s] seed import job: %v", label, err)
	}
	f.importJobID = importJob.Msg.GetJob().GetId()

	export, err := s.ExportAccounts(f.ctx, connect.NewRequest(&crmv1.ExportAccountsRequest{}))
	if err != nil {
		t.Fatalf("[%s] seed export job: %v", label, err)
	}
	f.exportJobID = export.Msg.GetJob().GetId()

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
	"SyncGoogleMailbox": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// B syncs its own mailbox. The stub Gmail answers nothing, so the
		// assertion is not about what gets indexed — it is that a pass run as
		// B cannot touch A's sync state or A's contacts' timelines.
		_, _ = s.SyncGoogleMailbox(ctxB, connect.NewRequest(&crmv1.SyncGoogleMailboxRequest{}))

		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		if _, err := s.store.GetGmailSyncState(a.ctx, victimScope, a.googleOwner); err == nil {
			t.Error("a sync pass run by tenant B created sync state in tenant A")
		}
		entries, err := s.store.ListGmailMessagesForSubject(a.ctx, victimScope,
			domain.SubjectContact, a.contactID, 0)
		if err != nil {
			t.Fatalf("read A's synced messages: %v", err)
		}
		// A has exactly the one the fixture seeded; B's pass added nothing.
		if len(entries) != 1 {
			t.Errorf("a sync pass run by tenant B changed tenant A's timeline: %d entries, want 1",
				len(entries))
		}
	},
	// Erasure is the most destructive RPC this service has: one call deletes
	// a customer's correspondence history. A cross-tenant one would let an
	// attacker destroy another organization's records.
	"EraseSyncedContentForContact": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.EraseSyncedContentForContact(ctxB, connect.NewRequest(
			&crmv1.EraseSyncedContentForContactRequest{ContactId: a.contactID}))
		wantNotFound(t, "EraseSyncedContentForContact", err)

		// A's synced correspondence survived the attempt.
		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		entries, err := s.store.ListGmailMessagesForSubject(a.ctx, victimScope,
			domain.SubjectContact, a.contactID, 0)
		if err != nil {
			t.Fatalf("read A's synced messages: %v", err)
		}
		if len(entries) != 1 || !entries[0].ErasedAt.IsZero() {
			t.Errorf("a cross-tenant erasure redacted tenant A's records: %+v", entries)
		}
	},
	"SuppressSyncAddress": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		if _, err := s.SuppressSyncAddress(ctxB, connect.NewRequest(
			&crmv1.SuppressSyncAddressRequest{Address: "shared@example.com"})); err != nil {
			t.Fatalf("suppress as B: %v", err)
		}
		// Suppressing in B must not stop A's sync from matching. A shared
		// suppression list would let one organization silence another's.
		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		suppressed, err := s.store.SuppressedSyncAddresses(a.ctx, victimScope)
		if err != nil {
			t.Fatalf("read A's suppressions: %v", err)
		}
		if suppressed["shared@example.com"] {
			t.Error("a suppression added by tenant B applies to tenant A")
		}
	},
	"ListSyncSuppressions": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		if err := s.store.SuppressSyncAddress(a.ctx, victimScope, &domain.SyncSuppression{
			Address: "private-request@victim.example", Reason: "erasure request",
		}); err != nil {
			t.Fatalf("seed A's suppression: %v", err)
		}
		resp, err := s.ListSyncSuppressions(ctxB, connect.NewRequest(
			&crmv1.ListSyncSuppressionsRequest{}))
		if err != nil {
			t.Fatalf("list as B: %v", err)
		}
		for _, row := range resp.Msg.GetSuppressions() {
			if row.GetAddress() == "private-request@victim.example" {
				t.Error("tenant B read the address of somebody who made an erasure request in tenant A")
			}
		}
	},
	"ReleaseSyncAddress": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		if err := s.store.SuppressSyncAddress(a.ctx, victimScope, &domain.SyncSuppression{
			Address: "do-not-track@victim.example",
		}); err != nil {
			t.Fatalf("seed A's suppression: %v", err)
		}
		// B must not be able to lift a suppression A recorded — that would
		// restart collection against somebody who asked it to stop.
		_, err := s.ReleaseSyncAddress(ctxB, connect.NewRequest(
			&crmv1.ReleaseSyncAddressRequest{Address: "do-not-track@victim.example"}))
		wantNotFound(t, "ReleaseSyncAddress", err)

		suppressed, err := s.store.SuppressedSyncAddresses(a.ctx, victimScope)
		if err != nil {
			t.Fatalf("read A's suppressions: %v", err)
		}
		if !suppressed["do-not-track@victim.example"] {
			t.Error("tenant B lifted a suppression belonging to tenant A")
		}
	},
	"SyncGoogleCalendar": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// As with SyncGoogleMailbox: the stub answers nothing, so what is
		// asserted is that a pass run as B leaves no trace in A.
		_, _ = s.SyncGoogleCalendar(ctxB, connect.NewRequest(&crmv1.SyncGoogleCalendarRequest{}))

		victimScope := domain.Scope{TenantID: "tenant-victim", OrgID: "org-victim"}
		if _, err := s.store.GetCalendarSyncState(a.ctx, victimScope, a.googleOwner); err == nil {
			t.Error("a calendar pass run by tenant B created sync state in tenant A")
		}
		events, err := s.store.ListCalendarEventsForSubject(a.ctx, victimScope,
			domain.SubjectContact, a.contactID, 0)
		if err != nil {
			t.Fatalf("read A's meetings: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("a calendar pass run by tenant B wrote %d meetings onto tenant A's timeline", len(events))
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
		// lead and pipeline its own probes create. Activity is two: one
		// logged directly and one the fixture's synced message produced.
		// What matters is that the counts never include A's rows.
		want := map[string]int64{"account": 1, "contact": 1, "opportunity": 1, "activity": 2}
		for entity, expected := range want {
			if got := res.Msg.GetCounts()[entity]; got != expected {
				t.Errorf("GetSummary counts %d %s rows for organization B; want %d — "+
					"the dashboard count spans organizations", got, entity, expected)
			}
		}
	},
	// GetDashboardReport composes pipelines, opportunities, tasks and leads
	// (internal/api/reporting.go): each read is already tenant-scoped the
	// same way ListPipelines/ListOpportunities/etc. are on their own, but
	// the report reshapes rows into aggregates rather than returning them
	// verbatim, so a leak here would be easy to miss by only checking list
	// RPCs. The pipeline identity and the forecast's per-owner bucket are
	// the two places A's seeded data (its own pipeline, and its
	// opportunity's owner-A account owner) would surface if the tenant
	// filter were dropped anywhere in the composition.
	"GetDashboardReport": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.GetDashboardReport(ctxB, connect.NewRequest(&crmv1.GetDashboardReportRequest{}))
		if err != nil {
			t.Fatalf("GetDashboardReport: %v", err)
		}
		report := res.Msg.GetReport()
		for _, p := range report.GetPipelines() {
			if p.GetPipelineId() == a.pipelineID {
				t.Errorf("GetDashboardReport pipelines included organization A's pipeline %q", a.pipelineID)
			}
		}
		for _, b := range report.GetForecast().GetBuckets() {
			if b.GetOwnerId() == "owner-victim" {
				t.Errorf("GetDashboardReport forecast leaked organization A's account owner %q", b.GetOwnerId())
			}
		}
		if _, leaked := report.GetForecast().GetByOwner()["owner-victim"]; leaked {
			t.Error("GetDashboardReport forecast.by_owner leaked organization A's account owner")
		}
	},
	// Workflow automation. Both fixtures seed an active rule, a version, an
	// execution and a notification, so each of these runs against a tenant
	// that really does have automation to leak.
	//
	// The notification probes are sharper than they look: both fixtures share
	// a subject ("test-user", from ctxAs), and neither RPC takes a recipient,
	// so only the tenant separates B's inbox from A's.
	"GetWorkflowRule": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetWorkflowRule(ctxB, connect.NewRequest(
			&crmv1.GetWorkflowRuleRequest{Id: a.workflowRuleID}))
		wantNotFound(t, "GetWorkflowRule", err)
	},
	"ListWorkflowRules": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		for _, includeArchived := range []bool{false, true} {
			res, err := s.ListWorkflowRules(ctxB, connect.NewRequest(
				&crmv1.ListWorkflowRulesRequest{IncludeArchived: includeArchived}))
			if err != nil {
				t.Fatalf("ListWorkflowRules(archived=%v): %v", includeArchived, err)
			}
			for _, rule := range res.Msg.GetRules() {
				if rule.GetId() == a.workflowRuleID {
					t.Errorf("ListWorkflowRules(archived=%v) returned organization A's rule",
						includeArchived)
				}
			}
		}
	},
	"CreateWorkflowRule": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// A rule takes no foreign id, so what is checked is the other
		// direction: creating one in B must not appear in A's list.
		created, err := s.CreateWorkflowRule(ctxB, connect.NewRequest(
			&crmv1.CreateWorkflowRuleRequest{Name: "B's rule"}))
		if err != nil {
			t.Fatalf("CreateWorkflowRule as another tenant: %v", err)
		}
		res, err := s.ListWorkflowRules(a.ctx, connect.NewRequest(&crmv1.ListWorkflowRulesRequest{}))
		if err != nil {
			t.Fatalf("ListWorkflowRules as the victim: %v", err)
		}
		for _, rule := range res.Msg.GetRules() {
			if rule.GetId() == created.Msg.GetRule().GetId() {
				t.Error("CreateWorkflowRule: organization B's rule landed in organization A")
			}
		}
	},
	"UpdateWorkflowRule": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateWorkflowRule(ctxB, connect.NewRequest(&crmv1.UpdateWorkflowRuleRequest{
			Id: a.workflowRuleID, Name: "Seized",
		}))
		wantNotFound(t, "UpdateWorkflowRule", err)
	},
	"SetWorkflowRuleStatus": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Pausing somebody else's automation is a denial of service that
		// leaves no trace in their records.
		_, err := s.SetWorkflowRuleStatus(ctxB, connect.NewRequest(
			&crmv1.SetWorkflowRuleStatusRequest{Id: a.workflowRuleID, Status: domain.WorkflowPaused}))
		wantNotFound(t, "SetWorkflowRuleStatus", err)
	},
	"PublishWorkflowRuleVersion": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Publishing into A's rule would make B the author of what runs
		// against A's records.
		_, err := s.PublishWorkflowRuleVersion(ctxB, connect.NewRequest(
			&crmv1.PublishWorkflowRuleVersionRequest{
				RuleId:           a.workflowRuleID,
				TriggerEventType: domain.EventLeadCreated,
				Actions: []*crmv1.WorkflowAction{{
					Type: domain.ActionCreateTask, Title: "Seized",
					Assignee: domain.AssigneeFixedUser, AssigneeId: "attacker",
				}},
				Activate: true,
			}))
		wantNotFound(t, "PublishWorkflowRuleVersion", err)
	},
	"ListWorkflowRuleVersions": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListWorkflowRuleVersions(ctxB, connect.NewRequest(
			&crmv1.ListWorkflowRuleVersionsRequest{RuleId: a.workflowRuleID}))
		if err != nil {
			wantNotFound(t, "ListWorkflowRuleVersions", err)
			return
		}
		for _, version := range res.Msg.GetVersions() {
			if version.GetId() == a.workflowVersionID {
				t.Error("ListWorkflowRuleVersions returned organization A's rule text")
			}
		}
	},
	"ListWorkflowExecutions": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListWorkflowExecutions(ctxB, connect.NewRequest(
			&crmv1.ListWorkflowExecutionsRequest{RuleId: a.workflowRuleID}))
		if err != nil {
			wantNotFound(t, "ListWorkflowExecutions", err)
			return
		}
		for _, execution := range res.Msg.GetExecutions() {
			if execution.GetRuleId() == a.workflowRuleID {
				t.Error("ListWorkflowExecutions returned organization A's ledger")
			}
		}
	},
	"ListNotifications": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		for _, unreadOnly := range []bool{false, true} {
			res, err := s.ListNotifications(ctxB, connect.NewRequest(
				&crmv1.ListNotificationsRequest{UnreadOnly: unreadOnly}))
			if err != nil {
				t.Fatalf("ListNotifications(unread=%v): %v", unreadOnly, err)
			}
			for _, row := range res.Msg.GetNotifications() {
				if row.GetId() == a.notificationID {
					t.Errorf("ListNotifications(unread=%v) returned organization A's notification",
						unreadOnly)
				}
			}
		}
	},
	"MarkNotificationRead": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.MarkNotificationRead(ctxB, connect.NewRequest(
			&crmv1.MarkNotificationReadRequest{Id: a.notificationID}))
		wantNotFound(t, "MarkNotificationRead", err)
	},
	"CreateTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// A tag has no cross-tenant reference to abuse at creation; the
		// assertion is that B may reuse organization A's tag name (names are
		// unique per tenant, not globally) and gets its own id.
		res, err := s.CreateTag(ctxB, connect.NewRequest(&crmv1.CreateTagRequest{Name: "victim confidential tag"}))
		if err != nil {
			t.Fatalf("CreateTag: %v", err)
		}
		if res.Msg.GetTag().GetId() == a.tagID {
			t.Error("CreateTag: organization B was handed organization A's tag id")
		}
	},
	"ListTags": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListTags(ctxB, connect.NewRequest(&crmv1.ListTagsRequest{}))
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		for _, row := range res.Msg.GetTags() {
			if row.GetId() == a.tagID {
				t.Error("ListTags returned organization A's tag")
			}
		}
	},
	"UpdateTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateTag(ctxB, connect.NewRequest(&crmv1.UpdateTagRequest{Id: a.tagID, Name: "seized"}))
		wantNotFound(t, "UpdateTag", err)
	},
	"DeleteTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteTag(ctxB, connect.NewRequest(&crmv1.DeleteTagRequest{Id: a.tagID}))
		wantNotFound(t, "DeleteTag", err)
	},
	"AssignTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Assigning organization A's tag id to anything — even B's own
		// account — must fail: the tag does not exist in B's tenant.
		_, err := s.AssignTag(ctxB, connect.NewRequest(&crmv1.AssignTagRequest{
			TagId: a.tagID, ObjectType: "account", ObjectId: "attacker-account",
		}))
		if err == nil {
			t.Error("AssignTag: organization B assigned organization A's tag")
		}
	},
	"RemoveTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// RemoveTag is idempotent by design, so this must not error — but it
		// must not touch organization A's assignment either.
		if _, err := s.RemoveTag(ctxB, connect.NewRequest(&crmv1.RemoveTagRequest{
			TagId: a.tagID, ObjectType: "account", ObjectId: a.accountID,
		})); err != nil {
			t.Fatalf("RemoveTag: %v", err)
		}
		tags, err := s.ListTagsForRecord(a.ctx, connect.NewRequest(&crmv1.ListTagsForRecordRequest{
			ObjectType: "account", ObjectId: a.accountID,
		}))
		if err != nil {
			t.Fatalf("verify victim's tags survived: %v", err)
		}
		found := false
		for _, tag := range tags.Msg.GetTags() {
			if tag.GetId() == a.tagID {
				found = true
			}
		}
		if !found {
			t.Error("RemoveTag: organization B removed organization A's tag assignment")
		}
	},
	"ListTagsForRecord": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListTagsForRecord(ctxB, connect.NewRequest(&crmv1.ListTagsForRecordRequest{
			ObjectType: "account", ObjectId: a.accountID,
		}))
		if err != nil {
			t.Fatalf("ListTagsForRecord: %v", err)
		}
		if len(res.Msg.GetTags()) != 0 {
			t.Error("ListTagsForRecord returned organization A's tags for a foreign account id")
		}
	},
	"CreateImportJob": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// A fresh job created by B does not name organization A at all;
		// the assertion is that B's own upload lands only in B and stays
		// invisible to A, mirroring "CreateLead"'s own probe shape.
		res, err := s.CreateImportJob(ctxB, connect.NewRequest(&crmv1.CreateImportJobRequest{
			ObjectType: "account", FileName: "attacker.csv", Content: "Company\nAttacker Co\n",
			Mapping: map[string]string{"Company": "name"},
		}))
		if err != nil {
			t.Fatalf("CreateImportJob: %v", err)
		}
		if res.Msg.GetJob().GetId() == a.importJobID {
			t.Error("CreateImportJob: organization B was handed organization A's import job id")
		}
	},
	"GetImportJob": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.GetImportJob(ctxB, connect.NewRequest(&crmv1.GetImportJobRequest{Id: a.importJobID}))
		wantNotFound(t, "GetImportJob", err)
	},
	"ListImportJobs": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListImportJobs(ctxB, connect.NewRequest(&crmv1.ListImportJobsRequest{ObjectType: "account"}))
		if err != nil {
			t.Fatalf("ListImportJobs: %v", err)
		}
		for _, job := range res.Msg.GetJobs() {
			if job.GetId() == a.importJobID {
				t.Error("ListImportJobs returned organization A's import job")
			}
		}
	},
	"CommitImportJob": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.CommitImportJob(ctxB, connect.NewRequest(&crmv1.CommitImportJobRequest{Id: a.importJobID}))
		if err == nil {
			t.Errorf("CommitImportJob: organization B committed organization A's import job: %+v", res.Msg)
		}
	},
	"ExportAccounts": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ExportAccounts(ctxB, connect.NewRequest(&crmv1.ExportAccountsRequest{}))
		if err != nil {
			t.Fatalf("ExportAccounts: %v", err)
		}
		if strings.Contains(res.Msg.GetContent(), a.accountID) {
			t.Error("ExportAccounts: organization B's export contains organization A's account id")
		}
	},
	"ListExportJobs": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListExportJobs(ctxB, connect.NewRequest(&crmv1.ListExportJobsRequest{ObjectType: "account"}))
		if err != nil {
			t.Fatalf("ListExportJobs: %v", err)
		}
		for _, job := range res.Msg.GetJobs() {
			if job.GetId() == a.exportJobID {
				t.Error("ListExportJobs returned organization A's export audit row")
			}
		}
	},
	"MergeAccounts": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Organization B's own account as the "survivor" and organization
		// A's as the "duplicate" — the write-side leak: it would delete A's
		// account and fold it into B's.
		res, err := s.MergeAccounts(ctxB, connect.NewRequest(&crmv1.MergeAccountsRequest{
			SurvivorId: "attacker-account", DuplicateId: a.accountID,
		}))
		if err == nil {
			t.Errorf("MergeAccounts: organization B merged organization A's account: %+v", res.Msg)
		}
		// The reverse direction too: A's account as "survivor" must not let
		// B delete one of its own accounts into it.
		if _, err := s.MergeAccounts(ctxB, connect.NewRequest(&crmv1.MergeAccountsRequest{
			SurvivorId: a.accountID, DuplicateId: "attacker-account",
		})); err == nil {
			t.Error("MergeAccounts: organization B merged into organization A's account")
		}
	},
	"ListAccountMerges": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListAccountMerges(ctxB, connect.NewRequest(&crmv1.ListAccountMergesRequest{AccountId: a.accountID}))
		if err != nil {
			t.Fatalf("ListAccountMerges: %v", err)
		}
		if len(res.Msg.GetMerges()) != 0 {
			t.Error("ListAccountMerges returned organization A's merge audit trail for a foreign account id")
		}
	},
	"CreateSavedView": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Both fixtures authenticate as the same subject ("test-user" —
		// ctxAs, crm_test.go) in different tenants, so this proves the
		// isolation holds on tenant, not owner: reusing organization A's
		// view name in organization B must succeed and mint a distinct id,
		// never collide with or overwrite A's row.
		res, err := s.CreateSavedView(ctxB, connect.NewRequest(&crmv1.CreateSavedViewRequest{
			ObjectType: "account", Name: "victim confidential view",
		}))
		if err != nil {
			t.Fatalf("CreateSavedView: %v", err)
		}
		if res.Msg.GetView().GetId() == a.savedViewID {
			t.Error("CreateSavedView: organization B was handed organization A's saved view id")
		}
	},
	"ListSavedViews": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListSavedViews(ctxB, connect.NewRequest(&crmv1.ListSavedViewsRequest{}))
		if err != nil {
			t.Fatalf("ListSavedViews: %v", err)
		}
		for _, view := range res.Msg.GetViews() {
			if view.GetId() == a.savedViewID {
				t.Error("ListSavedViews returned organization A's saved view")
			}
		}
	},
	"UpdateSavedView": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateSavedView(ctxB, connect.NewRequest(&crmv1.UpdateSavedViewRequest{
			Id: a.savedViewID, Name: "seized",
		}))
		wantNotFound(t, "UpdateSavedView", err)
	},
	"DeleteSavedView": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteSavedView(ctxB, connect.NewRequest(&crmv1.DeleteSavedViewRequest{Id: a.savedViewID}))
		wantNotFound(t, "DeleteSavedView", err)
	},
	"CreateMessageTemplate": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Names are unique per channel per tenant, so reusing organization A's
		// name in organization B must succeed with a distinct id rather than
		// colliding with — or overwriting — A's wording.
		res, err := s.CreateMessageTemplate(ctxB, connect.NewRequest(&crmv1.CreateMessageTemplateRequest{
			Name: "victim confidential opener", ChannelKind: "whatsapp", Body: "Hello",
		}))
		if err != nil {
			t.Fatalf("CreateMessageTemplate: %v", err)
		}
		if res.Msg.GetTemplate().GetId() == a.templateID {
			t.Error("CreateMessageTemplate handed organization B organization A's template id")
		}
	},
	"ListMessageTemplates": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListMessageTemplates(ctxB, connect.NewRequest(&crmv1.ListMessageTemplatesRequest{}))
		if err != nil {
			t.Fatalf("ListMessageTemplates: %v", err)
		}
		for _, template := range res.Msg.GetTemplates() {
			if template.GetId() == a.templateID {
				t.Error("ListMessageTemplates returned organization A's wording")
			}
		}
	},
	"UpdateMessageTemplate": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.UpdateMessageTemplate(ctxB, connect.NewRequest(&crmv1.UpdateMessageTemplateRequest{
			Id: a.templateID, Name: "seized", ChannelKind: "whatsapp", Body: "Rewritten",
		}))
		wantNotFound(t, "UpdateMessageTemplate", err)
	},
	"DeleteMessageTemplate": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		_, err := s.DeleteMessageTemplate(ctxB, connect.NewRequest(&crmv1.DeleteMessageTemplateRequest{
			Id: a.templateID,
		}))
		wantNotFound(t, "DeleteMessageTemplate", err)
	},
	"DescribeMessageTemplateCatalog": func(t *testing.T, s *CRMService, ctxB context.Context, _ apiFixture) {
		// Two static allowlists, identical for every tenant — there is nothing
		// here to leak. The probe exists so the sweep stays total: an RPC with
		// no entry is a decision nobody made, and this one is a decision.
		if _, err := s.DescribeMessageTemplateCatalog(ctxB,
			connect.NewRequest(&crmv1.DescribeMessageTemplateCatalogRequest{})); err != nil {
			t.Fatalf("DescribeMessageTemplateCatalog: %v", err)
		}
	},
	"ListContactChannels": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Naming organization A's contact id must read as absent, not as
		// forbidden: a refusal that distinguishes the two confirms the id is
		// real. Getting channels back would additionally hand over a phone
		// number and email address.
		_, err := s.ListContactChannels(ctxB, connect.NewRequest(&crmv1.ListContactChannelsRequest{
			ContactId: a.contactID,
		}))
		wantNotFound(t, "ListContactChannels", err)
	},
	"ListMessagingConsent": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// Consent is keyed on an address rather than on a record id, so
		// there is no foreign id to be refused — organization B simply asks
		// about the same address. It must see its own ledger, which is
		// empty for this address, not organization A's withdrawal.
		res, err := s.ListMessagingConsent(ctxB, connect.NewRequest(&crmv1.ListMessagingConsentRequest{
			ChannelKind: "email", Address: a.contactEmail,
		}))
		if err != nil {
			t.Fatalf("ListMessagingConsent: %v", err)
		}
		for _, row := range res.Msg.GetConsent() {
			if row.GetId() == a.consentID {
				t.Error("ListMessagingConsent returned organization A's withdrawal for the same address")
			}
		}
	},
	"RecordMessagingConsent": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		// The write direction of the same property. Organization B records
		// its own grant for an address organization A withdrew; the two must
		// not collide, and A's withdrawal must still stand afterwards —
		// otherwise one tenant could re-permit contact with somebody another
		// tenant was told to stop contacting.
		res, err := s.RecordMessagingConsent(ctxB, connect.NewRequest(&crmv1.RecordMessagingConsentRequest{
			ChannelKind: "email", Address: a.contactEmail, State: "granted", Source: "manual",
		}))
		if err != nil {
			t.Fatalf("RecordMessagingConsent: %v", err)
		}
		if res.Msg.GetConsent().GetState() != "granted" {
			t.Errorf("organization B recorded %q, want its own grant", res.Msg.GetConsent().GetState())
		}
		rows, err := s.ListMessagingConsent(a.ctx, connect.NewRequest(&crmv1.ListMessagingConsentRequest{
			ChannelKind: "email", Address: a.contactEmail,
		}))
		if err != nil {
			t.Fatalf("ListMessagingConsent for organization A: %v", err)
		}
		if len(rows.Msg.GetConsent()) != 1 ||
			rows.Msg.GetConsent()[0].GetId() != a.consentID ||
			rows.Msg.GetConsent()[0].GetState() != "withdrawn" {
			t.Error("organization B's grant reached organization A's ledger; a withdrawal must not be " +
				"overturnable by another tenant")
		}
	},
	"ListTaggedRecords": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.ListTaggedRecords(ctxB, connect.NewRequest(&crmv1.ListTaggedRecordsRequest{TagId: a.tagID}))
		if err != nil {
			t.Fatalf("ListTaggedRecords: %v", err)
		}
		if len(res.Msg.GetRecords()) != 0 {
			t.Error("ListTaggedRecords returned organization A's tag assignments for a foreign tag id")
		}
	},
	"BulkAssignTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkAssignTag(ctxB, connect.NewRequest(&crmv1.BulkAssignTagRequest{
			TagId: a.tagID, ObjectType: "account", ObjectIds: []string{a.accountID},
		}))
		if err != nil {
			t.Fatalf("BulkAssignTag: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkAssignTag: organization B assigned organization A's tag to organization A's account")
			}
		}
	},
	"BulkRemoveTag": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		if _, err := s.BulkRemoveTag(ctxB, connect.NewRequest(&crmv1.BulkRemoveTagRequest{
			TagId: a.tagID, ObjectType: "account", ObjectIds: []string{a.accountID},
		})); err != nil {
			t.Fatalf("BulkRemoveTag: %v", err)
		}
		tags, err := s.ListTagsForRecord(a.ctx, connect.NewRequest(&crmv1.ListTagsForRecordRequest{
			ObjectType: "account", ObjectId: a.accountID,
		}))
		if err != nil {
			t.Fatalf("verify victim's tags survived: %v", err)
		}
		found := false
		for _, tag := range tags.Msg.GetTags() {
			if tag.GetId() == a.tagID {
				found = true
			}
		}
		if !found {
			t.Error("BulkRemoveTag: organization B removed organization A's tag assignment")
		}
	},
	"BulkCompleteTasks": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkCompleteTasks(ctxB, connect.NewRequest(&crmv1.BulkCompleteTasksRequest{Ids: []string{a.taskID}}))
		if err != nil {
			t.Fatalf("BulkCompleteTasks: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkCompleteTasks: organization B completed organization A's task")
			}
		}
		task, err := s.GetTask(a.ctx, connect.NewRequest(&crmv1.GetTaskRequest{Id: a.taskID}))
		if err != nil {
			t.Fatalf("verify victim's task survived: %v", err)
		}
		if task.Msg.GetTask().GetStatus() == "completed" {
			t.Error("BulkCompleteTasks: organization A's task was completed by organization B's request")
		}
	},
	"BulkReopenTasks": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkReopenTasks(ctxB, connect.NewRequest(&crmv1.BulkReopenTasksRequest{Ids: []string{a.taskID}}))
		if err != nil {
			t.Fatalf("BulkReopenTasks: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkReopenTasks: organization B reopened organization A's task")
			}
		}
	},
	"BulkReassignTaskAssignee": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkReassignTaskAssignee(ctxB, connect.NewRequest(&crmv1.BulkReassignTaskAssigneeRequest{
			Ids: []string{a.taskID}, NewAssigneeId: "attacker",
		}))
		if err != nil {
			t.Fatalf("BulkReassignTaskAssignee: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkReassignTaskAssignee: organization B reassigned organization A's task")
			}
		}
		task, err := s.GetTask(a.ctx, connect.NewRequest(&crmv1.GetTaskRequest{Id: a.taskID}))
		if err != nil {
			t.Fatalf("verify victim's task survived: %v", err)
		}
		if task.Msg.GetTask().GetAssigneeId() == "attacker" {
			t.Error("BulkReassignTaskAssignee: organization A's task was reassigned by organization B's request")
		}
	},
	"BulkReassignAccountOwner": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkReassignAccountOwner(ctxB, connect.NewRequest(&crmv1.BulkReassignAccountOwnerRequest{
			Ids: []string{a.accountID}, NewOwnerId: "attacker",
		}))
		if err != nil {
			t.Fatalf("BulkReassignAccountOwner: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkReassignAccountOwner: organization B reassigned organization A's account")
			}
		}
		account, err := s.GetAccount(a.ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: a.accountID}))
		if err != nil {
			t.Fatalf("verify victim's account survived: %v", err)
		}
		if account.Msg.GetAccount().GetOwnerId() == "attacker" {
			t.Error("BulkReassignAccountOwner: organization A's account was reassigned by organization B's request")
		}
	},
	"BulkReassignLeadOwner": func(t *testing.T, s *CRMService, ctxB context.Context, a apiFixture) {
		res, err := s.BulkReassignLeadOwner(ctxB, connect.NewRequest(&crmv1.BulkReassignLeadOwnerRequest{
			Ids: []string{a.leadID}, NewOwnerId: "attacker",
		}))
		if err != nil {
			t.Fatalf("BulkReassignLeadOwner: %v", err)
		}
		for _, r := range res.Msg.GetResults() {
			if r.GetSuccess() {
				t.Error("BulkReassignLeadOwner: organization B reassigned organization A's lead")
			}
		}
		lead, err := s.GetLead(a.ctx, connect.NewRequest(&crmv1.GetLeadRequest{Id: a.leadID}))
		if err != nil {
			t.Fatalf("verify victim's lead survived: %v", err)
		}
		if lead.Msg.GetLead().GetOwnerId() == "attacker" {
			t.Error("BulkReassignLeadOwner: organization A's lead was reassigned by organization B's request")
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
	rpcsWithoutTenantData := map[string]string{
		"DescribeWorkflowCatalog": "Returns the authoring allowlists — triggers, comparable " +
			"fields, operators, action types — which are compiled into the binary and " +
			"identical for every organization. It reads no store at all.",
	}

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
