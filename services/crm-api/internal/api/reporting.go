package api

import (
	"context"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Dashboard and reporting (M4 — data productivity).
//
// GetDashboardReport composes several of the tenant's own bounded list
// reads into the pure aggregation functions in internal/domain/reporting.go.
// Pipelines, tasks, and leads are each read as one bounded page
// (reportPageSize, matching Store's own maxPageSize cap) rather than looped
// to exhaustion — a tenant with more than 500 open tasks or leads gets a
// dashboard scoped to its first page rather than an unbounded per-tenant
// scan. Forecast is the one exception: it reuses the existing
// domain.PipelineForecast, which already loops pages to completion as a
// deliberate whole-tenant rollup (forecast.go) — this handler does not
// re-bound something that already has its own bounded-per-page, looped
// design.
const reportPageSize = 500

// GetDashboardReport returns the tenant's pipeline, task, and lead-funnel
// summary, plus the default-weighted forecast.
func (s *CRMService) GetDashboardReport(
	ctx context.Context, _ *connect.Request[crmv1.GetDashboardReportRequest],
) (*connect.Response[crmv1.GetDashboardReportResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}

	pipelines, err := s.store.ListPipelines(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	opportunities, _, err := s.store.ListOpportunities(ctx, scope, domain.Page{Size: reportPageSize})
	if err != nil {
		return nil, connectErr(err)
	}
	tasks, _, err := s.store.ListTasks(ctx, scope, domain.Page{Size: reportPageSize}, domain.TaskFilter{}, domain.SortTaskDue)
	if err != nil {
		return nil, connectErr(err)
	}
	leads, _, err := s.store.ListLeads(ctx, scope, domain.Page{Size: reportPageSize})
	if err != nil {
		return nil, connectErr(err)
	}
	forecast, err := domain.PipelineForecast(ctx, s.store, scope, domain.ForecastOptions{})
	if err != nil {
		return nil, connectErr(err)
	}

	pipelineReports := make([]*crmv1.PipelineReport, 0, len(pipelines))
	for _, pipeline := range pipelines {
		stages, err := s.store.ListPipelineStages(ctx, scope, pipeline.ID)
		if err != nil {
			return nil, connectErr(err)
		}
		pipelineReports = append(pipelineReports, pipelineReportToProto(domain.BuildPipelineReport(pipeline, stages, opportunities)))
	}

	now := time.Now().UTC()
	report := &crmv1.DashboardReport{
		Pipelines:   pipelineReports,
		Tasks:       taskReportToProto(domain.BuildTaskReport(tasks, now)),
		Conversion:  conversionReportToProto(domain.BuildConversionReport(leads)),
		Forecast:    forecastToProto(forecast),
		GeneratedAt: timestamppb.New(now),
	}
	return connect.NewResponse(&crmv1.GetDashboardReportResponse{Report: report}), nil
}

func pipelineReportToProto(r domain.PipelineReport) *crmv1.PipelineReport {
	stages := make([]*crmv1.PipelineStageSummary, 0, len(r.Stages))
	for _, st := range r.Stages {
		stages = append(stages, &crmv1.PipelineStageSummary{
			StageId: st.StageID, StageName: st.StageName, TerminalType: st.TerminalType,
			Count: boundedInt32(st.Count), Amount: st.Amount,
		})
	}
	return &crmv1.PipelineReport{
		PipelineId: r.PipelineID, PipelineName: r.PipelineName, Stages: stages,
		WonCount: boundedInt32(r.WonCount), WonAmount: r.WonAmount,
		LostCount: boundedInt32(r.LostCount), LostAmount: r.LostAmount,
	}
}

func taskReportToProto(r domain.TaskReport) *crmv1.TaskSummary {
	return &crmv1.TaskSummary{
		OpenCount: boundedInt32(r.OpenCount), InProgressCount: boundedInt32(r.InProgressCount),
		CompletedCount: boundedInt32(r.CompletedCount), CancelledCount: boundedInt32(r.CancelledCount),
		OverdueCount: boundedInt32(r.OverdueCount),
	}
}

func conversionReportToProto(r domain.ConversionReport) *crmv1.ConversionFunnel {
	funnel := make([]*crmv1.LeadFunnelStage, 0, len(r.Funnel))
	for _, stage := range r.Funnel {
		funnel = append(funnel, &crmv1.LeadFunnelStage{Status: stage.Status, Count: boundedInt32(stage.Count)})
	}
	return &crmv1.ConversionFunnel{
		Funnel: funnel, TotalLeads: boundedInt32(r.TotalLeads), ConvertedLeads: boundedInt32(r.ConvertedLeads),
		ConversionRate: r.ConversionRate(),
	}
}

func forecastToProto(f *domain.Forecast) *crmv1.ForecastSummary {
	buckets := make([]*crmv1.ForecastBucket, 0, len(f.Buckets))
	for _, b := range f.Buckets {
		buckets = append(buckets, &crmv1.ForecastBucket{
			OwnerId: b.OwnerID, Stage: b.Stage, Count: boundedInt32(b.Count), Amount: b.Amount, Weighted: b.Weighted,
		})
	}
	return &crmv1.ForecastSummary{ByOwner: f.ByOwner, ByStage: f.ByStage, Total: f.Total, Buckets: buckets}
}
