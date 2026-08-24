package domain

import (
	"testing"
	"time"
)

// TestBuildPipelineReport proves stage counts/amounts are scoped to their
// own pipeline and stage, and that won/lost totals come from
// PipelineStage.TerminalType rather than a stage's name or position.
func TestBuildPipelineReport(t *testing.T) {
	pipeline := &Pipeline{ID: "pipe-1", Name: "Sales"}
	stages := []*PipelineStage{
		{ID: "stage-open", Name: "Qualifying", TerminalType: "open"},
		{ID: "stage-won", Name: "Closed Won", TerminalType: "won"},
		{ID: "stage-lost", Name: "Closed Lost", TerminalType: "lost"},
	}
	opportunities := []*Opportunity{
		{ID: "opp-1", PipelineID: "pipe-1", StageID: "stage-open", Amount: 100},
		{ID: "opp-2", PipelineID: "pipe-1", StageID: "stage-won", Amount: 250},
		{ID: "opp-3", PipelineID: "pipe-1", StageID: "stage-won", Amount: 50},
		{ID: "opp-4", PipelineID: "pipe-1", StageID: "stage-lost", Amount: 75},
		// Belongs to a different pipeline entirely — must not be counted.
		{ID: "opp-5", PipelineID: "pipe-2", StageID: "stage-open", Amount: 999},
		// Names a stage that is not this pipeline's own — dropped, not panicked on.
		{ID: "opp-6", PipelineID: "pipe-1", StageID: "stage-unknown", Amount: 999},
	}

	report := BuildPipelineReport(pipeline, stages, opportunities)

	if report.PipelineID != "pipe-1" || report.PipelineName != "Sales" {
		t.Fatalf("report identity = %+v", report)
	}
	if len(report.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(report.Stages))
	}
	byID := map[string]PipelineStageSummary{}
	for _, s := range report.Stages {
		byID[s.StageID] = s
	}
	if got := byID["stage-open"]; got.Count != 1 || got.Amount != 100 {
		t.Fatalf("open stage = %+v", got)
	}
	if got := byID["stage-won"]; got.Count != 2 || got.Amount != 300 {
		t.Fatalf("won stage = %+v", got)
	}
	if got := byID["stage-lost"]; got.Count != 1 || got.Amount != 75 {
		t.Fatalf("lost stage = %+v", got)
	}
	if report.WonCount != 2 || report.WonAmount != 300 {
		t.Fatalf("won totals = %d/%v, want 2/300", report.WonCount, report.WonAmount)
	}
	if report.LostCount != 1 || report.LostAmount != 75 {
		t.Fatalf("lost totals = %d/%v, want 1/75", report.LostCount, report.LostAmount)
	}
}

// TestBuildTaskReport proves status counts are exhaustive and that overdue
// only counts open/in-progress tasks whose due date is in the past — a
// completed task past its due date is not "overdue".
func TestBuildTaskReport(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)

	tasks := []*Task{
		{Status: TaskOpen, DueAt: past},       // overdue
		{Status: TaskOpen, DueAt: future},     // not overdue
		{Status: TaskOpen},                    // no due date — not overdue
		{Status: TaskInProgress, DueAt: past}, // overdue
		{Status: TaskCompleted, DueAt: past},  // completed — never overdue
		{Status: TaskCancelled, DueAt: past},  // cancelled — never overdue
	}

	report := BuildTaskReport(tasks, now)

	if report.OpenCount != 3 {
		t.Fatalf("OpenCount = %d, want 3", report.OpenCount)
	}
	if report.InProgressCount != 1 {
		t.Fatalf("InProgressCount = %d, want 1", report.InProgressCount)
	}
	if report.CompletedCount != 1 {
		t.Fatalf("CompletedCount = %d, want 1", report.CompletedCount)
	}
	if report.CancelledCount != 1 {
		t.Fatalf("CancelledCount = %d, want 1", report.CancelledCount)
	}
	if report.OverdueCount != 2 {
		t.Fatalf("OverdueCount = %d, want 2", report.OverdueCount)
	}
}

// TestBuildConversionReport proves the funnel is reported in lifecycle
// order regardless of input order, and that the rate divides converted by
// total.
func TestBuildConversionReport(t *testing.T) {
	leads := []*Lead{
		{Status: LeadStatusConverted},
		{Status: LeadStatusNew},
		{Status: LeadStatusNew},
		{Status: LeadStatusWorking},
		{Status: LeadStatusQualified},
		{Status: LeadStatusDisqualified},
	}

	report := BuildConversionReport(leads)

	if report.TotalLeads != 6 || report.ConvertedLeads != 1 {
		t.Fatalf("totals = %d/%d, want 6/1", report.TotalLeads, report.ConvertedLeads)
	}
	wantOrder := []string{LeadStatusNew, LeadStatusWorking, LeadStatusQualified, LeadStatusConverted, LeadStatusDisqualified}
	if len(report.Funnel) != len(wantOrder) {
		t.Fatalf("funnel stages = %d, want %d", len(report.Funnel), len(wantOrder))
	}
	for i, status := range wantOrder {
		if report.Funnel[i].Status != status {
			t.Fatalf("funnel[%d].Status = %q, want %q", i, report.Funnel[i].Status, status)
		}
	}
	counts := map[string]int{}
	for _, stage := range report.Funnel {
		counts[stage.Status] = stage.Count
	}
	if counts[LeadStatusNew] != 2 {
		t.Fatalf("new count = %d, want 2", counts[LeadStatusNew])
	}
	if counts[LeadStatusConverted] != 1 {
		t.Fatalf("converted count = %d, want 1", counts[LeadStatusConverted])
	}

	rate := report.ConversionRate()
	if rate < 0.1666 || rate > 0.1667 {
		t.Fatalf("ConversionRate() = %v, want ~1/6", rate)
	}
}

// TestConversionRateWithNoLeads proves a zero-lead tenant gets 0, not NaN
// or a division panic.
func TestConversionRateWithNoLeads(t *testing.T) {
	report := BuildConversionReport(nil)
	if report.ConversionRate() != 0 {
		t.Fatalf("ConversionRate() = %v, want 0", report.ConversionRate())
	}
}
