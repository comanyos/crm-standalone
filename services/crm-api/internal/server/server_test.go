package server

import (
	"strings"
	"testing"

	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
)

// The scope policy is checked against the SERVICE DESCRIPTOR, not against a
// list written by hand.
//
// A hand-written list inverts the point of this test. Adding an RPC and
// forgetting its scopePolicy entry, and forgetting to add it to the test, are
// the same act of forgetting — so a hardcoded list only catches someone who
// remembered the test but not the policy, which is not how the mistake happens.
// It caught nothing while looking like it caught everything.
//
// Deriving the list from the generated descriptor means a new RPC appears here
// the moment `make generate` runs, whether or not anyone remembered this file.

// procedures returns every procedure connect registers for CRMService, built
// the same way connect builds them: "/<service full name>/<method name>".
func procedures(t *testing.T) []string {
	t.Helper()

	services := crmv1.File_crm_v1_crm_proto.Services()
	svc := services.Get(0)
	for i := 0; i < services.Len(); i++ {
		if services.Get(i).Name() == "CRMService" {
			svc = services.Get(i)
		}
	}
	if got := string(svc.Name()); got != "CRMService" {
		t.Fatalf("expected CRMService in the descriptor, found %q", got)
	}

	methods := svc.Methods()
	if methods.Len() == 0 {
		t.Fatal("the descriptor reports no methods; the derivation is broken, and a " +
			"test that derives nothing passes for the wrong reason")
	}

	out := make([]string, 0, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		out = append(out, "/"+string(svc.FullName())+"/"+string(methods.Get(i).Name()))
	}
	return out
}

// TestScopePolicyCoversEveryProcedure fails if any registered RPC has no scope.
//
// An RPC missing from scopePolicy is enforced as PUBLIC — the interceptor
// checks only the procedures it finds in the map — and it behaves completely
// normally in every other test. This is the only thing standing between
// "someone added a handler" and "someone shipped an unauthorised endpoint".
func TestScopePolicyCoversEveryProcedure(t *testing.T) {
	for _, proc := range procedures(t) {
		scope, ok := scopePolicy[proc]
		if !ok || strings.TrimSpace(scope) == "" {
			t.Errorf("scopePolicy has no entry for %q; that RPC is enforced as public", proc)
		}
	}
}

// TestScopePolicyHasNoDeadEntries fails if the map names a procedure that does
// not exist.
//
// A typo in a key is worse than a missing key: the entry looks present in
// review, matches nothing at runtime, and leaves the real procedure unguarded.
// This is the direction a hand-written list could never check, because both
// sides were typed by the same person from the same assumption.
func TestScopePolicyHasNoDeadEntries(t *testing.T) {
	live := make(map[string]bool)
	for _, p := range procedures(t) {
		live[p] = true
	}
	for proc := range scopePolicy {
		if !live[proc] {
			t.Errorf("scopePolicy names %q, which is not a procedure on this service; "+
				"if that is a typo, the real procedure is unguarded", proc)
		}
	}
}

// TestScopePolicySeparatesReadFromWrite fails if a mutation is guarded by a
// read scope. Reusing the read scope for a write lets anyone who may look at a
// record change it, which reads fine in a diff.
func TestScopePolicySeparatesReadFromWrite(t *testing.T) {
	for _, proc := range procedures(t) {
		scope, ok := scopePolicy[proc]
		if !ok {
			continue // reported by TestScopePolicyCoversEveryProcedure
		}
		method := proc[strings.LastIndex(proc, "/")+1:]
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
func isMutation(method string) bool {
	for _, p := range []string{"Create", "Update", "Delete", "Upsert", "Convert"} {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}
