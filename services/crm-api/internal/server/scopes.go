package server

import (
	"fmt"
	"sort"

	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	platformv1 "github.com/santoshguru/crm/gen/platform/v1"
	"github.com/santoshguru/crm/internal/auth"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The permission catalog: every procedure this server exposes, and the scope
// a caller must hold to reach it (task.md §1.4).
//
// Two rules make this a catalog rather than a list of exceptions:
//
//  1. Deny by default. auth.ScopePolicy refuses any procedure it has no entry
//     for, so a handler added without a decision here is unreachable rather
//     than public. The old map failed the other way.
//  2. Totality is checked, not trusted. assertPolicyIsTotal walks the
//     generated service descriptors — the same source connect-go registers
//     its routes from — and fails if a procedure has no decision, or has two.
//     It runs at startup, not only in tests, because a policy hole should stop
//     a deployment rather than wait for someone to run the suite.
//
// Scopes are coarse: one read and one write scope per entity. Anything finer
// (this user may edit only accounts they own) is record-level authorization,
// which a scope in a token cannot express — see the FGA items in task.md §1.4.

// crmScopes maps each CRM procedure to its required scope.
var crmScopes = map[string]string{
	"/crm.v1.CRMService/CreateAccount":               "crm:account:write",
	"/crm.v1.CRMService/GetAccount":                  "crm:account:read",
	"/crm.v1.CRMService/ListAccounts":                "crm:account:read",
	"/crm.v1.CRMService/UpdateAccount":               "crm:account:write",
	"/crm.v1.CRMService/DeleteAccount":               "crm:account:write",
	"/crm.v1.CRMService/CreateContact":               "crm:contact:write",
	"/crm.v1.CRMService/GetContact":                  "crm:contact:read",
	"/crm.v1.CRMService/ListContacts":                "crm:contact:read",
	"/crm.v1.CRMService/UpdateContact":               "crm:contact:write",
	"/crm.v1.CRMService/DeleteContact":               "crm:contact:write",
	"/crm.v1.CRMService/CreateLead":                  "crm:lead:write",
	"/crm.v1.CRMService/GetLead":                     "crm:lead:read",
	"/crm.v1.CRMService/ListLeads":                   "crm:lead:read",
	"/crm.v1.CRMService/UpdateLead":                  "crm:lead:write",
	"/crm.v1.CRMService/DeleteLead":                  "crm:lead:write",
	"/crm.v1.CRMService/ConvertLead":                 "crm:lead:write",
	"/crm.v1.CRMService/CreateOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/GetOpportunity":              "crm:opportunity:read",
	"/crm.v1.CRMService/ListOpportunities":           "crm:opportunity:read",
	"/crm.v1.CRMService/UpdateOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/MoveOpportunityToPipeline":   "crm:opportunity:write",
	"/crm.v1.CRMService/DeleteOpportunity":           "crm:opportunity:write",
	"/crm.v1.CRMService/ListOpportunityStageHistory": "crm:opportunity:read",
	"/crm.v1.CRMService/CreatePipeline":              "crm:pipeline:write",
	"/crm.v1.CRMService/GetPipeline":                 "crm:pipeline:read",
	"/crm.v1.CRMService/ListPipelines":               "crm:pipeline:read",
	"/crm.v1.CRMService/UpdatePipeline":              "crm:pipeline:write",
	"/crm.v1.CRMService/CreatePipelineStage":         "crm:pipeline:write",
	"/crm.v1.CRMService/ListPipelineStages":          "crm:pipeline:read",
	"/crm.v1.CRMService/UpdatePipelineStage":         "crm:pipeline:write",
	"/crm.v1.CRMService/CreateActivity":              "crm:activity:write",
	"/crm.v1.CRMService/GetActivity":                 "crm:activity:read",
	"/crm.v1.CRMService/ListActivities":              "crm:activity:read",
	"/crm.v1.CRMService/UpdateActivity":              "crm:activity:write",
	"/crm.v1.CRMService/DeleteActivity":              "crm:activity:write",
	"/crm.v1.CRMService/ListActivitiesForSubject":    "crm:activity:read",
	"/crm.v1.CRMService/GetSummary":                  "crm:summary:read",
	"/crm.v1.CRMService/SearchRecords":               "crm:search:read",

	// The Google connection. Starting and disconnecting are one write scope
	// because both act only on the caller's own mailbox link — the RPCs take
	// no owner at all, so the scope is about whether this user may connect a
	// Google account, not whose they may touch.
	//
	// ListGoogleConnections reads a different entity from the other three:
	// they act on the caller's own connection, it enumerates everyone's. That
	// is a real difference in authority — collapsing the two would mean any
	// user who can check their own status can also enumerate every
	// colleague's linked mailbox address — so it gets its own read scope
	// rather than sharing crm:integration:read.
	"/crm.v1.CRMService/StartGoogleConnection":      "crm:integration:write",
	"/crm.v1.CRMService/GetGoogleConnection":        "crm:integration:read",
	"/crm.v1.CRMService/DisconnectGoogleConnection": "crm:integration:write",
	"/crm.v1.CRMService/ListGoogleConnections":      "crm:integration_admin:read",
	// Synchronising writes CRM Activities, so it is a write even though it
	// reads a mailbox to decide what to write.
	"/crm.v1.CRMService/SyncGoogleMailbox":  "crm:integration:write",
	"/crm.v1.CRMService/SyncGoogleCalendar": "crm:integration:write",

	// Data-subject requests are administrative, not personal: they act on
	// behalf of somebody who is usually not a CRM user, so they need an
	// administrator rather than the ordinary integration scope. Reading the
	// suppression list is separated from changing it because the list names
	// people who asked not to be tracked — a fact worth knowing and a
	// decision worth restricting.
	"/crm.v1.CRMService/EraseSyncedContentForContact": "crm:integration_admin:write",
	"/crm.v1.CRMService/SuppressSyncAddress":          "crm:integration_admin:write",
	"/crm.v1.CRMService/ReleaseSyncAddress":           "crm:integration_admin:write",
	"/crm.v1.CRMService/ListSyncSuppressions":         "crm:integration_admin:read",

	// Tasks and Notes carry their own scopes rather than reusing
	// crm:activity:*. They are separate entities with separate reporting and
	// separate permission needs — a role that may read the immutable activity
	// log is not thereby a role that may reassign somebody's work.
	"/crm.v1.CRMService/CreateTask":                    "crm:task:write",
	"/crm.v1.CRMService/GetTask":                       "crm:task:read",
	"/crm.v1.CRMService/ListTasks":                     "crm:task:read",
	"/crm.v1.CRMService/UpdateTask":                    "crm:task:write",
	"/crm.v1.CRMService/DeleteTask":                    "crm:task:write",
	"/crm.v1.CRMService/CompleteTask":                  "crm:task:write",
	"/crm.v1.CRMService/ReopenTask":                    "crm:task:write",
	"/crm.v1.CRMService/CreateNote":                    "crm:note:write",
	"/crm.v1.CRMService/GetNote":                       "crm:note:read",
	"/crm.v1.CRMService/ListNotes":                     "crm:note:read",
	"/crm.v1.CRMService/UpdateNote":                    "crm:note:write",
	"/crm.v1.CRMService/DeleteNote":                    "crm:note:write",
	"/crm.v1.CRMService/CreateCustomFieldDefinition":   "crm:custom_field:write",
	"/crm.v1.CRMService/GetCustomFieldDefinition":      "crm:custom_field:read",
	"/crm.v1.CRMService/ListCustomFieldDefinitions":    "crm:custom_field:read",
	"/crm.v1.CRMService/UpdateCustomFieldDefinition":   "crm:custom_field:write",
	"/crm.v1.CRMService/ArchiveCustomFieldDefinition":  "crm:custom_field:write",
	"/crm.v1.CRMService/CreateCustomObjectDefinition":  "crm:custom_object:write",
	"/crm.v1.CRMService/GetCustomObjectDefinition":     "crm:custom_object:read",
	"/crm.v1.CRMService/ListCustomObjectDefinitions":   "crm:custom_object:read",
	"/crm.v1.CRMService/ArchiveCustomObjectDefinition": "crm:custom_object:write",
	"/crm.v1.CRMService/CreateCustomRecord":            "crm:custom_object:write",
	"/crm.v1.CRMService/GetCustomRecord":               "crm:custom_object:read",
	"/crm.v1.CRMService/ListCustomRecords":             "crm:custom_object:read",
	"/crm.v1.CRMService/UpdateCustomRecord":            "crm:custom_object:write",
	"/crm.v1.CRMService/DeleteCustomRecord":            "crm:custom_object:write",

	// Workflow automation. Authoring a rule is a privileged act that reading
	// the CRM is not: a rule acts on every record its trigger matches, on
	// behalf of whoever wrote it, indefinitely. So it gets its own pair
	// rather than sharing any entity's — a role that may edit opportunities
	// is not thereby a role that may make every new opportunity create work
	// for somebody.
	//
	// The catalog is a read even though it returns no tenant data: it
	// describes the authoring surface, and there is no one who needs it who
	// does not also need to read the rules.
	"/crm.v1.CRMService/DescribeWorkflowCatalog":    "crm:workflow:read",
	"/crm.v1.CRMService/CreateWorkflowRule":         "crm:workflow:write",
	"/crm.v1.CRMService/GetWorkflowRule":            "crm:workflow:read",
	"/crm.v1.CRMService/ListWorkflowRules":          "crm:workflow:read",
	"/crm.v1.CRMService/UpdateWorkflowRule":         "crm:workflow:write",
	"/crm.v1.CRMService/PublishWorkflowRuleVersion": "crm:workflow:write",
	"/crm.v1.CRMService/ListWorkflowRuleVersions":   "crm:workflow:read",
	"/crm.v1.CRMService/SetWorkflowRuleStatus":      "crm:workflow:write",
	"/crm.v1.CRMService/ListWorkflowExecutions":     "crm:workflow:read",

	// Notifications are the caller's own and nobody else's — neither RPC
	// takes a recipient — so the scope is about whether this principal
	// participates in the in-app inbox at all, and every CRM user does.
	"/crm.v1.CRMService/ListNotifications":    "crm:notification:read",
	"/crm.v1.CRMService/MarkNotificationRead": "crm:notification:write",

	// Tags. Defining/renaming/deleting a tag is administration of a
	// tenant-shared taxonomy, so it gets its own write scope rather than
	// riding on any one entity's — a role that may edit contacts is not
	// thereby a role that may rename a label every other user sees.
	// Assigning/removing a tag on a record and reading a record's tags need
	// only the read scope of tags themselves, since the record-level
	// authorization for the record itself is enforced by the record's own
	// scope on whichever RPC loaded it into view.
	"/crm.v1.CRMService/CreateTag":         "crm:tag:write",
	"/crm.v1.CRMService/ListTags":          "crm:tag:read",
	"/crm.v1.CRMService/UpdateTag":         "crm:tag:write",
	"/crm.v1.CRMService/DeleteTag":         "crm:tag:write",
	"/crm.v1.CRMService/AssignTag":         "crm:tag:write",
	"/crm.v1.CRMService/RemoveTag":         "crm:tag:write",
	"/crm.v1.CRMService/ListTagsForRecord": "crm:tag:read",
	"/crm.v1.CRMService/ListTaggedRecords": "crm:tag:read",

	// Bulk operations. Each carries the same scope its single-record
	// equivalent would — a bulk action is authorization-equivalent to
	// looping the single-record RPC, not a separate authority.
	"/crm.v1.CRMService/BulkAssignTag":            "crm:tag:write",
	"/crm.v1.CRMService/BulkRemoveTag":            "crm:tag:write",
	"/crm.v1.CRMService/BulkCompleteTasks":        "crm:task:write",
	"/crm.v1.CRMService/BulkReopenTasks":          "crm:task:write",
	"/crm.v1.CRMService/BulkReassignTaskAssignee": "crm:task:write",
	"/crm.v1.CRMService/BulkReassignAccountOwner": "crm:account:write",
	"/crm.v1.CRMService/BulkReassignLeadOwner":    "crm:lead:write",

	// Saved views. Every RPC scopes to the caller's own views by
	// construction (see internal/api/saved_views.go), so read and write here
	// mean "may this principal have saved views at all" rather than
	// authority over anyone else's — the same posture notifications' scopes
	// have. crm_viewer holds neither: see role_matrix_test.go's
	// TestViewerHoldsNoWriteAuthority for why that role holds no :write
	// scope of any kind, which as a side effect means a pure viewer cannot
	// save a view in this version — a real, named limitation rather than an
	// oversight.
	"/crm.v1.CRMService/CreateSavedView": "crm:saved_view:write",
	"/crm.v1.CRMService/ListSavedViews":  "crm:saved_view:read",
	"/crm.v1.CRMService/UpdateSavedView": "crm:saved_view:write",
	"/crm.v1.CRMService/DeleteSavedView": "crm:saved_view:write",

	// Account deduplication and merge. Both carry the account scopes rather
	// than a scope of their own: a merge is authorization-equivalent to an
	// UpdateAccount plus a DeleteAccount on the two accounts it touches.
	"/crm.v1.CRMService/MergeAccounts":     "crm:account:write",
	"/crm.v1.CRMService/ListAccountMerges": "crm:account:read",

	// CSV import and export, accounts only in v1. Every RPC reuses the
	// account scope pair rather than a scope of its own — see
	// docs/CSV_IMPORT_EXPORT.md's "Authorization" section: an import is
	// authorization-equivalent to calling CreateAccount once per row, and
	// an export to calling ListAccounts.
	"/crm.v1.CRMService/CreateImportJob": "crm:account:write",
	"/crm.v1.CRMService/GetImportJob":    "crm:account:read",
	"/crm.v1.CRMService/ListImportJobs":  "crm:account:read",
	"/crm.v1.CRMService/CommitImportJob": "crm:account:write",
	"/crm.v1.CRMService/ExportAccounts":  "crm:account:read",
	"/crm.v1.CRMService/ListExportJobs":  "crm:account:read",

	// GetDashboardReport reuses GetSummary's scope: both are read-only,
	// tenant-wide aggregate rollups over records the caller could otherwise
	// list one type at a time, not a new authorization surface.
	"/crm.v1.CRMService/GetDashboardReport": "crm:summary:read",

	// Messaging channels and consent (task_crm.md §2). Its own scope pair
	// rather than the contact's, because the authority differs in a way that
	// matters: seeing that somebody opted out is what stops a viewer wasting
	// a call on them, while recording that they opted *in* is a claim about a
	// third party's wishes that the organization may later have to defend.
	// Read is broad, write is not — see crm-roles.json.
	//
	// ListContactChannels demands the consent scope rather than
	// crm:contact:read even though it reads a Contact, because what it
	// actually returns is the gate's verdict; the address is incidental. A
	// caller who may read contacts but not consent still sees every contact
	// field through GetContact.
	"/crm.v1.CRMService/ListContactChannels":    "crm:consent:read",
	"/crm.v1.CRMService/ListMessagingConsent":   "crm:consent:read",
	"/crm.v1.CRMService/RecordMessagingConsent": "crm:consent:write",
}

// unscopedProcedures is the allowlist, and each value is the argument for
// waiving the scope check. Authentication still applies: these are open to any
// verified principal, not to the public internet.
//
// Nothing here returns tenant-owned data. A procedure that does belongs in
// crmScopes even if it feels innocuous — "it only returns a count" is how a
// tenant-scoped leak gets waved through.
var unscopedProcedures = map[string]string{
	"/platform.v1.ModuleService/GetManifest": "Static module identity, navigation and capability list. Identical for " +
		"every organization; the shell needs it before it knows which CRM scopes the user holds.",
	"/platform.v1.ModuleService/GetHealth": "Liveness of this module, with no tenant data in the response. " +
		"Gating it on a CRM scope would mean a user who cannot read accounts also cannot be told the service is down.",
	"/platform.v1.ModuleService/ListActions": "The Copilot-invocable action list, currently empty. When it starts " +
		"naming actions, each action's own scope is what governs invoking it; listing the catalog stays open.",
}

// scopePolicy is the wired policy.
var scopePolicy = auth.ScopePolicy{Required: crmScopes, Unscoped: unscopedProcedures}

// servedFiles are the proto files whose services this server registers. The
// totality check reads these rather than a hand-maintained list of procedures,
// so it cannot fall behind the generated code.
var servedFiles = []protoreflect.FileDescriptor{
	crmv1.File_crm_v1_crm_proto,
	platformv1.File_platform_v1_module_proto,
}

// servedProcedures lists every procedure the registered services expose, in
// connect-go's "/package.Service/Method" form.
func servedProcedures() []string {
	var out []string
	for _, file := range servedFiles {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			methods := service.Methods()
			for j := 0; j < methods.Len(); j++ {
				out = append(out, fmt.Sprintf("/%s/%s", service.FullName(), methods.Get(j).Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

// assertPolicyIsTotal reports every procedure the policy fails to decide, every
// procedure it decides twice, and every decision for a procedure that no longer
// exists. A non-nil error means the server must not start.
func assertPolicyIsTotal(policy auth.ScopePolicy) error {
	served := servedProcedures()
	isServed := make(map[string]bool, len(served))
	for _, procedure := range served {
		isServed[procedure] = true
	}

	var undecided, ambiguous, orphaned []string
	for _, procedure := range served {
		_, required := policy.Required[procedure]
		_, unscoped := policy.Unscoped[procedure]
		switch {
		case required && unscoped:
			ambiguous = append(ambiguous, procedure)
		case !required && !unscoped:
			undecided = append(undecided, procedure)
		}
	}
	for _, procedure := range policy.Procedures() {
		if !isServed[procedure] {
			orphaned = append(orphaned, procedure)
		}
	}

	if len(undecided) == 0 && len(ambiguous) == 0 && len(orphaned) == 0 {
		return nil
	}
	err := fmt.Errorf("scope policy is incomplete")
	if len(undecided) > 0 {
		err = fmt.Errorf("%w; no scope decision for %v (add the scope to crmScopes, "+
			"or record in unscopedProcedures why it needs none)", err, undecided)
	}
	if len(ambiguous) > 0 {
		err = fmt.Errorf("%w; %v are both scoped and unscoped", err, ambiguous)
	}
	if len(orphaned) > 0 {
		err = fmt.Errorf("%w; %v are no longer served and should be removed", err, orphaned)
	}
	return err
}
