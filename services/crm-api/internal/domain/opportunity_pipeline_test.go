package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// newTestOpportunity seeds an Account (referential integrity, B2 item 2) and
// creates an Opportunity at the given stage. An empty stage lets Create apply
// its default (prospecting). A closed stage needs a close_reason to satisfy
// the win/loss rule even at creation time, so this seeds one.
func newTestOpportunity(t *testing.T, s *MemStore, sc Scope, stage string) *Opportunity {
	t.Helper()
	acct, err := s.CreateAccount(ctx, sc, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	o := &Opportunity{Name: "Deal", AccountID: acct.ID, Stage: stage}
	if opportunityStageIsClosed(stage) {
		o.CloseReason = "seed-reason"
	}
	out, err := s.CreateOpportunity(ctx, sc, o)
	if err != nil {
		t.Fatalf("seed opportunity at stage %q: %v", stage, err)
	}
	return out
}

func mustCreateOpp(t *testing.T, s *MemStore, sc Scope, accountID, stage string, amount float64) *Opportunity {
	t.Helper()
	out, err := s.CreateOpportunity(ctx, sc, &Opportunity{
		Name: "Deal", AccountID: accountID, Stage: stage, Amount: amount,
	})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	return out
}

// TestOpportunityCreateDefaultsStageToProspecting covers B2 item 1's default.
func TestOpportunityCreateDefaultsStageToProspecting(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	out, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.Stage != StageProspecting {
		t.Fatalf("Stage = %q, want %q", out.Stage, StageProspecting)
	}
}

func TestOpportunityCreateRejectsUnknownStage(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	_, err = s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: acct.ID, Stage: "made-up-stage"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("create with unknown stage: got %v, want ErrInvalidTransition", err)
	}
}

// TestOpportunityStageTransitions is the transition table test: legal
// transitions, illegal transitions (skip-ahead and backward), terminal-state
// rejection, and reopen-of-closed rejection.
func TestOpportunityStageTransitions(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{"prospecting_to_qualification", StageProspecting, StageQualification, false},
		{"qualification_to_proposal", StageQualification, StageProposal, false},
		{"proposal_to_negotiation", StageProposal, StageNegotiation, false},
		{"negotiation_to_closed_won", StageNegotiation, StageClosedWon, false},
		{"negotiation_to_closed_lost", StageNegotiation, StageClosedLost, false},
		{"unchanged_stage_is_always_legal", StageProposal, StageProposal, false},

		{"skip_ahead_illegal", StageProspecting, StageProposal, true},
		{"skip_ahead_to_negotiation_illegal", StageQualification, StageNegotiation, true},
		{"backward_illegal", StageNegotiation, StageQualification, true},
		{"closed_won_is_terminal", StageClosedWon, StageNegotiation, true},
		{"closed_lost_is_terminal", StageClosedLost, StageNegotiation, true},
		{"reopen_closed_won_to_prospecting", StageClosedWon, StageProspecting, true},
		{"reopen_closed_lost_to_prospecting", StageClosedLost, StageProspecting, true},
		{"closed_won_to_closed_lost_illegal", StageClosedWon, StageClosedLost, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			o := newTestOpportunity(t, s, tenantA, tc.from)
			o.Stage = tc.to
			if opportunityStageIsClosed(tc.to) {
				o.CloseReason = "reason"
			}
			_, err := s.UpdateOpportunity(ctx, tenantA, o)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("%s -> %s: got %v, want ErrInvalidTransition", tc.from, tc.to, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s -> %s: got %v, want no error", tc.from, tc.to, err)
			}
		})
	}
}

// TestOpportunityDanglingAccountRejected covers B2 item 2, both on Create and
// on Update.
func TestOpportunityDanglingAccountRejected(t *testing.T) {
	s := NewMemStore()

	t.Run("create", func(t *testing.T) {
		_, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: "does-not-exist"})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("create with dangling account_id: got %v, want ErrInvalid", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		o := newTestOpportunity(t, s, tenantA, "")
		o.AccountID = "does-not-exist"
		_, err := s.UpdateOpportunity(ctx, tenantA, o)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("update with dangling account_id: got %v, want ErrInvalid", err)
		}
	})

	t.Run("account from a different tenant is also dangling", func(t *testing.T) {
		otherAccount, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Tenant B Co", OwnerID: "owner-b"})
		if err != nil {
			t.Fatalf("seed tenant B account: %v", err)
		}
		_, err = s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Deal", AccountID: otherAccount.ID})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("create against another tenant's account: got %v, want ErrInvalid", err)
		}
	})
}

