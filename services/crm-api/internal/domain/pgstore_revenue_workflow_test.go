package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/pgtest"
)

// M3's revenue workflows against real PostgreSQL (docs/ROADMAP.md M3:
// "Verify each backend workflow against Postgres/RLS, not only in-memory
// stores, including authorization and race/retry behavior").
//
// pgstore_lead_conversion_test.go already walks the conversion happy path
// here. This file covers what it does not: the lead and opportunity
// lifecycles in full, the terminal-stage close rules, stage-history
// integrity, optimistic-concurrency conflicts, and the two races that
// actually matter — two conversions of one lead, and two stage advances of
// one opportunity.
//
// Everything runs through PgStore, so the SQL, the transactions and the row
// locks are what is being exercised. TestRevenueWorkflowsUnderRestrictedRole
// at the bottom re-runs the load-bearing part as crm_app, the role
// row-level security actually binds.

// revenueScope returns a scope nothing else in the suite shares. Every test
// here creates its own tenant, so tests neither see each other's rows nor
// depend on running in a particular order — the tmpfs test database is reused
// across runs and is never truncated between them.
func revenueScope(label string) Scope {
	stamp := time.Now().UnixNano()
	return Scope{
		TenantID: fmt.Sprintf("tenant-rev-%s-%d", label, stamp),
		OrgID:    fmt.Sprintf("org-rev-%s-%d", label, stamp),
	}
}

// advanceLead walks a Lead from its current status to want, one legal
// transition at a time, and returns the stored record. The lifecycle is
// deliberately forward-only one step at a time (lead_lifecycle.go), so a test
// that wants a qualified lead has to earn it.
func advanceLead(ctx context.Context, t *testing.T, s Store, scope Scope, lead *Lead, want string) *Lead {
	t.Helper()
	order := []string{LeadStatusNew, LeadStatusWorking, LeadStatusQualified}
	from := -1
	to := -1
	for i, status := range order {
		if status == lead.Status {
			from = i
		}
		if status == want {
			to = i
		}
	}
	if from < 0 || to < 0 || to < from {
		t.Fatalf("advanceLead cannot walk %q -> %q", lead.Status, want)
	}
	for i := from + 1; i <= to; i++ {
		next := *lead
		next.Status = order[i]
		updated, err := s.UpdateLead(ctx, scope, &next)
		if err != nil {
			t.Fatalf("advance lead to %s: %v", order[i], err)
		}
		lead = updated
	}
	return lead
}

