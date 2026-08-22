package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPipelineStages_AreTenantScopedAndValidated(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	tenantA := Scope{TenantID: "pipeline-a", OrgID: "org-a"}
	tenantB := Scope{TenantID: "pipeline-b", OrgID: "org-b"}

	pipeline, err := s.CreatePipeline(ctx, tenantA, &Pipeline{Name: "Sales"})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	stage, err := s.CreatePipelineStage(ctx, tenantA, &PipelineStage{
		PipelineID: pipeline.ID, Name: "Discovery", Position: 0, Probability: 10,
	})
	if err != nil {
		t.Fatalf("create stage: %v", err)
	}
	if stage.TerminalType != "open" {
		t.Fatalf("default terminal type = %q, want open", stage.TerminalType)
	}

	if _, err := s.ListPipelineStages(ctx, tenantB, pipeline.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant stage list error = %v, want ErrNotFound", err)
	}
	if _, err := s.CreatePipelineStage(ctx, tenantA, &PipelineStage{
		PipelineID: pipeline.ID, Name: "Bad win", Position: 1, Probability: 50, TerminalType: "won",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("won stage with non-100 probability error = %v, want ErrInvalid", err)
	}

	pipeline.ArchivedAt = time.Now().UTC()
	pipeline, err = s.UpdatePipeline(ctx, tenantA, pipeline)
	if err != nil {
		t.Fatalf("archive pipeline: %v", err)
	}
	if _, err := s.CreatePipelineStage(ctx, tenantA, &PipelineStage{
		PipelineID: pipeline.ID, Name: "Blocked", Position: 1, Probability: 25,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stage on archived pipeline error = %v, want ErrInvalid", err)
	}
}

func TestPipelineDefaultSwitchesWithinTenant(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := Scope{TenantID: "pipeline-default", OrgID: "org-default"}
	first, err := s.CreatePipeline(ctx, scope, &Pipeline{Name: "Direct", IsDefault: true})
	if err != nil {
		t.Fatalf("create first pipeline: %v", err)
	}
	second, err := s.CreatePipeline(ctx, scope, &Pipeline{Name: "Partners"})
	if err != nil {
		t.Fatalf("create second pipeline: %v", err)
	}
	second.IsDefault = true
	if _, err := s.UpdatePipeline(ctx, scope, second); err != nil {
		t.Fatalf("switch default: %v", err)
	}
	first, err = s.GetPipeline(ctx, scope, first.ID)
	if err != nil {
		t.Fatalf("read first pipeline: %v", err)
	}
	if first.IsDefault {
		t.Fatal("first pipeline remains default after switch")
	}
}

func TestOpportunityUsesConfiguredPipelineStages(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := Scope{TenantID: "configured-opportunity", OrgID: "configured-org"}
	account, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme", OwnerID: "owner"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	pipeline, err := s.CreatePipeline(ctx, scope, &Pipeline{Name: "Enterprise", IsDefault: true})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	for _, stage := range []*PipelineStage{
		{ID: "discovery", PipelineID: pipeline.ID, Name: "Discovery", Position: 0, Probability: 20},
		{ID: "won", PipelineID: pipeline.ID, Name: "Won", Position: 1, Probability: 100, TerminalType: "won", RequiresCloseReason: true},
	} {
		if _, err := s.CreatePipelineStage(ctx, scope, stage); err != nil {
			t.Fatalf("create stage: %v", err)
		}
	}
	opp, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: "New deal", AccountID: account.ID})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	if opp.PipelineID != pipeline.ID || opp.StageID != "discovery" || opp.Stage != "Discovery" {
		t.Fatalf("unexpected configured opportunity: %+v", opp)
	}
	opp.StageID, opp.CloseReason = "won", "Signed"
	updated, err := s.UpdateOpportunity(ctx, scope, opp)
	if err != nil {
		t.Fatalf("close opportunity: %v", err)
	}
	if updated.CloseDate.IsZero() {
		t.Fatal("configured closing stage did not stamp close date")
	}
}