// TestOpportunityWinLossRules covers B2 item 3 exactly: close_reason is
// required to close, close_date is stamped when unset, and a pre-supplied
// close_date on the closing write is rejected rather than honoured.
func TestOpportunityWinLossRules(t *testing.T) {
	t.Run("closing without a reason is rejected", func(t *testing.T) {
		s := NewMemStore()
		o := newTestOpportunity(t, s, tenantA, StageNegotiation)
		o.Stage = StageClosedWon
		_, err := s.UpdateOpportunity(ctx, tenantA, o)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("close without close_reason: got %v, want ErrInvalid", err)
		}
	})

	t.Run("close_date is stamped when unset", func(t *testing.T) {
		s := NewMemStore()
		o := newTestOpportunity(t, s, tenantA, StageNegotiation)
		o.Stage = StageClosedWon
		o.CloseReason = "won-fair"
		before := time.Now().UTC().Add(-time.Second)
		updated, err := s.UpdateOpportunity(ctx, tenantA, o)
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if updated.CloseDate.IsZero() || updated.CloseDate.Before(before) {
			t.Fatalf("CloseDate = %v, want stamped to ~now", updated.CloseDate)
		}
	})

	t.Run("a pre-supplied close_date is rejected", func(t *testing.T) {
		s := NewMemStore()
		o := newTestOpportunity(t, s, tenantA, StageNegotiation)
		o.Stage = StageClosedLost
		o.CloseReason = "lost-fair"
		o.CloseDate = time.Now().UTC()
		_, err := s.UpdateOpportunity(ctx, tenantA, o)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("close with pre-set close_date: got %v, want ErrInvalid", err)
		}
	})

	t.Run("re-saving an already-closed opportunity does not re-run the closing check", func(t *testing.T) {
		s := NewMemStore()
		o := newTestOpportunity(t, s, tenantA, StageNegotiation)
		o.Stage = StageClosedWon
		o.CloseReason = "won-fair"
		closed, err := s.UpdateOpportunity(ctx, tenantA, o)
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		// Stage is unchanged (still closed_won); only editing Name. This must
		// not re-trigger the "close_date already set" rejection.
		closed.Name = "Deal (renamed)"
		if _, err := s.UpdateOpportunity(ctx, tenantA, closed); err != nil {
			t.Fatalf("re-save of closed opportunity: %v", err)
		}
	})
}

// TestOpportunityStageHistoryRecordedOnChange covers B2 item 4: a
// stage-changing Update appends one history row; an Update that leaves Stage
// unchanged appends none.
func TestOpportunityStageHistoryRecordedOnChange(t *testing.T) {
	s := NewMemStore()
	o := newTestOpportunity(t, s, tenantA, "") // prospecting

	o.Stage = StageQualification
	updated, err := s.UpdateOpportunity(ctx, tenantA, o)
	if err != nil {
		t.Fatalf("stage-changing update: %v", err)
	}

	updated.Name = "renamed, no stage change"
	if _, err := s.UpdateOpportunity(ctx, tenantA, updated); err != nil {
		t.Fatalf("non-stage update: %v", err)
	}

	hist, err := s.ListStageHistory(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("stage history has %d rows, want exactly 1", len(hist))
	}
	if hist[0].OpportunityID != updated.ID || hist[0].FromStage != StageProspecting || hist[0].ToStage != StageQualification {
		t.Fatalf("stage history row = %+v, want opportunity %q %q -> %q",
			hist[0], updated.ID, StageProspecting, StageQualification)
	}
	if hist[0].ChangedAt.IsZero() {
		t.Fatal("stage history row has a zero ChangedAt")
	}
}

