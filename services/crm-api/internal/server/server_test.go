package server

import (
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/auth"
)

// The scope policy is checked against the SERVICE DESCRIPTORS, not against a
// list written by hand.
//
// A hand-written list inverts the point of this test. Adding an RPC and
// forgetting its policy entry, and forgetting to add it to the test, are the
// same act of forgetting — so a hardcoded list only catches someone who
// remembered the test but not the policy, which is not how the mistake happens.
// It caught nothing while looking like it caught everything.
//
// Deriving the list from the generated descriptors means a new RPC appears here
// the moment `make generate` runs, whether or not anyone remembered this file.
// servedProcedures (scopes.go) does that derivation, and the same function
// backs the startup check, so the test and the running server cannot disagree
// about which procedures exist.

// TestServedProceduresIsDerivedNotGuessed guards the derivation itself: a bug
// that returned nothing would make every check below pass vacuously.
func TestServedProceduresIsDerivedNotGuessed(t *testing.T) {
	served := servedProcedures()
	if len(served) == 0 {
		t.Fatal("no procedures derived from the service descriptors")
	}
	for _, want := range []string{
		"/crm.v1.CRMService/GetAccount",
		"/crm.v1.CRMService/GetSummary",
		"/platform.v1.ModuleService/GetHealth",
	} {
		found := false
		for _, procedure := range served {
			if procedure == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is served but not derived; the descriptor walk misses a service", want)
		}
	}
}

// TestScopePolicyDecidesEveryProcedure is the check that also runs at startup.
// A procedure with no decision is denied at request time, so this failing is
// not itself a hole — it is the hole being reported before deployment.
func TestScopePolicyDecidesEveryProcedure(t *testing.T) {
	if err := assertPolicyIsTotal(scopePolicy); err != nil {
		t.Error(err)
	}
}

// TestPolicyTotalityCatchesEachKindOfHole proves the previous test can fail.
// A completeness check that cannot be made to fail is decoration.
func TestPolicyTotalityCatchesEachKindOfHole(t *testing.T) {
	const real = "/crm.v1.CRMService/GetAccount"

	t.Run("undecided procedure", func(t *testing.T) {
		required := map[string]string{}
		for procedure, scope := range crmScopes {
			required[procedure] = scope
		}
		delete(required, real)
		err := assertPolicyIsTotal(auth.ScopePolicy{Required: required, Unscoped: unscopedProcedures})
		if err == nil {
			t.Fatal("a procedure with no scope decision was accepted")
		}
		if !strings.Contains(err.Error(), real) {
			t.Errorf("error does not name the undecided procedure: %v", err)
		}
	})

	t.Run("procedure decided twice", func(t *testing.T) {
		unscoped := map[string]string{real: "a mistake"}
		for procedure, why := range unscopedProcedures {
			unscoped[procedure] = why
		}
		if err := assertPolicyIsTotal(auth.ScopePolicy{Required: crmScopes, Unscoped: unscoped}); err == nil {
			t.Fatal("a procedure that is both scoped and unscoped was accepted")
		}
	})

	t.Run("entry for a procedure that does not exist", func(t *testing.T) {
		required := map[string]string{"/crm.v1.CRMService/GetAcccount": "crm:account:read"}
		for procedure, scope := range crmScopes {
			required[procedure] = scope
		}
		err := assertPolicyIsTotal(auth.ScopePolicy{Required: required, Unscoped: unscopedProcedures})
		if err == nil {
			t.Fatal("a typo'd procedure name was accepted")
		}
		// A typo is worse than a missing key: the entry looks present in
		// review, matches nothing at runtime, and leaves the real procedure
		// undecided. Here the real one is still present, so only the orphan
		// is reported — which is exactly the signal a reviewer needs.
		if !strings.Contains(err.Error(), "no longer served") {
			t.Errorf("error does not report the dead entry: %v", err)
		}
	})
}

// TestUnscopedProceduresAreArguedFor keeps the allowlist from becoming a
// dumping ground: every waiver carries the reason it is safe, and no CRM
// procedure may take one, because all of them read or write tenant data.
func TestUnscopedProceduresAreArguedFor(t *testing.T) {
	for procedure, why := range unscopedProcedures {
		if len(strings.TrimSpace(why)) < 40 {
			t.Errorf("%s is unscoped with no real justification (%q)", procedure, why)
		}
		if strings.HasPrefix(procedure, "/crm.v1.") {
			t.Errorf("%s waives its scope check, but every CRM procedure reads or "+
				"writes tenant-owned data", procedure)
		}
	}
}

// TestScopePolicySeparatesReadFromWrite fails if a mutation is guarded by a
// read scope. Reusing the read scope for a write lets anyone who may look at a
// record change it, which reads fine in a diff.
func TestScopePolicySeparatesReadFromWrite(t *testing.T) {
	for procedure, scope := range crmScopes {
		method := procedure[strings.LastIndex(procedure, "/")+1:]
		if isMutation(method) {
			if !strings.HasSuffix(scope, ":write") {
				t.Errorf("%s mutates but is guarded by %q; a write needs a :write scope",
					method, scope)
			}
			continue
		}
		if !strings.HasSuffix(scope, ":read") {
			t.Errorf("%s is a query but is guarded by %q", method, scope)
		}
	}
}

// isMutation reports whether a method name names a write.
//
// A name list rather than a proto annotation, because the Module Contract's
// proto is shared and frozen. If it ever grows a `mutating` option, read that
// instead and delete this.
//
// "Convert" joined the list for B1's ConvertLead: it creates a Contact, an
// Account and (optionally) an Opportunity and marks the Lead converted — a
// write by any definition, just not one spelled Create/Update/Delete/Upsert.
// "Complete" and "Reopen" joined it for Task's lifecycle transitions, which
// are writes that deliberately are not Update (task.md §5.1).
//
// "Erase", "Suppress" and "Release" joined it for data-subject requests:
// erasing deletes timeline entries, and the other two add and remove rows on
// the suppression list.
//
// "Publish", "Set" and "Mark" joined it for workflow automation and
// notifications: publishing writes an immutable rule version, SetWorkflowRule-
// Status moves a rule through its lifecycle, and MarkNotificationRead stamps
// a row. "Describe" deliberately did not — the catalog is static allowlists.
//
// "Start" and "Disconnect" joined it for the Google connection. Both read as
// navigation and are not: StartGoogleConnection writes a durable one-time
// authorization record before it returns a URL, and DisconnectGoogleConnection
// revokes a grant at Google and erases a stored credential. "Sync" joined it
// for the same reason: reading a mailbox is how it decides what to write, but
// what it writes is CRM Activities.
//
// "Assign" and "Remove" joined it for tags: AssignTag and RemoveTag write a
// tag_assignments row each, even though both also return a read-shaped list
// of the record's current tags for the caller's convenience.
//
// "Bulk" joined it for the same reason as all of the above: every BulkX RPC
// loops a write (assign/remove a tag, complete/reopen a task, reassign an
// owner), even though the response is a read-shaped list of per-item results.
func isMutation(method string) bool {
	for _, p := range []string{"Create", "Update", "Delete", "Upsert", "Convert", "Complete", "Reopen", "Archive", "Move", "Start", "Disconnect", "Sync", "Erase", "Suppress", "Release", "Publish", "Set", "Mark", "Assign", "Remove", "Bulk"} {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}
