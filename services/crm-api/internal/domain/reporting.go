package domain

import "time"

// Dashboard and reporting (M4 — Data productivity).
//
// Canonical metric semantics — the decisions the M4 goal asked to be named
// before building on top of them:
//
//   - Currency. Amounts are the tenant's own raw numbers
//     (Opportunity.Amount), summed with no currency code, conversion, or
//     rounding beyond what float64 already carries — the same "no currency
//     field" limitation Opportunity and Account already have. A tenant
//     recording deals in more than one currency gets a total that mixes
//     them; that decision (task.md's "Required external decisions" table:
//     "Currency, fiscal calendar, quota, forecast rules") has not been
//     made, so this report does not pretend to have made it.
//   - Dates and time zones. Every boundary ("overdue" is due_at before now)
//     compares in UTC, matching nowStamp()'s convention across this
//     codebase. There is no per-tenant or per-user time zone setting, so
//     "overdue as of midnight" is not a question this report can answer
//     for a specific calendar day in a specific zone.
//   - Pipeline status. "open"/"won"/"lost" is PipelineStage.TerminalType,
//     the same field pipeline_test.go and transitions.go already treat as
//     authoritative — never inferred from a stage's name or position.
//   - Forecast. Not implemented. A weighted-pipeline forecast needs
//     probability-by-stage rules a product owner has not set (the same
//     open decision the currency note above cites), so this report offers
//     none rather than a number nobody agreed the formula for.
//
// This file holds pure aggregation over already-fetched records — no store
// access, so it is unit-testable without a live database or a MemStore. The
// composition (paging through a tenant's pipelines, opportunities, tasks and
// leads to build the inputs) lives in internal/api/reporting.go, bounded the
// same way every other list this session added is bounded: one page, not an
// unbounded per-tenant scan.

// PipelineStageSummary is one stage's open pipeline: how many opportunities
// sit in it and their combined amount.
type PipelineStageSummary struct {
	StageID      string
	StageName    string
	TerminalType string
	Count        int
	Amount       float64
}

// PipelineReport is one pipeline's shape: every stage's summary, plus the
// won/lost totals that summarize the pipeline's outcome so far.
type PipelineReport struct {
	PipelineID   string
	PipelineName string
	Stages       []PipelineStageSummary
	WonCount     int
	WonAmount    float64
	LostCount    int
	LostAmount   float64
}

// TaskReport counts a tenant's tasks by status, plus how many open ones are
// overdue as of the report's generation time.
type TaskReport struct {
	OpenCount       int
	InProgressCount int
	CompletedCount  int
	CancelledCount  int
	OverdueCount    int
}

// LeadFunnelStage is one status's count in the lead lifecycle.
type LeadFunnelStage struct {
	Status string
	Count  int
}

// ConversionReport is the lead funnel: how many leads are at each lifecycle
// status, and the conversion rate (converted / total) it implies.
type ConversionReport struct {
	Funnel         []LeadFunnelStage
	TotalLeads     int
	ConvertedLeads int
}

// ConversionRate returns ConvertedLeads/TotalLeads, or 0 for a tenant with
// no leads yet — a zero-lead tenant has an undefined rate, not a 0% one,
// but 0 is the only value a caller can render without a special case for
// "no data yet."
func (r ConversionReport) ConversionRate() float64 {
	if r.TotalLeads == 0 {
		return 0
	}
	return float64(r.ConvertedLeads) / float64(r.TotalLeads)
}

// DashboardReport is the whole first-cut dashboard: one entry per pipeline,
// the task summary, and the lead conversion funnel.
type DashboardReport struct {
	Pipelines   []PipelineReport
	Tasks       TaskReport
	Conversion  ConversionReport
	GeneratedAt time.Time
}

// BuildPipelineReport aggregates one pipeline's stages and opportunities.
// stages must already be this pipeline's own (ListPipelineStages' result);
// opportunities may be the tenant's whole fetched page and is filtered to
// this pipeline's own here, so a caller does not need to pre-split them.
func BuildPipelineReport(pipeline *Pipeline, stages []*PipelineStage, opportunities []*Opportunity) PipelineReport {
	report := PipelineReport{PipelineID: pipeline.ID, PipelineName: pipeline.Name}

	// Stages is preallocated to its final length up front: byStage below
	// holds pointers into its backing array, and a later append growing
	// past capacity would reallocate and silently orphan those pointers.
	report.Stages = make([]PipelineStageSummary, len(stages))
	byStage := make(map[string]*PipelineStageSummary, len(stages))
	for i, stage := range stages {
		report.Stages[i] = PipelineStageSummary{StageID: stage.ID, StageName: stage.Name, TerminalType: stage.TerminalType}
		byStage[stage.ID] = &report.Stages[i]
	}

	for _, opp := range opportunities {
		if opp.PipelineID != pipeline.ID {
			continue
		}
		summary, ok := byStage[opp.StageID]
		if !ok {
			continue
		}
		summary.Count++
		summary.Amount += opp.Amount
		switch summary.TerminalType {
		case "won":
			report.WonCount++
			report.WonAmount += opp.Amount
		case "lost":
			report.LostCount++
			report.LostAmount += opp.Amount
		}
	}
	return report
}

// BuildTaskReport counts tasks by status and how many open ones are
// overdue as of now.
func BuildTaskReport(tasks []*Task, now time.Time) TaskReport {
	var report TaskReport
	for _, t := range tasks {
		switch t.Status {
		case TaskOpen:
			report.OpenCount++
		case TaskInProgress:
			report.InProgressCount++
		case TaskCompleted:
			report.CompletedCount++
		case TaskCancelled:
			report.CancelledCount++
		}
		if (t.Status == TaskOpen || t.Status == TaskInProgress) && !t.DueAt.IsZero() && t.DueAt.Before(now) {
			report.OverdueCount++
		}
	}
	return report
}

// leadFunnelOrder is the lifecycle order the funnel is reported in — the
// same sequence leadStatusTransitions (lead_lifecycle.go) allows moving
// through, not alphabetical.
var leadFunnelOrder = []string{LeadStatusNew, LeadStatusWorking, LeadStatusQualified, LeadStatusConverted, LeadStatusDisqualified}

// BuildConversionReport counts leads by lifecycle status.
func BuildConversionReport(leads []*Lead) ConversionReport {
	counts := make(map[string]int, len(leadFunnelOrder))
	var total, converted int
	for _, lead := range leads {
		counts[lead.Status]++
		total++
		if lead.Status == LeadStatusConverted {
			converted++
		}
	}
	report := ConversionReport{TotalLeads: total, ConvertedLeads: converted}
	for _, status := range leadFunnelOrder {
		report.Funnel = append(report.Funnel, LeadFunnelStage{Status: status, Count: counts[status]})
	}
	return report
}