// TestOpportunityStageHistoryTenantIsolation is the mandatory tenancy test
// (.claude/rules/tenancy-and-scopes.md) for the new store method.
func TestOpportunityStageHistoryTenantIsolation(t *testing.T) {
	s := NewMemStore()
	o := newTestOpportunity(t, s, tenantA, "")
	o.Stage = StageQualification
	updated, err := s.UpdateOpportunity(ctx, tenantA, o)
	if err != nil {
		t.Fatalf("stage-changing update: %v", err)
	}

	histB, err := s.ListStageHistory(ctx, tenantB, updated.ID)
	if err != nil {
		t.Fatalf("tenant B list stage history: %v", err)
	}
	if len(histB) != 0 {
		t.Fatalf("tenant B read %d of tenant A's stage history rows; want 0", len(histB))
	}

	histA, err := s.ListStageHistory(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatalf("tenant A list stage history: %v", err)
	}
	if len(histA) != 1 {
		t.Fatalf("tenant A's own stage history has %d rows, want 1", len(histA))
	}

	if _, err := s.ListStageHistory(ctx, noScope, updated.ID); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("list stage history with no tenant: got %v, want ErrNoTenant", err)
	}
}

// TestPipelineForecast covers B2 item 5's weighted math: open opportunities
// weighted by DefaultStageProbabilities, closed_won at full amount,
// closed_lost at zero, grouped by owner (the Opportunity's Account's owner)
// and by stage.
func TestPipelineForecast(t *testing.T) {
	s := NewMemStore()

	alice, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Alice Co", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("seed alice's account: %v", err)
	}
	bob, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Bob Co", OwnerID: "bob"})
	if err != nil {
		t.Fatalf("seed bob's account: %v", err)
	}

	mustCreateOpp(t, s, tenantA, alice.ID, StageProspecting, 1000) // 1000 * .10 = 100
	mustCreateOpp(t, s, tenantA, alice.ID, StageNegotiation, 2000) // 2000 * .75 = 1500

	won := mustCreateOpp(t, s, tenantA, bob.ID, StageNegotiation, 500)
	won.Stage = StageClosedWon
	won.CloseReason = "won-fair"
	if _, err := s.UpdateOpportunity(ctx, tenantA, won); err != nil {
		t.Fatalf("close won: %v", err)
	}

	lost := mustCreateOpp(t, s, tenantA, bob.ID, StageNegotiation, 10000)
	lost.Stage = StageClosedLost
	lost.CloseReason = "lost-fair"
	if _, err := s.UpdateOpportunity(ctx, tenantA, lost); err != nil {
		t.Fatalf("close lost: %v", err)
	}

	// A tenant B opportunity must never leak into tenant A's forecast.
	otherAcct, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Co", OwnerID: "carol"})
	if err != nil {
		t.Fatalf("seed tenant B account: %v", err)
	}
	mustCreateOpp(t, s, tenantB, otherAcct.ID, StageProspecting, 999999)

	forecast, err := PipelineForecast(ctx, s, tenantA, ForecastOptions{})
	if err != nil {
		t.Fatalf("forecast: %v", err)
	}

	wantByOwner := map[string]float64{
		"alice": 100 + 1500,
		"bob":   500 + 0,
	}
	for owner, want := range wantByOwner {
		if got := forecast.ByOwner[owner]; got != want {
			t.Errorf("ByOwner[%q] = %v, want %v", owner, got, want)
		}
	}
	if got, ok := forecast.ByOwner["carol"]; ok {
		t.Errorf("ByOwner[carol] = %v; tenant B's owner leaked into tenant A's forecast", got)
	}

	wantByStage := map[string]float64{
		StageProspecting: 100,
		StageNegotiation: 1500,
		StageClosedWon:   500,
		StageClosedLost:  0,
	}
	for stage, want := range wantByStage {
		if got := forecast.ByStage[stage]; got != want {
			t.Errorf("ByStage[%q] = %v, want %v", stage, got, want)
		}
	}

	wantTotal := 100.0 + 1500.0 + 500.0 + 0.0
	if forecast.Total != wantTotal {
		t.Errorf("Total = %v, want %v", forecast.Total, wantTotal)
	}
}

func TestPipelineForecastRejectsEmptyScope(t *testing.T) {
	s := NewMemStore()
	if _, err := PipelineForecast(ctx, s, noScope, ForecastOptions{}); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("forecast with no tenant: got %v, want ErrNoTenant", err)
	}
}

