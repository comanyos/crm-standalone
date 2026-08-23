package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Archiving a pipeline or stage that still holds open deals used to be
// accepted, and it stranded every deal left behind: activePipelineStage
// resolves a deal's current stage before any other rule and refuses an
// archived one, so the deal could not advance, could not be closed, and could
// not even be renamed. It stayed readable and kept its amount, so it went on
// counting toward anything summed later, with no way back short of SQL.
//
// These run against both stores, because MemStore is what the service runs on
// with DATABASE_URL unset and what internal/api's suite tests against — a
// guard only PgStore enforces is a guard the API tier never exercises.

// eachArchivalStore runs body against both stores. PgStore is resolved inside
// its own subtest rather than while building a list, so a machine with no
// database skips that half and still runs the MemStore one — the half that
// covers what the service does when DATABASE_URL is unset.
func eachArchivalStore(t *testing.T, body func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, livePgStore(t)) })
}

// openDeal creates an opportunity on the tenant's default pipeline at its
// first open stage, returning the deal and the pipeline it landed on.
func openDeal(ctx context.Context, t *testing.T, s Store, scope Scope, name string) (*Opportunity, *Pipeline) {
	t.Helper()
	account, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme", OwnerID: "user-1"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	opportunity, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: name, AccountID: account.ID, Amount: 50000})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	pipeline, err := s.GetPipeline(ctx, scope, opportunity.PipelineID)
	if err != nil {
		t.Fatalf("get pipeline: %v", err)
	}
	return opportunity, pipeline
}

func stageNamed(ctx context.Context, t *testing.T, s Store, scope Scope, pipelineID, stageID string) *PipelineStage {
	t.Helper()
	stages, err := s.ListPipelineStages(ctx, scope, pipelineID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}
	for _, stage := range stages {
		if stage.ID == stageID {
			return stage
		}
	}
	t.Fatalf("stage %q not found", stageID)
	return nil
}

func TestArchivingAPipelineWithOpenDealsIsRefused(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archive-pipeline")
		_, pipeline := openDeal(ctx, t, s, scope, "Big renewal")

		archived := *pipeline
		archived.IsDefault = false
		archived.ArchivedAt = time.Now().UTC()
		_, err := s.UpdatePipeline(ctx, scope, &archived)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("archiving an occupied pipeline: %v, want ErrConflict", err)
		}
		// The message has to name what is in the way, or the administrator is
		// told "no" with nowhere to go.
		if !strings.Contains(err.Error(), "Big renewal") {
			t.Errorf("refusal does not name the blocking deal: %v", err)
		}

		// And the refusal wrote nothing: the pipeline is still live, so the
		// deal is still workable.
		stored, err := s.GetPipeline(ctx, scope, pipeline.ID)
		if err != nil {
			t.Fatalf("re-read pipeline: %v", err)
		}
		if !stored.ArchivedAt.IsZero() {
			t.Fatalf("the refused archival applied anyway")
		}
	})
}

func TestArchivingAStageWithOpenDealsIsRefused(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archive-stage")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Big renewal")

		occupied := *stageNamed(ctx, t, s, scope, pipeline.ID, opportunity.StageID)
		occupied.ArchivedAt = time.Now().UTC()
		_, err := s.UpdatePipelineStage(ctx, scope, &occupied)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("archiving an occupied stage: %v, want ErrConflict", err)
		}
		if !strings.Contains(err.Error(), "Big renewal") {
			t.Errorf("refusal does not name the blocking deal: %v", err)
		}

		stages, err := s.ListPipelineStages(ctx, scope, pipeline.ID)
		if err != nil {
			t.Fatalf("list stages: %v", err)
		}
		for _, stage := range stages {
			if stage.ID == opportunity.StageID && !stage.ArchivedAt.IsZero() {
				t.Fatalf("the refused archival applied anyway")
			}
		}
	})
}

// The remedy the refusal points at has to actually work: move the deal off,
// then the stage archives.
func TestAStageArchivesOnceItsDealsHaveMovedOn(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archive-after-move")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Big renewal")
		vacated := opportunity.StageID

		advance := *opportunity
		advance.UpdatedAt = time.Time{}
		advance.Stage = ""
		advance.StageID = StageQualification
		if _, err := s.UpdateOpportunity(ctx, scope, &advance); err != nil {
			t.Fatalf("advance the deal: %v", err)
		}

		stage := *stageNamed(ctx, t, s, scope, pipeline.ID, vacated)
		stage.ArchivedAt = time.Now().UTC()
		if _, err := s.UpdatePipelineStage(ctx, scope, &stage); err != nil {
			t.Fatalf("archiving a vacated stage: %v, want it allowed", err)
		}
	})
}

// A closed deal cannot be moved off — every transition out of a terminal
// stage is refused — so counting it would make any stage that has ever closed
// a deal permanently unarchivable, and the remedy the refusal names would be
// impossible to follow.
func TestClosedDealsDoNotBlockArchival(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archive-closed")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Lost one")

		closed := *opportunity
		closed.UpdatedAt = time.Time{}
		closed.Stage = ""
		closed.StageID = StageClosedLost
		closed.CloseReason = "went with a competitor"
		if _, err := s.UpdateOpportunity(ctx, scope, &closed); err != nil {
			t.Fatalf("close the deal: %v", err)
		}

		archived := *pipeline
		archived.IsDefault = false
		archived.ArchivedAt = time.Now().UTC()
		if _, err := s.UpdatePipeline(ctx, scope, &archived); err != nil {
			t.Fatalf("archiving a pipeline holding only closed deals: %v, want it allowed", err)
		}
	})
}

