package domain

import (
	"context"
	"sort"
)

// The in-memory half of CSV import/export (import_export.go).

// CreateImportJob validates and stores a dry-run preview. No Account is
// created — see NewImportJobPreview's doc comment.
func (s *MemStore) CreateImportJob(_ context.Context, scope Scope, in CreateImportJobInput) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	job, err := NewImportJobPreview(in)
	if err != nil {
		return nil, err
	}
	return s.importJob.create(scope, job)
}

// GetImportJob reads one import job.
func (s *MemStore) GetImportJob(_ context.Context, scope Scope, id string) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	return s.importJob.get(scope, id)
}

// ListImportJobs returns the tenant's import jobs, optionally narrowed to
// one object type, newest first.
func (s *MemStore) ListImportJobs(_ context.Context, scope Scope, objectType string) ([]*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	all, err := s.importJob.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*ImportJob, 0, len(all))
	for _, job := range all {
		if objectType != "" && job.ObjectType != objectType {
			continue
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CommitImportJob creates one Account per structurally valid row. Calling
// it again on a job already "completed" returns the stored result rather
// than re-running the commit — the idempotency CreateImportJob's own doc
// comment promises.
func (s *MemStore) CommitImportJob(ctx context.Context, scope Scope, id string) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.compound.Lock()
	defer s.compound.Unlock()
	job, err := s.importJob.get(scope, id)
	if err != nil {
		return nil, err
	}
	if job.Status == ImportStatusCompleted {
		return job, nil
	}
	created, skipped, errs, err := RunImportCommit(ctx, s, scope, job)
	if err != nil {
		return nil, err
	}
	job.Status = ImportStatusCompleted
	job.CreatedRows = created
	job.SkippedRows = skipped
	job.Errors = errs
	return s.importJob.update(scope, job)
}

// CreateExportJob generates CSV content for the tenant's accounts (bounded
// at maxExportRows) and records an audit row. The content is returned but
// never stored — see import_export.go's ExportJob doc comment.
func (s *MemStore) CreateExportJob(
	ctx context.Context, scope Scope, objectType, requestedBy string,
) (*ExportJob, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if objectType != "account" {
		return nil, "", ErrInvalid
	}
	accounts, _, err := s.ListAccounts(ctx, scope, Page{Size: maxExportRows}, AccountFilter{}, SortAccountName)
	if err != nil {
		return nil, "", err
	}
	content, err := AccountsToCSV(accounts)
	if err != nil {
		return nil, "", err
	}
	job, err := s.exportJob.create(scope, &ExportJob{
		ObjectType: objectType, RowCount: len(accounts), RequestedBy: requestedBy,
	})
	if err != nil {
		return nil, "", err
	}
	return job, content, nil
}

// ListExportJobs returns the tenant's export audit history, optionally
// narrowed to one object type, newest first.
func (s *MemStore) ListExportJobs(_ context.Context, scope Scope, objectType string) ([]*ExportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	all, err := s.exportJob.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*ExportJob, 0, len(all))
	for _, job := range all {
		if objectType != "" && job.ObjectType != objectType {
			continue
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