// TestOpportunityClosedWonEvent covers B2 item 6: opportunity.closed_won
// fires exactly once, only on the transition into closed_won — never on
// closed_lost, never on any other transition, and never again on a later
// unrelated save of an already-won deal.
func TestOpportunityClosedWonEvent(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	t.Run("closed_won enqueues exactly one event with the documented payload", func(t *testing.T) {
		o := mustCreateOpp(t, s, tenantA, acct.ID, StageNegotiation, 5000)
		o.Stage = StageClosedWon
		o.CloseReason = "won-fair"
		updated, err := s.UpdateOpportunity(ctx, tenantA, o)
		if err != nil {
			t.Fatalf("close won: %v", err)
		}

		events, err := s.OutboxEventsFor(ctx, tenantA, updated.ID)
		if err != nil {
			t.Fatalf("outbox events for %s: %v", updated.ID, err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events on closed_won, want exactly 1", len(events))
		}
		if events[0].Envelope.Type != EventOpportunityClosedWon {
			t.Fatalf("event type = %q, want %q", events[0].Envelope.Type, EventOpportunityClosedWon)
		}
		if events[0].Envelope.EntityType != EntityOpportunity || events[0].Envelope.EntityID != updated.ID {
			t.Fatalf("envelope identifies %s/%s, want %s/%s",
				events[0].Envelope.EntityType, events[0].Envelope.EntityID, EntityOpportunity, updated.ID)
		}

		var payload OpportunityClosedWonPayload
		if err := json.Unmarshal(events[0].Envelope.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.OpportunityID != updated.ID {
			t.Errorf("payload.OpportunityID = %q, want %q", payload.OpportunityID, updated.ID)
		}
		if payload.AccountID != acct.ID {
			t.Errorf("payload.AccountID = %q, want %q", payload.AccountID, acct.ID)
		}
		if payload.Amount != 5000 {
			t.Errorf("payload.Amount = %v, want 5000", payload.Amount)
		}
		if payload.OwnerID != "owner-1" {
			t.Errorf("payload.OwnerID = %q, want %q", payload.OwnerID, "owner-1")
		}
		if payload.CloseDate.IsZero() {
			t.Error("payload.CloseDate is zero")
		}

		// Re-saving the already-won deal must not enqueue a second event.
		updated.Name = "renamed after win"
		resaved, err := s.UpdateOpportunity(ctx, tenantA, updated)
		if err != nil {
			t.Fatalf("re-save of won opportunity: %v", err)
		}
		events, err = s.OutboxEventsFor(ctx, tenantA, resaved.ID)
		if err != nil {
			t.Fatalf("outbox events for %s: %v", resaved.ID, err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events after re-saving a won deal, want still exactly 1", len(events))
		}
	})

	t.Run("closed_lost enqueues zero events", func(t *testing.T) {
		o := mustCreateOpp(t, s, tenantA, acct.ID, StageNegotiation, 4000)
		o.Stage = StageClosedLost
		o.CloseReason = "lost-fair"
		updated, err := s.UpdateOpportunity(ctx, tenantA, o)
		if err != nil {
			t.Fatalf("close lost: %v", err)
		}
		events, err := s.OutboxEventsFor(ctx, tenantA, updated.ID)
		if err != nil {
			t.Fatalf("outbox events for %s: %v", updated.ID, err)
		}
		if len(events) != 0 {
			t.Fatalf("got %d events on closed_lost, want 0", len(events))
		}
	})

	t.Run("an ordinary forward transition enqueues zero events", func(t *testing.T) {
		o := mustCreateOpp(t, s, tenantA, acct.ID, StageProspecting, 3000)
		o.Stage = StageQualification
		updated, err := s.UpdateOpportunity(ctx, tenantA, o)
		if err != nil {
			t.Fatalf("advance stage: %v", err)
		}
		events, err := s.OutboxEventsFor(ctx, tenantA, updated.ID)
		if err != nil {
			t.Fatalf("outbox events for %s: %v", updated.ID, err)
		}
		if len(events) != 0 {
			t.Fatalf("got %d events on an ordinary transition, want 0", len(events))
		}
	})
}