// An empty pipeline archives, and a pipeline that is already archived can
// still be written — otherwise an administrator could not rename one, or
// correct the flag they had just set.
func TestArchivalOfAnUnoccupiedPipelineStillWorks(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archive-empty")
		pipeline, err := s.CreatePipeline(ctx, scope, &Pipeline{Name: "Partner sales"})
		if err != nil {
			t.Fatalf("create pipeline: %v", err)
		}

		archived := *pipeline
		archived.ArchivedAt = time.Now().UTC()
		stored, err := s.UpdatePipeline(ctx, scope, &archived)
		if err != nil {
			t.Fatalf("archive an empty pipeline: %v", err)
		}
		if stored.ArchivedAt.IsZero() {
			t.Fatal("the pipeline did not archive")
		}

		renamed := *stored
		renamed.Name = "Partner sales (retired)"
		if _, err := s.UpdatePipeline(ctx, scope, &renamed); err != nil {
			t.Fatalf("rename an already-archived pipeline: %v, want it allowed", err)
		}
	})
}

// Archiving a closing stage is allowed — closed deals do not block it — and
// that used to freeze every deal standing on it: the close reason could not
// be corrected and the deal could not even be renamed, while it stayed
// readable and kept its amount. Archival stops arrivals; it does not freeze
// the history already there.
func TestAClosedDealStaysEditableAfterItsStageIsArchived(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archived-stage-edit")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Lost deal")

		closing := *opportunity
		closing.UpdatedAt = time.Time{}
		closing.Stage = ""
		closing.StageID = StageClosedLost
		closing.CloseReason = "went with a competitr" // typo, corrected below
		closed, err := s.UpdateOpportunity(ctx, scope, &closing)
		if err != nil {
			t.Fatalf("close the deal: %v", err)
		}

		stage := *stageNamed(ctx, t, s, scope, pipeline.ID, StageClosedLost)
		stage.ArchivedAt = time.Now().UTC()
		if _, err := s.UpdatePipelineStage(ctx, scope, &stage); err != nil {
			t.Fatalf("archive the closing stage: %v, want it allowed", err)
		}

		fix := *closed
		fix.UpdatedAt = time.Time{}
		fix.Stage = ""
		fix.Name = "Lost deal (Acme)"
		fix.CloseReason = "went with a competitor"
		fixed, err := s.UpdateOpportunity(ctx, scope, &fix)
		if err != nil {
			t.Fatalf("correct a closed deal on an archived stage: %v, want it allowed", err)
		}
		if fixed.CloseReason != "went with a competitor" || fixed.Name != "Lost deal (Acme)" {
			t.Fatalf("the correction did not stick: %+v", fixed)
		}
		if fixed.StageID != StageClosedLost {
			t.Fatalf("stage moved to %q; an edit in place must not relocate the deal", fixed.StageID)
		}
	})
}

// The lenient resolution applies to where a deal already is, not to where it
// is going. An archived stage is still not a destination.
func TestAnArchivedStageIsStillNotADestination(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archived-not-destination")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Live deal")

		// Vacate and archive the stage after this one.
		advance := *opportunity
		advance.UpdatedAt = time.Time{}
		advance.Stage = ""
		advance.StageID = StageQualification
		moved, err := s.UpdateOpportunity(ctx, scope, &advance)
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		retired := *stageNamed(ctx, t, s, scope, pipeline.ID, StageProposal)
		retired.ArchivedAt = time.Now().UTC()
		if _, err := s.UpdatePipelineStage(ctx, scope, &retired); err != nil {
			t.Fatalf("archive an empty stage: %v", err)
		}

		attempt := *moved
		attempt.UpdatedAt = time.Time{}
		attempt.Stage = ""
		attempt.StageID = StageProposal
		if _, err := s.UpdateOpportunity(ctx, scope, &attempt); err == nil {
			t.Fatal("moving a deal onto an archived stage was allowed")
		}
	})
}

// A closed deal is still closed. Loosening the resolution must not have
// loosened the state machine with it.
func TestAnArchivedTerminalStageStillCannotBeReopened(t *testing.T) {
	eachArchivalStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		scope := revenueScope("archived-no-reopen")
		opportunity, pipeline := openDeal(ctx, t, s, scope, "Done deal")

		closing := *opportunity
		closing.UpdatedAt = time.Time{}
		closing.Stage = ""
		closing.StageID = StageClosedWon
		closing.CloseReason = "signed"
		closed, err := s.UpdateOpportunity(ctx, scope, &closing)
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		stage := *stageNamed(ctx, t, s, scope, pipeline.ID, StageClosedWon)
		stage.ArchivedAt = time.Now().UTC()
		if _, err := s.UpdatePipelineStage(ctx, scope, &stage); err != nil {
			t.Fatalf("archive the closing stage: %v", err)
		}

		reopen := *closed
		reopen.UpdatedAt = time.Time{}
		reopen.Stage = ""
		reopen.StageID = StageQualification
		if _, err := s.UpdateOpportunity(ctx, scope, &reopen); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("reopening from an archived terminal stage: %v, want ErrInvalidTransition", err)
		}
	})
}
