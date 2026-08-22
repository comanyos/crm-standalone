package domain

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The revenue workflows have two Store implementations, and a rule that only
// one of them enforces is worse than no rule: MemStore is what the service
// runs on when DATABASE_URL is unset, and what internal/api's whole suite
// tests against — so a guarantee proven only in pgstore_revenue_workflow_test.go
// is a guarantee the API tier never actually exercises.
//
// These run the same assertions against both, which is what keeps the pair
// honest. The behaviour-in-depth (SQL, transactions, row locks) stays in the
// PgStore file; this is the contract both must meet.

// eachRevenueStore runs body against both stores.
//
// PgStore is resolved inside its own subtest, not while building a list of
// stores: livePgStore ends the test it is handed when no database is
// reachable, so constructing the pair up front skipped the *whole* test —
// MemStore half included. That silently removed the only half that can run
// without a database, which is the half covering what the service does with
// DATABASE_URL unset.
func eachRevenueStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

// TestLeadOptimisticConcurrencyIsTheSameInBothStores pins the contract
// UpdateAccount already had and leads did not: a non-zero UpdatedAt that
// disagrees with the stored record is ErrConflict, a zero one opts out.
func TestLeadOptimisticConcurrencyIsTheSameInBothStores(t *testing.T) {
	eachRevenueStore(t, func(t *testing.T, name string, s Store) {
		{
			ctx := context.Background()
			scope := revenueScope("parity-lead-" + name)

			lead, err := s.CreateLead(ctx, scope, &Lead{Company: "Parity", ContactEmail: "p@parity.example"})
			if err != nil {
				t.Fatalf("create lead: %v", err)
			}

			stale := *lead
			fresh := *lead
			fresh.Score = 1
			if _, err := s.UpdateLead(ctx, scope, &fresh); err != nil {
				t.Fatalf("first save: %v", err)
			}

			stale.Score = 2
			if _, err := s.UpdateLead(ctx, scope, &stale); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale save: %v, want ErrConflict", err)
			}
			stored, err := s.GetLead(ctx, scope, lead.ID)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if stored.Score != 1 {
				t.Fatalf("score = %d, want 1 — the refused save wrote", stored.Score)
			}

			optedOut := *stored
			optedOut.UpdatedAt = time.Time{}
			optedOut.Score = 3
			if _, err := s.UpdateLead(ctx, scope, &optedOut); err != nil {
				t.Fatalf("save without a version: %v, want it allowed", err)
			}
		}
	})
}

// TestOpportunityOptimisticConcurrencyIsTheSameInBothStores is the same
// contract for the opportunity, on the write that matters: a stage move.
func TestOpportunityOptimisticConcurrencyIsTheSameInBothStores(t *testing.T) {
	eachRevenueStore(t, func(t *testing.T, name string, s Store) {
		{
			ctx := context.Background()
			scope := revenueScope("parity-opp-" + name)

			_, opp := openOpportunity(ctx, t, s, scope, "Parity deal")

			stale := *opp
			fresh := *opp
			fresh.Amount = 111
			if _, err := s.UpdateOpportunity(ctx, scope, &fresh); err != nil {
				t.Fatalf("first save: %v", err)
			}

			stale.Stage = ""
			stale.StageID = StageQualification
			if _, err := s.UpdateOpportunity(ctx, scope, &stale); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale stage move: %v, want ErrConflict", err)
			}
			stored, err := s.GetOpportunity(ctx, scope, opp.ID)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if stored.StageID != StageProspecting {
				t.Fatalf("stage = %q, want %q — the refused move applied", stored.StageID, StageProspecting)
			}
			history, err := s.ListStageHistory(ctx, scope, opp.ID)
			if err != nil {
				t.Fatalf("list stage history: %v", err)
			}
			if len(history) != 0 {
				t.Fatalf("a refused move recorded %d history entries, want 0", len(history))
			}
		}
	})
}

// TestConcurrentConversionConvertsOnceInBothStores is the conversion race in
// both implementations. PgStore serializes on the lead row; MemStore, which
// has no rows to lock, serializes the compound write paths on one mutex. The
// observable guarantee has to be identical either way, because the API tier
// only ever sees this one.
func TestConcurrentConversionConvertsOnceInBothStores(t *testing.T) {
	eachRevenueStore(t, func(t *testing.T, name string, s Store) {
		{
			ctx := context.Background()
			scope := revenueScope("parity-race-" + name)

			lead := qualifiedLead(ctx, t, s, scope, "Parity Race Co", "buyer@parityrace.example")

			const callers = 4
			results := make([]*ConvertLeadResult, callers)
			errs := make([]error, callers)
			var start, done sync.WaitGroup
			start.Add(1)
			for i := 0; i < callers; i++ {
				done.Add(1)
				go func(i int) {
					defer done.Done()
					start.Wait()
					results[i], errs[i] = s.ConvertLead(ctx, scope, ConvertLeadInput{
						LeadID: lead.ID, CreateOpportunity: true,
					})
				}(i)
			}
			start.Done()
			done.Wait()

			for i, err := range errs {
				if err != nil {
					t.Fatalf("caller %d: %v", i, err)
				}
			}
			for i := 1; i < callers; i++ {
				if results[i].Opportunity.ID != results[0].Opportunity.ID {
					t.Errorf("caller %d converted to a different opportunity than caller 0", i)
				}
			}
			opps, _, err := s.ListOpportunities(ctx, scope, Page{})
			if err != nil {
				t.Fatalf("list opportunities: %v", err)
			}
			if len(opps) != 1 {
				t.Fatalf("%d concurrent conversions created %d opportunities, want 1", callers, len(opps))
			}
		}
	})
}

// TestConcurrentStageAdvanceRecordsOneMoveInBothStores is the stage-history
// half of the same parity argument.
func TestConcurrentStageAdvanceRecordsOneMoveInBothStores(t *testing.T) {
	eachRevenueStore(t, func(t *testing.T, name string, s Store) {
		{
			ctx := context.Background()
			scope := revenueScope("parity-stage-" + name)

			_, opp := openOpportunity(ctx, t, s, scope, "Parity race deal")

			const callers = 4
			errs := make([]error, callers)
			var start, done sync.WaitGroup
			start.Add(1)
			for i := 0; i < callers; i++ {
				done.Add(1)
				go func(i int) {
					defer done.Done()
					attempt := *opp
					attempt.UpdatedAt = time.Time{}
					attempt.Stage = ""
					attempt.StageID = StageQualification
					start.Wait()
					_, errs[i] = s.UpdateOpportunity(ctx, scope, &attempt)
				}(i)
			}
			start.Done()
			done.Wait()

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
				t.Fatalf("%d concurrent advances recorded %d history entries, want 1: %+v",
					callers, len(history), history)
			}
		}
	})
}
