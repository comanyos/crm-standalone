package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// CSV import and export, accounts only in v1 (M4 — data productivity,
// internal/domain/import_export.go, docs/CSV_IMPORT_EXPORT.md).

func importJobToProto(v *domain.ImportJob) *crmv1.ImportJob {
	if v == nil {
		return nil
	}
	errs := make([]*crmv1.ImportRowError, 0, len(v.Errors))
	for _, e := range v.Errors {
		errs = append(errs, &crmv1.ImportRowError{RowNumber: boundedInt32(e.RowNumber), Message: e.Message})
	}
	return &crmv1.ImportJob{
		Id: v.ID, ObjectType: v.ObjectType, Status: v.Status, FileName: v.FileName, Mapping: v.Mapping,
		RowCount: boundedInt32(v.RowCount), CreatedRows: boundedInt32(v.CreatedRows), SkippedRows: boundedInt32(v.SkippedRows),
		Errors: errs, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
	}
}

func exportJobToProto(v *domain.ExportJob) *crmv1.ExportJob {
	if v == nil {
		return nil
	}
	return &crmv1.ExportJob{
		Id: v.ID, ObjectType: v.ObjectType, RowCount: boundedInt32(v.RowCount), RequestedBy: v.RequestedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt),
	}
}

// CreateImportJob validates and stores a dry-run preview. No Account is
// created — see ImportJob's doc comment.
func (s *CRMService) CreateImportJob(
	ctx context.Context, req *connect.Request[crmv1.CreateImportJobRequest],
) (*connect.Response[crmv1.CreateImportJobResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	job, err := s.store.CreateImportJob(ctx, scope, domain.CreateImportJobInput{
		ObjectType: req.Msg.GetObjectType(), FileName: req.Msg.GetFileName(), Content: req.Msg.GetContent(),
		Mapping: req.Msg.GetMapping(), CreatedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateImportJobResponse{Job: importJobToProto(job)}), nil
}

// GetImportJob reads one import job.
func (s *CRMService) GetImportJob(
	ctx context.Context, req *connect.Request[crmv1.GetImportJobRequest],
) (*connect.Response[crmv1.GetImportJobResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	job, err := s.store.GetImportJob(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetImportJobResponse{Job: importJobToProto(job)}), nil
}

// ListImportJobs returns the tenant's import history.
func (s *CRMService) ListImportJobs(
	ctx context.Context, req *connect.Request[crmv1.ListImportJobsRequest],
) (*connect.Response[crmv1.ListImportJobsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.ListImportJobs(ctx, scope, req.Msg.GetObjectType())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.ImportJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, importJobToProto(j))
	}
	return connect.NewResponse(&crmv1.ListImportJobsResponse{Jobs: out}), nil
}

// CommitImportJob creates one Account per structurally valid row. Idempotent
// on an already-completed job — see internal/domain/import_export.go.
func (s *CRMService) CommitImportJob(
	ctx context.Context, req *connect.Request[crmv1.CommitImportJobRequest],
) (*connect.Response[crmv1.CommitImportJobResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	job, err := s.store.CommitImportJob(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CommitImportJobResponse{Job: importJobToProto(job)}), nil
}

// ExportAccounts generates bounded CSV for the caller's own tenant and
// records an audit row. The file itself is never persisted.
func (s *CRMService) ExportAccounts(
	ctx context.Context, _ *connect.Request[crmv1.ExportAccountsRequest],
) (*connect.Response[crmv1.ExportAccountsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	job, content, err := s.store.CreateExportJob(ctx, scope, "account", principalSubject(ctx))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ExportAccountsResponse{
		Job: exportJobToProto(job), Content: content,
	}), nil
}

// ListExportJobs returns the tenant's export audit history.
func (s *CRMService) ListExportJobs(
	ctx context.Context, req *connect.Request[crmv1.ListExportJobsRequest],
) (*connect.Response[crmv1.ListExportJobsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.ListExportJobs(ctx, scope, req.Msg.GetObjectType())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.ExportJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, exportJobToProto(j))
	}
	return connect.NewResponse(&crmv1.ListExportJobsResponse{Jobs: out}), nil
}