// qualifiedLead creates a Lead and walks it to qualified, ready to convert.
func qualifiedLead(ctx context.Context, t *testing.T, s Store, scope Scope, company, email string) *Lead {
	t.Helper()
	lead, err := s.CreateLead(ctx, scope, &Lead{Company: company, ContactEmail: email, OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	return advanceLead(ctx, t, s, scope, lead, LeadStatusQualified)
}

// openOpportunity creates an Account and an Opportunity on the tenant's
// default pipeline, at its first open stage.
func openOpportunity(ctx context.Context, t *testing.T, s Store, scope Scope, name string) (*Account, *Opportunity) {
	t.Helper()
	account, err := s.CreateAccount(ctx, scope, &Account{Name: name + " Holdings", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	opp, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: name, AccountID: account.ID, Amount: 1000})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	return account, opp
}

// --- Leads -----------------------------------------------------------------

// TestPgStoreLeadLifecycle walks the whole state machine against Postgres:
// the default status, every legal step, both terminal branches, and the four
// ways a caller can try to cheat it.
func TestPgStoreLeadLifecycle(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("lead-lifecycle")

	lead, err := s.CreateLead(ctx, scope, &Lead{Company: "Acme", ContactEmail: "alice@acme.example", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	if lead.Status != LeadStatusNew {
		t.Fatalf("default status = %q, want %q", lead.Status, LeadStatusNew)
	}

	// A lead cannot be created straight into a terminal status.
	if _, err := s.CreateLead(ctx, scope, &Lead{Company: "Cheat", Status: LeadStatusConverted}); !errors.Is(err, ErrInvalid) {
		t.Errorf("create as converted: %v, want ErrInvalid", err)
	}
	if _, err := s.CreateLead(ctx, scope, &Lead{Company: "Cheat", Status: "sold"}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("create with an unknown status: %v, want ErrInvalidTransition", err)
	}

	// Skipping a step is refused, and refusing it must not have written
	// anything: the stored record still reads new.
	skip := *lead
	skip.Status = LeadStatusQualified
	if _, err := s.UpdateLead(ctx, scope, &skip); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("new -> qualified: %v, want ErrInvalidTransition", err)
	}
	if stored, err := s.GetLead(ctx, scope, lead.ID); err != nil || stored.Status != LeadStatusNew {
		t.Fatalf("after the refused skip the lead reads %+v, %v — a rejected transition wrote", stored, err)
	}

	// An edit that does not touch status is never blocked by the machine.
	rename := *lead
	rename.Company = "Acme Incorporated"
	rename.Score = 42
	renamed, err := s.UpdateLead(ctx, scope, &rename)
	if err != nil {
		t.Fatalf("edit without a status change: %v", err)
	}
	if renamed.Company != "Acme Incorporated" || renamed.Score != 42 || renamed.Status != LeadStatusNew {
		t.Fatalf("edited lead = %+v", renamed)
	}
	if !renamed.CreatedAt.Equal(lead.CreatedAt) {
		t.Errorf("created_at = %v after update, want %v preserved", renamed.CreatedAt, lead.CreatedAt)
	}

	working := advanceLead(ctx, t, s, scope, renamed, LeadStatusWorking)

	// Backward is refused as firmly as skipping ahead.
	backward := *working
	backward.Status = LeadStatusNew
	if _, err := s.UpdateLead(ctx, scope, &backward); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("working -> new: %v, want ErrInvalidTransition", err)
	}

	qualified := advanceLead(ctx, t, s, scope, working, LeadStatusQualified)

	// converted is ConvertLead's to make, never UpdateLead's, even from the
	// one status that may legally reach it.
	direct := *qualified
	direct.Status = LeadStatusConverted
	if _, err := s.UpdateLead(ctx, scope, &direct); !errors.Is(err, ErrInvalid) {
		t.Errorf("qualified -> converted through UpdateLead: %v, want ErrInvalid", err)
	}

	// The other branch out of qualified is a normal update, and is terminal.
	disqualify := *qualified
	disqualify.Status = LeadStatusDisqualified
	dead, err := s.UpdateLead(ctx, scope, &disqualify)
	if err != nil {
		t.Fatalf("qualified -> disqualified: %v", err)
	}
	revive := *dead
	revive.Status = LeadStatusWorking
	if _, err := s.UpdateLead(ctx, scope, &revive); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("disqualified -> working: %v, want ErrInvalidTransition", err)
	}
	if _, err := s.ConvertLead(ctx, scope, ConvertLeadInput{LeadID: dead.ID}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("converting a disqualified lead: %v, want ErrInvalidTransition", err)
	}

	// Delete is tenant-scoped and idempotent only in the sense that the
	// second call reports NotFound rather than succeeding silently.
	if err := s.DeleteLead(ctx, scope, dead.ID); err != nil {
		t.Fatalf("delete lead: %v", err)
	}
	if err := s.DeleteLead(ctx, scope, dead.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
}

// TestPgStoreLeadOptimisticConcurrency proves the check the web client has
// been sending a value for all along. Two editors open the same lead; the
// second save must be refused rather than silently overwriting the first.
func TestPgStoreLeadOptimisticConcurrency(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("lead-occ")

	lead, err := s.CreateLead(ctx, scope, &Lead{Company: "Contested", ContactEmail: "c@contested.example"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}

	// Both editors hold the version they read.
	first := *lead
	second := *lead

	first.Score = 10
	if _, err := s.UpdateLead(ctx, scope, &first); err != nil {
		t.Fatalf("first save: %v", err)
	}

	second.Score = 20
	_, err = s.UpdateLead(ctx, scope, &second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second save: %v, want ErrConflict — the first editor's change was about to be lost", err)
	}

	stored, err := s.GetLead(ctx, scope, lead.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Score != 10 {
		t.Fatalf("score = %d, want 10 — the refused save wrote anyway", stored.Score)
	}

	// The documented recovery: re-read, re-apply, save.
	retry := *stored
	retry.Score = 20
	saved, err := s.UpdateLead(ctx, scope, &retry)
	if err != nil {
		t.Fatalf("retry after conflict: %v", err)
	}
	if saved.Score != 20 {
		t.Fatalf("score after retry = %d, want 20", saved.Score)
	}

	// A caller that never sends updated_at opts out, which is what makes the
	// check safe to add to an endpoint that already had callers.
	blind := *saved
	blind.UpdatedAt = time.Time{}
	blind.Score = 30
	if _, err := s.UpdateLead(ctx, scope, &blind); err != nil {
		t.Fatalf("update without a version: %v, want it to be allowed", err)
	}
}

// --- Lead conversion -------------------------------------------------------

// TestPgStoreLeadConversionIsAtomic proves the whole conversion is one
// transaction by making it fail at its last step and showing that the
// Contact and Account it had already created are gone.
//
// The failure is engineered from configuration rather than injected: the
// tenant's default pipeline has no open stage, so creating the conversion
// Opportunity is refused — after the Contact and Account inserts have already
// run inside the transaction. Without the transaction those two would
// survive, leaving a tenant with an orphan contact and account and a lead
// still sitting in qualified.
func TestPgStoreLeadConversionIsAtomic(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("convert-atomic")

	// A default pipeline whose only stage is terminal.
	pipeline, err := s.CreatePipeline(ctx, scope, &Pipeline{Name: "Closed only", IsDefault: true})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if _, err := s.CreatePipelineStage(ctx, scope, &PipelineStage{
		PipelineID: pipeline.ID, ID: "won", Name: "Won", Position: 0,
		Probability: 100, TerminalType: "won", RequiresCloseReason: true,
	}); err != nil {
		t.Fatalf("create stage: %v", err)
	}

	lead := qualifiedLead(ctx, t, s, scope, "Rollback Co", "new@rollback.example")

	_, err = s.ConvertLead(ctx, scope, ConvertLeadInput{LeadID: lead.ID, CreateOpportunity: true})
	if err == nil {
		t.Fatal("conversion succeeded against a pipeline with no open stage; this test's premise is wrong")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("conversion error = %v, want ErrInvalid", err)
	}

	// Nothing the failed transaction created may have survived it.
	if got, err := s.FindContactByEmail(ctx, scope, "new@rollback.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a Contact survived the rolled-back conversion: %+v (%v)", got, err)
	}
	if got, err := s.FindAccountByDomain(ctx, scope, "rollback.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an Account survived the rolled-back conversion: %+v (%v)", got, err)
	}
	opps, _, err := s.ListOpportunities(ctx, scope, Page{})
	if err != nil {
		t.Fatalf("list opportunities: %v", err)
	}
	if len(opps) != 0 {
		t.Errorf("tenant has %d opportunities after a failed conversion, want 0", len(opps))
	}

	stored, err := s.GetLead(ctx, scope, lead.ID)
	if err != nil {
		t.Fatalf("re-read lead: %v", err)
	}
	if stored.Status != LeadStatusQualified {
		t.Errorf("lead status = %q after a failed conversion, want %q", stored.Status, LeadStatusQualified)
	}
	if stored.ConvertedContactID != "" || stored.ConvertedAccountID != "" || stored.ConvertedOpportunityID != "" {
		t.Errorf("lead carries conversion links after a failed conversion: %+v", stored)
	}
}

// TestPgStoreLeadConversionLinksAreReal checks the linkage rather than just
// the ids being non-empty: each id must name a record that exists, in this
// tenant, pointing back the way conversion says it does.
func TestPgStoreLeadConversionLinksAreReal(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("convert-links")

	lead := qualifiedLead(ctx, t, s, scope, "Linkage Ltd", "buyer@linkage.example")

	result, err := s.ConvertLead(ctx, scope, ConvertLeadInput{
		LeadID: lead.ID, CreateOpportunity: true, OpportunityName: "Linkage rollout", OpportunityAmount: 5000,
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	contact, err := s.GetContact(ctx, scope, result.Lead.ConvertedContactID)
	if err != nil {
		t.Fatalf("converted_contact_id %q does not resolve: %v", result.Lead.ConvertedContactID, err)
	}
	account, err := s.GetAccount(ctx, scope, result.Lead.ConvertedAccountID)
	if err != nil {
		t.Fatalf("converted_account_id %q does not resolve: %v", result.Lead.ConvertedAccountID, err)
	}
	opp, err := s.GetOpportunity(ctx, scope, result.Lead.ConvertedOpportunityID)
	if err != nil {
		t.Fatalf("converted_opportunity_id %q does not resolve: %v", result.Lead.ConvertedOpportunityID, err)
	}

	if contact.Email != "buyer@linkage.example" {
		t.Errorf("contact email = %q, want the lead's", contact.Email)
	}
	if contact.AccountID != account.ID {
		t.Errorf("contact.account_id = %q, want %q — the contact is not attached to the account conversion linked", contact.AccountID, account.ID)
	}
	if opp.AccountID != account.ID {
		t.Errorf("opportunity.account_id = %q, want %q", opp.AccountID, account.ID)
	}
	if opp.Name != "Linkage rollout" || opp.Amount != 5000 {
		t.Errorf("opportunity = %+v, want the requested name and amount", opp)
	}
	// A conversion-created deal is an ordinary deal: bound to the tenant's
	// default pipeline at its first open stage, exactly like one created
	// directly.
	if opp.PipelineID == "" || opp.StageID == "" {
		t.Errorf("opportunity is not pipeline-bound: %+v", opp)
	}
	stage, err := activePipelineStage(ctx, s, scope, opp.PipelineID, opp.StageID)
	if err != nil {
		t.Fatalf("conversion opportunity names a stage that does not resolve: %v", err)
	}
	if stage.TerminalType != "open" {
		t.Errorf("conversion opportunity opened in a %q stage, want open", stage.TerminalType)
	}

	// The lead is now terminal in every direction.
	moveOut := *result.Lead
	moveOut.Status = LeadStatusWorking
	if _, err := s.UpdateLead(ctx, scope, &moveOut); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("converted -> working: %v, want ErrInvalidTransition", err)
	}
}

// TestPgStoreConcurrentLeadConversionConvertsExactlyOnce is the retry/race
// case: several callers convert the same lead at the same moment — a
// double-clicked button, or a client retrying a request whose response it
// never saw.
//
// Every caller must come back with the same three ids, and the tenant must
// hold exactly one contact, one account and one opportunity afterwards.
// Without the row lock the losers would each create their own Opportunity,
// which has no natural key to deduplicate against.
func TestPgStoreConcurrentLeadConversionConvertsExactlyOnce(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("convert-race")

	lead := qualifiedLead(ctx, t, s, scope, "Race Co", "buyer@race.example")

	const callers = 4
	results := make([]*ConvertLeadResult, callers)
	errs := make([]error, callers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = s.ConvertLead(ctx, scope, ConvertLeadInput{
				LeadID: lead.ID, CreateOpportunity: true, OpportunityAmount: float64(100 * (i + 1)),
			})
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v — every concurrent conversion of a convertible lead must succeed", i, err)
		}
	}
	for i := 1; i < callers; i++ {
		if results[i].Contact.ID != results[0].Contact.ID ||
			results[i].Account.ID != results[0].Account.ID ||
			results[i].Opportunity.ID != results[0].Opportunity.ID {
			t.Fatalf("caller %d converted to different records than caller 0:\n  %d: %s/%s/%s\n  0: %s/%s/%s",
				i, i, results[i].Contact.ID, results[i].Account.ID, results[i].Opportunity.ID,
				results[0].Contact.ID, results[0].Account.ID, results[0].Opportunity.ID)
		}
	}

	contacts, _, err := s.ListContacts(ctx, scope, Page{}, ContactFilter{}, SortContactCreatedDesc)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Errorf("tenant has %d contacts after %d concurrent conversions, want 1", len(contacts), callers)
	}
	accounts, _, err := s.ListAccounts(ctx, scope, Page{}, AccountFilter{}, SortAccountCreatedDesc)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("tenant has %d accounts after %d concurrent conversions, want 1", len(accounts), callers)
	}
	opps, _, err := s.ListOpportunities(ctx, scope, Page{})
	if err != nil {
		t.Fatalf("list opportunities: %v", err)
	}
	if len(opps) != 1 {
		t.Errorf("tenant has %d opportunities after %d concurrent conversions, want 1", len(opps), callers)
	}
}

