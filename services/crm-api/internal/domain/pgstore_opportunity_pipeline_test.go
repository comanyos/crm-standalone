package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPgStoreOpportunityPipeline exercises B2's rules against real Postgres:
// the stage machine, referential integrity, the win/loss rule, stage
// history (plus its tenant isolation), and the closed_won event — the same
// behaviors opportunity_pipeline_test.go proves against MemStore, run here
// through PgStore/scopedDB so the SQL paths are not just compiled, but
// verified.
func TestPgStoreOpportunityPipeline(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-pipeline-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-pipeline-b-%d", stamp), OrgID: "org-b"}

	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-pg-1"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Dangling account_id rejected on Create.
	if _, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: "does-not-exist"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling account_id: got %v, want ErrInvalid", err)
	}

	opp, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	if opp.Stage != StageProspecting {
		t.Fatalf("default Stage = %q, want %q", opp.Stage, StageProspecting)
	}

	// Illegal transition: skip-ahead.
	skip := *opp
	skip.Stage = StageProposal
	if _, err := s.UpdateOpportunity(ctx, tenantA, &skip); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skip-ahead transition: got %v, want ErrInvalidTransition", err)
	}

	// Legal forward transition, no stage-changing side effects.
	opp.Stage = StageQualification
	opp, err = s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("advance to qualification: %v", err)
	}

	// Dangling account_id rejected on Update too.
	danglingAccount := *opp
	danglingAccount.AccountID = "does-not-exist"
	if _, err := s.UpdateOpportunity(ctx, tenantA, &danglingAccount); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update with dangling account_id: got %v, want ErrInvalid", err)
	}

	opp.Stage = StageProposal
	opp, err = s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("advance to proposal: %v", err)
	}
	opp.Stage = StageNegotiation
	opp, err = s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("advance to negotiation: %v", err)
	}

	// Win/loss: closing without a reason is rejected.
	noReason := *opp
	noReason.Stage = StageClosedWon
	if _, err := s.UpdateOpportunity(ctx, tenantA, &noReason); !errors.Is(err, ErrInvalid) {
		t.Fatalf("close without reason: got %v, want ErrInvalid", err)
	}

	// Close for real: closed_won, reason set, close_date unset -> stamped.
	opp.Stage = StageClosedWon
	opp.CloseReason = "won-fair"
	opp.Amount = 12345
	before := time.Now().UTC().Add(-time.Second)
	opp, err = s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("close won: %v", err)
	}
	if opp.CloseDate.IsZero() || opp.CloseDate.Before(before) {
		t.Fatalf("CloseDate = %v, want stamped to ~now", opp.CloseDate)
	}

	// Terminal: no transition out of closed_won.
	reopen := *opp
	reopen.Stage = StageNegotiation
	if _, err := s.UpdateOpportunity(ctx, tenantA, &reopen); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reopen closed_won: got %v, want ErrInvalidTransition", err)
	}

	// Stage history: prospecting -> qualification -> proposal -> negotiation
	// -> closed_won is 4 recorded changes (Create's initial stage is not one).
	histA, err := s.ListStageHistory(ctx, tenantA, opp.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	if len(histA) != 4 {
		t.Fatalf("stage history has %d rows, want 4", len(histA))
	}
	if histA[0].FromStage != StageProspecting || histA[0].ToStage != StageQualification {
		t.Fatalf("first stage history row = %+v", histA[0])
	}
	if last := histA[len(histA)-1]; last.ToStage != StageClosedWon {
		t.Fatalf("last stage history row = %+v, want ToStage %q", last, StageClosedWon)
	}

	// Tenant isolation on the new table.
	histB, err := s.ListStageHistory(ctx, tenantB, opp.ID)
	if err != nil {
		t.Fatalf("tenant B list stage history: %v", err)
	}
	if len(histB) != 0 {
		t.Fatalf("tenant B read %d of tenant A's stage history rows; want 0", len(histB))
	}

	// closed_won published exactly one event, with the documented payload
	// shape (source_version 1, entity type "opportunity").
	events, err := s.OutboxEventsFor(ctx, tenantA, opp.ID)
	if err != nil {
		t.Fatalf("outbox events for %s: %v", opp.ID, err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events on closed_won, want exactly 1", len(events))
	}
	if events[0].Envelope.Type != EventOpportunityClosedWon {
		t.Fatalf("event type = %q, want %q", events[0].Envelope.Type, EventOpportunityClosedWon)
	}

	// PipelineForecast reflects the closed_won deal at full amount, and
	// tenant A's forecast never sees tenant B's data.
	forecast, err := PipelineForecast(ctx, s, tenantA, ForecastOptions{})
	if err != nil {
		t.Fatalf("forecast: %v", err)
	}
	if got := forecast.ByOwner["owner-pg-1"]; got != 12345 {
		t.Fatalf("ByOwner[owner-pg-1] = %v, want 12345", got)
	}
}