// --- Opportunities ---------------------------------------------------------

// TestPgStoreOpportunityPipelineWorkflow walks a deal the length of the
// default pipeline and checks what each step wrote, including the close
// fields a terminal stage requires and the history the moves left behind.
func TestPgStoreOpportunityPipelineWorkflow(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("opp-workflow")

	account, opp := openOpportunity(ctx, t, s, scope, "Renewal")
	if opp.Stage != StageProspecting {
		t.Fatalf("new opportunity opened at %q, want %q", opp.Stage, StageProspecting)
	}
	if !opp.CloseDate.IsZero() || opp.CloseReason != "" {
		t.Errorf("a new open deal carries close fields: %+v", opp)
	}

	// A dangling account is refused on create and on update.
	if _, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: "Ghost", AccountID: "no-such-account"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("create against an unknown account: %v, want ErrInvalid", err)
	}

	// Skipping a stage is refused, and refusing it writes nothing — neither
	// the row nor a history entry.
	skip := *opp
	skip.StageID = StageProposal
	if _, err := s.UpdateOpportunity(ctx, scope, &skip); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("prospecting -> proposal: %v, want ErrInvalidTransition", err)
	}
	unknown := *opp
	unknown.StageID = "haggling"
	if _, err := s.UpdateOpportunity(ctx, scope, &unknown); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("advance to an unknown stage: %v, want ErrInvalidTransition", err)
	}
	if history, err := s.ListStageHistory(ctx, scope, opp.ID); err != nil || len(history) != 0 {
		t.Fatalf("history after two refused transitions = %d entries (%v), want 0", len(history), err)
	}

	// Every legal step, one at a time.
	for _, stage := range []string{StageQualification, StageProposal, StageNegotiation} {
		next := *opp
		next.StageID = stage
		advanced, err := s.UpdateOpportunity(ctx, scope, &next)
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		opp = advanced
	}

	// Closing requires a reason, and refuses a client-supplied close date —
	// the closing write is the one that stamps it.
	noReason := *opp
	noReason.StageID = StageClosedWon
	if _, err := s.UpdateOpportunity(ctx, scope, &noReason); !errors.Is(err, ErrInvalid) {
		t.Errorf("close without a reason: %v, want ErrInvalid", err)
	}
	preDated := *opp
	preDated.StageID = StageClosedWon
	preDated.CloseReason = "budget approved"
	preDated.CloseDate = time.Now().Add(-24 * time.Hour)
	if _, err := s.UpdateOpportunity(ctx, scope, &preDated); !errors.Is(err, ErrInvalid) {
		t.Errorf("close with a pre-supplied close_date: %v, want ErrInvalid", err)
	}
	if stored, err := s.GetOpportunity(ctx, scope, opp.ID); err != nil || stored.Stage != StageNegotiation {
		t.Fatalf("after two refused closes the deal reads %+v (%v), want still %q", stored, err, StageNegotiation)
	}

	closing := *opp
	closing.StageID = StageClosedWon
	closing.CloseReason = "budget approved"
	won, err := s.UpdateOpportunity(ctx, scope, &closing)
	if err != nil {
		t.Fatalf("close won: %v", err)
	}
	if won.CloseReason != "budget approved" {
		t.Errorf("close_reason = %q", won.CloseReason)
	}
	if won.CloseDate.IsZero() {
		t.Error("close_date was not stamped by the closing transition")
	}

	// Terminal means terminal, in both directions.
	reopen := *won
	reopen.StageID = StageNegotiation
	if _, err := s.UpdateOpportunity(ctx, scope, &reopen); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("closed_won -> negotiation: %v, want ErrInvalidTransition", err)
	}
	crossClose := *won
	crossClose.StageID = StageClosedLost
	if _, err := s.UpdateOpportunity(ctx, scope, &crossClose); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("closed_won -> closed_lost: %v, want ErrInvalidTransition", err)
	}

	// An edit that leaves the stage alone still works on a closed deal, and
	// must not append a history entry or re-stamp the close date.
	rename := *won
	rename.Name = "Renewal (signed)"
	renamed, err := s.UpdateOpportunity(ctx, scope, &rename)
	if err != nil {
		t.Fatalf("rename a closed deal: %v", err)
	}
	if !renamed.CloseDate.Equal(won.CloseDate) {
		t.Errorf("close_date moved on an unrelated save: %v -> %v", won.CloseDate, renamed.CloseDate)
	}

	// The history is the audit record: one entry per real move, in order,
	// forming an unbroken chain from where the deal opened to where it is.
	history, err := s.ListStageHistory(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	wantMoves := []struct{ from, to string }{
		{StageProspecting, StageQualification},
		{StageQualification, StageProposal},
		{StageProposal, StageNegotiation},
		{StageNegotiation, StageClosedWon},
	}
	if len(history) != len(wantMoves) {
		t.Fatalf("history has %d entries, want %d: %+v", len(history), len(wantMoves), history)
	}
	for i, want := range wantMoves {
		if history[i].FromStage != want.from || history[i].ToStage != want.to {
			t.Errorf("history[%d] = %s -> %s, want %s -> %s",
				i, history[i].FromStage, history[i].ToStage, want.from, want.to)
		}
		if history[i].OpportunityID != opp.ID {
			t.Errorf("history[%d].opportunity_id = %q, want %q", i, history[i].OpportunityID, opp.ID)
		}
		if i > 0 && history[i].FromStage != history[i-1].ToStage {
			t.Errorf("history breaks its chain at %d: previous ended at %q, this one starts at %q",
				i, history[i-1].ToStage, history[i].FromStage)
		}
	}
	if history[len(history)-1].ToStage != renamed.Stage {
		t.Errorf("history ends at %q but the deal is at %q", history[len(history)-1].ToStage, renamed.Stage)
	}

	// The won transition published exactly one durable event, carrying the
	// account's owner.
	events, err := s.OutboxEventsFor(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("outbox holds %d events for the won deal, want 1", len(events))
	}
	if events[0].Envelope.Type != EventOpportunityClosedWon {
		t.Errorf("event type = %q, want %q", events[0].Envelope.Type, EventOpportunityClosedWon)
	}
	if events[0].Envelope.EntityID != opp.ID || events[0].Envelope.TenantID != scope.TenantID {
		t.Errorf("event envelope = %+v, want it to name this deal and tenant", events[0].Envelope)
	}
	_ = account

	if err := s.DeleteOpportunity(ctx, scope, opp.ID); err != nil {
		t.Fatalf("delete opportunity: %v", err)
	}
	if _, err := s.GetOpportunity(ctx, scope, opp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v, want ErrNotFound", err)
	}
	if err := s.DeleteOpportunity(ctx, scope, opp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
}

// TestPgStoreOpportunityLostBranchRequiresCloseFields covers the other
// terminal stage, which the win path does not reach, and confirms losing a
// deal publishes nothing.
func TestPgStoreOpportunityLostBranchRequiresCloseFields(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("opp-lost")

	_, opp := openOpportunity(ctx, t, s, scope, "Lost deal")
	for _, stage := range []string{StageQualification, StageProposal, StageNegotiation} {
		next := *opp
		next.StageID = stage
		advanced, err := s.UpdateOpportunity(ctx, scope, &next)
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		opp = advanced
	}

	noReason := *opp
	noReason.StageID = StageClosedLost
	if _, err := s.UpdateOpportunity(ctx, scope, &noReason); !errors.Is(err, ErrInvalid) {
		t.Fatalf("close lost without a reason: %v, want ErrInvalid", err)
	}

	closing := *opp
	closing.StageID = StageClosedLost
	closing.CloseReason = "lost to competitor"
	lost, err := s.UpdateOpportunity(ctx, scope, &closing)
	if err != nil {
		t.Fatalf("close lost: %v", err)
	}
	if lost.CloseDate.IsZero() || lost.CloseReason != "lost to competitor" {
		t.Errorf("closed-lost deal = %+v, want both close fields set", lost)
	}

	events, err := s.OutboxEventsFor(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("losing a deal published %d events, want 0 — only closed_won publishes", len(events))
	}
}

// TestPgStoreOpportunityOptimisticConcurrency is the opportunity half of the
// lost-update proof, and additionally checks that a refused save leaves no
// stage-history entry: a conflict must not be half-applied.
func TestPgStoreOpportunityOptimisticConcurrency(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("opp-occ")

	_, opp := openOpportunity(ctx, t, s, scope, "Contested deal")

	first := *opp
	second := *opp

	first.Amount = 2000
	saved, err := s.UpdateOpportunity(ctx, scope, &first)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	// The second editor is still holding the pre-save version and is trying
	// to advance the stage from it.
	second.StageID = StageQualification
	_, err = s.UpdateOpportunity(ctx, scope, &second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second save: %v, want ErrConflict", err)
	}

	stored, err := s.GetOpportunity(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Amount != 2000 {
		t.Errorf("amount = %v, want 2000 — the refused save overwrote the first", stored.Amount)
	}
	if stored.Stage != StageProspecting {
		t.Errorf("stage = %q, want %q — the refused save advanced the deal", stored.Stage, StageProspecting)
	}
	history, err := s.ListStageHistory(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("a refused save left %d history entries, want 0: %+v", len(history), history)
	}

	// Re-read and retry, the documented recovery.
	retry := *saved
	retry.StageID = StageQualification
	advanced, err := s.UpdateOpportunity(ctx, scope, &retry)
	if err != nil {
		t.Fatalf("retry after conflict: %v", err)
	}
	if advanced.Stage != StageQualification {
		t.Fatalf("stage after retry = %q, want %q", advanced.Stage, StageQualification)
	}
}

// TestPgStoreConcurrentStageAdvanceKeepsHistoryContiguous is the race that
// motivated making UpdateOpportunity one locked transaction.
//
// Several callers try to move the same deal out of the same stage at once,
// each to a different destination. At most one can be right: the stage is a
// single value, and the history is supposed to be a chain. Without the lock
// every caller decides its transition against the same stale "from" stage and
// every one of them appends an entry leaving it — so the audit log grows a
// branch, and all but one of its entries describe a move that did not happen.
//
// Note these callers do not send updated_at: the optimistic-concurrency check
// would settle it before the lock ever mattered, and it is the lock under
// test here. They also send stage_id without the legacy stage string, which
// is what a current client does — sending a stale stage alongside a new
// stage_id means the compatibility branch in prepareOpportunityStage reads
// the stale one as the request.
func TestPgStoreConcurrentStageAdvanceKeepsHistoryContiguous(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("stage-race")

	_, opp := openOpportunity(ctx, t, s, scope, "Racy deal")

	const callers = 4
	errs := make([]error, callers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			attempt := *opp
			attempt.UpdatedAt = time.Time{} // opt out of the version check
			attempt.Stage = ""
			attempt.StageID = StageQualification
			attempt.Amount = float64(1000 + i)
			start.Wait()
			_, errs[i] = s.UpdateOpportunity(ctx, scope, &attempt)
		}(i)
	}
	start.Done()
	done.Wait()

	// Every caller asked for the same legal move, so re-applying it to an
	// already-advanced deal is a no-op rather than an error: all of them may
	// succeed. What must not happen is more than one of them recording a
	// move.
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}

	history, err := s.ListStageHistory(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("%d concurrent advances recorded %d history entries, want exactly 1: %+v",
			callers, len(history), history)
	}
	if history[0].FromStage != StageProspecting || history[0].ToStage != StageQualification {
		t.Errorf("history[0] = %s -> %s, want %s -> %s",
			history[0].FromStage, history[0].ToStage, StageProspecting, StageQualification)
	}

	stored, err := s.GetOpportunity(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Stage != history[0].ToStage {
		t.Errorf("deal is at %q but its history ends at %q", stored.Stage, history[0].ToStage)
	}
}

// TestPgStoreConcurrentDivergentStageAdvanceIsSerialized is the sharper form
// of the same race: callers asking for *different* destinations out of one
// stage. Exactly one may succeed; the others must be refused, because by the
// time they run the deal is no longer where their transition started.
func TestPgStoreConcurrentDivergentStageAdvanceIsSerialized(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := revenueScope("stage-diverge")

	_, opp := openOpportunity(ctx, t, s, scope, "Diverging deal")
	// Move it to negotiation, the one stage with two legal destinations.
	for _, stage := range []string{StageQualification, StageProposal, StageNegotiation} {
		next := *opp
		next.StageID = stage
		advanced, err := s.UpdateOpportunity(ctx, scope, &next)
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		opp = advanced
	}

	destinations := []string{StageClosedWon, StageClosedLost}
	errs := make([]error, len(destinations))
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i, dest := range destinations {
		done.Add(1)
		go func(i int, dest string) {
			defer done.Done()
			attempt := *opp
			attempt.UpdatedAt = time.Time{}
			attempt.Stage = ""
			attempt.StageID = dest
			attempt.CloseReason = "decided: " + dest
			start.Wait()
			_, errs[i] = s.UpdateOpportunity(ctx, scope, &attempt)
		}(i, dest)
	}
	start.Done()
	done.Wait()

	succeeded := 0
	for i, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidTransition):
			// Correct: the deal had already closed by the time this ran.
		default:
			t.Errorf("caller %d (%s): %v, want nil or ErrInvalidTransition", i, destinations[i], err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d of %d divergent closes succeeded, want exactly 1", succeeded, len(destinations))
	}

	history, err := s.ListStageHistory(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	closing := history[len(history)-1]
	if closing.FromStage != StageNegotiation {
		t.Errorf("the closing history entry leaves %q, want %q", closing.FromStage, StageNegotiation)
	}
	leaving := 0
	for _, h := range history {
		if h.FromStage == StageNegotiation {
			leaving++
		}
	}
	if leaving != 1 {
		t.Fatalf("%d history entries leave negotiation, want 1 — the audit log branched: %+v", leaving, history)
	}

	stored, err := s.GetOpportunity(ctx, scope, opp.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Stage != closing.ToStage {
		t.Errorf("deal is at %q but its history ends at %q", stored.Stage, closing.ToStage)
	}
	if stored.CloseReason == "" || stored.CloseDate.IsZero() {
		t.Errorf("the winning close left the deal without close fields: %+v", stored)
	}
}

// --- Pool behaviour under contention ---------------------------------------

// smallPoolStore opens a store whose connection pool holds exactly two
// connections, and seeds a custom field so every write has to read the
// definition catalog on its way through.
//
// A multi-statement write holds one connection for the length of its
// transaction. If any step inside it reads through the store instead of
// through the transaction, that read asks the pool for a *second* connection
// while the first is still held — and with as many concurrent writers as the
// pool has connections, every writer holds one and waits for another that
// will never be free. The symptom is not a wrong answer, it is the whole
// service hanging until the contexts expire, which is why it needs its own
// test rather than being covered incidentally by the races above.
func smallPoolStore(t *testing.T, scope Scope) *PgStore {
	t.Helper()
	base := livePgStore(t)

	dsn := testDSN()
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	s, err := NewPgStore(context.Background(), dsn+sep+"pool_max_conns=2&pool_min_conns=1")
	if err != nil {
		pgtest.Unavailable(t, "cannot open a size-2 pool: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// One custom field per object the revenue writes touch, so the catalog
	// read inside each transaction is not trivially empty.
	for _, object := range []string{"contact", "account", "opportunity"} {
		if _, err := base.CreateCustomFieldDefinition(context.Background(), scope, &CustomFieldDefinition{
			ObjectType: object, FieldKey: "region", Label: "Region", DataType: "text",
		}); err != nil {
			t.Fatalf("seed custom field on %s: %v", object, err)
		}
	}
	return s
}

// TestPgStoreConcurrentWritesDoNotExhaustThePool runs more concurrent
// multi-statement writes than the pool has connections. Every one of them
// must finish well inside the deadline; a second acquire inside an open
// transaction would leave them all blocked instead.
func TestPgStoreConcurrentWritesDoNotExhaustThePool(t *testing.T) {
	scope := revenueScope("pool")
	s := smallPoolStore(t, scope)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Four leads and four opportunities, worked concurrently by eight
	// goroutines against a two-connection pool.
	const writers = 4
	leads := make([]*Lead, writers)
	opps := make([]*Opportunity, writers)
	for i := 0; i < writers; i++ {
		leads[i] = qualifiedLead(ctx, t, s, scope, fmt.Sprintf("Pool Co %d", i), fmt.Sprintf("buyer%d@pool.example", i))
		_, opps[i] = openOpportunity(ctx, t, s, scope, fmt.Sprintf("Pool deal %d", i))
	}

	errs := make([]error, 2*writers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < writers; i++ {
		done.Add(2)
		go func(i int) {
			defer done.Done()
			start.Wait()
			_, errs[i] = s.ConvertLead(ctx, scope, ConvertLeadInput{LeadID: leads[i].ID, CreateOpportunity: true})
		}(i)
		go func(i int) {
			defer done.Done()
			attempt := *opps[i]
			attempt.Stage = ""
			attempt.StageID = StageQualification
			start.Wait()
			_, errs[writers+i] = s.UpdateOpportunity(ctx, scope, &attempt)
		}(i)
	}
	start.Done()

	finished := make(chan struct{})
	go func() { done.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("concurrent writes did not finish: the pool is deadlocked, " +
			"which is what a second connection acquired inside an open transaction does")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
}

// --- The same workflows as the role row-level security actually binds ------

// restrictedRevenueStore opens a PgStore connected as crm_app — the role
// migration 0003 creates without BYPASSRLS, and the one a real deployment
// uses.
//
// Everything above this point runs as the superuser that owns the tables,
// which Postgres exempts from row-level security. That is fine for proving
// the Go logic, and proves nothing at all about the policies. This connection
// is the one where a missing tenant predicate returns no rows instead of
// someone else's.
func restrictedRevenueStore(t *testing.T) *PgStore {
	t.Helper()
	// Applies migrations, so crm_app and the policies exist before we
	// connect as it.
	_ = livePgStore(t)

	s, err := NewPgStore(context.Background(), restrictedDSN(t))
	if err != nil {
		pgtest.Unavailable(t, "cannot open a crm_app store (migration 0003 needs a superuser connection): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRevenueWorkflowsUnderRowLevelSecurity re-runs the load-bearing half of
// the revenue workflow as crm_app, with two organizations, and proves the
// tenant boundary from the other side: every cross-organization read and
// every cross-organization mutation answers NotFound.
//
// NotFound rather than PermissionDenied is the deliberate part. "You may not
// touch that" confirms the id names a real record in someone else's
// organization, which is itself the disclosure the boundary exists to
// prevent — see internal/api/tenant_isolation_test.go for the same rule at
// the RPC layer.
func TestRevenueWorkflowsUnderRowLevelSecurity(t *testing.T) {
	s := restrictedRevenueStore(t)
	ctx := context.Background()

	orgA := revenueScope("rls-a")
	orgB := revenueScope("rls-b")

	// Organization A runs a complete revenue workflow.
	lead := qualifiedLead(ctx, t, s, orgA, "Alpha Co", "buyer@alpha.example")
	converted, err := s.ConvertLead(ctx, orgA, ConvertLeadInput{
		LeadID: lead.ID, CreateOpportunity: true, OpportunityAmount: 7500,
	})
	if err != nil {
		t.Fatalf("[A] convert under RLS: %v", err)
	}
	opp := converted.Opportunity
	advance := *opp
	advance.Stage = ""
	advance.StageID = StageQualification
	opp, err = s.UpdateOpportunity(ctx, orgA, &advance)
	if err != nil {
		t.Fatalf("[A] advance under RLS: %v", err)
	}
	if history, err := s.ListStageHistory(ctx, orgA, opp.ID); err != nil || len(history) != 1 {
		t.Fatalf("[A] stage history = %d entries (%v), want 1", len(history), err)
	}

	// Organization B exists and has its own records, so an empty answer
	// below cannot be "the store is broken for everyone".
	otherLead := qualifiedLead(ctx, t, s, orgB, "Beta Co", "buyer@beta.example")
	_, otherOpp := openOpportunity(ctx, t, s, orgB, "Beta deal")

	// --- Cross-organization reads ------------------------------------
	if _, err := s.GetLead(ctx, orgB, lead.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] GetLead on A's lead: %v, want ErrNotFound", err)
	}
	if _, err := s.GetOpportunity(ctx, orgB, opp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] GetOpportunity on A's deal: %v, want ErrNotFound", err)
	}
	if _, err := s.GetContact(ctx, orgB, converted.Contact.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] GetContact on A's converted contact: %v, want ErrNotFound", err)
	}
	if _, err := s.GetAccount(ctx, orgB, converted.Account.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] GetAccount on A's converted account: %v, want ErrNotFound", err)
	}
	if _, err := s.FindContactByEmail(ctx, orgB, "buyer@alpha.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] FindContactByEmail on A's contact: %v, want ErrNotFound", err)
	}
	if _, err := s.FindAccountByDomain(ctx, orgB, "alpha.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] FindAccountByDomain on A's account: %v, want ErrNotFound", err)
	}
	// An empty history is the tenant-safe answer for a stage history that
	// exists but is not B's.
	if history, err := s.ListStageHistory(ctx, orgB, opp.ID); err != nil || len(history) != 0 {
		t.Errorf("[B] ListStageHistory on A's deal = %d entries (%v), want 0", len(history), err)
	}

	// --- Cross-organization collection reads --------------------------
	leads, _, err := s.ListLeads(ctx, orgB, Page{})
	if err != nil {
		t.Fatalf("[B] list leads: %v", err)
	}
	for _, l := range leads {
		if l.ID == lead.ID {
			t.Errorf("[B] ListLeads returned A's lead %q", l.ID)
		}
	}
	if len(leads) != 1 || leads[0].ID != otherLead.ID {
		t.Errorf("[B] ListLeads = %d rows, want only B's own", len(leads))
	}
	opps, _, err := s.ListOpportunities(ctx, orgB, Page{})
	if err != nil {
		t.Fatalf("[B] list opportunities: %v", err)
	}
	if len(opps) != 1 || opps[0].ID != otherOpp.ID {
		t.Errorf("[B] ListOpportunities = %d rows, want only B's own", len(opps))
	}

	// --- Cross-organization mutations ---------------------------------
	stealLead := *lead
	stealLead.Company = "Owned by B"
	if _, err := s.UpdateLead(ctx, orgB, &stealLead); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] UpdateLead on A's lead: %v, want ErrNotFound", err)
	}
	if err := s.DeleteLead(ctx, orgB, lead.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] DeleteLead on A's lead: %v, want ErrNotFound", err)
	}
	stealOpp := *opp
	stealOpp.Name = "Owned by B"
	if _, err := s.UpdateOpportunity(ctx, orgB, &stealOpp); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] UpdateOpportunity on A's deal: %v, want ErrNotFound", err)
	}
	if err := s.DeleteOpportunity(ctx, orgB, opp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] DeleteOpportunity on A's deal: %v, want ErrNotFound", err)
	}
	// Conversion is the compound one, and the refusal has to come before it
	// creates anything.
	if _, err := s.ConvertLead(ctx, orgB, ConvertLeadInput{LeadID: otherLead.ID, CreateOpportunity: true}); err != nil {
		t.Fatalf("[B] converting its own lead: %v", err)
	}
	if _, err := s.ConvertLead(ctx, orgB, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("[B] ConvertLead on A's lead: %v, want ErrNotFound", err)
	}
	// A cross-tenant opportunity may not be attached to a local account
	// either: the account reference is resolved in the caller's own tenant.
	crossAccount := *otherOpp
	crossAccount.AccountID = converted.Account.ID
	if _, err := s.UpdateOpportunity(ctx, orgB, &crossAccount); !errors.Is(err, ErrInvalid) {
		t.Errorf("[B] pointing its own deal at A's account: %v, want ErrInvalid", err)
	}

	// --- A's records are untouched ------------------------------------
	afterLead, err := s.GetLead(ctx, orgA, lead.ID)
	if err != nil {
		t.Fatalf("[A] re-read lead: %v", err)
	}
	if afterLead.Company != "Alpha Co" || afterLead.Status != LeadStatusConverted {
		t.Errorf("[A] lead = %+v after B's attempts, want untouched", afterLead)
	}
	afterOpp, err := s.GetOpportunity(ctx, orgA, opp.ID)
	if err != nil {
		t.Fatalf("[A] re-read opportunity: %v", err)
	}
	if afterOpp.Name != opp.Name || afterOpp.StageID != StageQualification {
		t.Errorf("[A] opportunity = %+v after B's attempts, want untouched", afterOpp)
	}
}
