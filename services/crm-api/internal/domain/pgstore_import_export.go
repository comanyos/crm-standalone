package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of CSV import/export (import_export.go).

const importJobColumns = `id, object_type, status, file_name, content, mapping,
	row_count, created_rows, skipped_rows, errors, created_by, created_at, updated_at`

func scanImportJob(row pgx.Row) (*ImportJob, error) {
	var (
		v       ImportJob
		mapping []byte
		rowErrs []byte
	)
	if err := row.Scan(&v.ID, &v.ObjectType, &v.Status, &v.FileName, &v.Content, &mapping,
		&v.RowCount, &v.CreatedRows, &v.SkippedRows, &rowErrs, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if len(mapping) > 0 {
		if err := json.Unmarshal(mapping, &v.Mapping); err != nil {
			return nil, fmt.Errorf("decode import mapping: %w", err)
		}
	}
	if len(rowErrs) > 0 {
		if err := json.Unmarshal(rowErrs, &v.Errors); err != nil {
			return nil, fmt.Errorf("decode import errors: %w", err)
		}
	}
	return &v, nil
}

// CreateImportJob validates and stores a dry-run preview. No Account is
// created — see NewImportJobPreview's doc comment.
func (s *PgStore) CreateImportJob(ctx context.Context, scope Scope, in CreateImportJobInput) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	job, err := NewImportJobPreview(in)
	if err != nil {
		return nil, err
	}
	if job.ID == "" {
		job.ID = newID()
	}
	mapping, err := json.Marshal(job.Mapping)
	if err != nil {
		return nil, err
	}
	rowErrs, err := marshalImportErrors(job.Errors)
	if err != nil {
		return nil, err
	}
	now := nowStamp()
	out, err := scanImportJob(s.db.QueryRow(ctx, scope,
		`INSERT INTO import_jobs
		     (tenant_id, id, object_type, status, file_name, content, mapping,
		      row_count, created_rows, skipped_rows, errors, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		 RETURNING `+importJobColumns,
		scope.TenantID, job.ID, job.ObjectType, job.Status, job.FileName, job.Content, mapping,
		job.RowCount, job.CreatedRows, job.SkippedRows, rowErrs, job.CreatedBy, now))
	if err != nil {
		return nil, fmt.Errorf("create import job: %w", err)
	}
	return out, nil
}

func marshalImportErrors(errs []ImportRowError) ([]byte, error) {
	if errs == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(errs)
}

// GetImportJob reads one import job.
func (s *PgStore) GetImportJob(ctx context.Context, scope Scope, id string) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanImportJob(s.db.QueryRow(ctx, scope,
		`SELECT `+importJobColumns+` FROM import_jobs WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get import job: %w", err)
	}
	return out, nil
}

// ListImportJobs returns the tenant's import jobs, optionally narrowed to
// one object type, newest first.
func (s *PgStore) ListImportJobs(ctx context.Context, scope Scope, objectType string) ([]*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+importJobColumns+` FROM import_jobs
		  WHERE tenant_id = $1 AND ($2::text = '' OR object_type = $2)
		  ORDER BY created_at DESC, id`,
		scope.TenantID, objectType)
	if err != nil {
		return nil, fmt.Errorf("list import jobs: %w", err)
	}
	defer rows.Close()

	out := make([]*ImportJob, 0, 8)
	for rows.Next() {
		job, err := scanImportJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan import job: %w", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// CommitImportJob creates one Account per structurally valid row. Calling
// it again on a job already "completed" returns the stored result rather
// than re-running the commit.
func (s *PgStore) CommitImportJob(ctx context.Context, scope Scope, id string) (*ImportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	var out *ImportJob
	err := s.db.transaction(ctx, scope, func(txCtx context.Context) error {
		job, scanErr := scanImportJob(s.db.QueryRow(txCtx, scope,
			`SELECT `+importJobColumns+` FROM import_jobs
			  WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, scope.TenantID, id))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return fmt.Errorf("lock import job: %w", scanErr)
		}
		if job.Status == ImportStatusCompleted {
			out = job
			return nil
		}
		created, skipped, errs, runErr := RunImportCommit(txCtx, s, scope, job)
		if runErr != nil {
			return runErr
		}
		rowErrs, marshalErr := marshalImportErrors(errs)
		if marshalErr != nil {
			return marshalErr
		}
		out, scanErr = scanImportJob(s.db.QueryRow(txCtx, scope,
			`UPDATE import_jobs SET status = $3, created_rows = $4, skipped_rows = $5, errors = $6, updated_at = $7
			  WHERE tenant_id = $1 AND id = $2
			RETURNING `+importJobColumns,
			scope.TenantID, id, ImportStatusCompleted, created, skipped, rowErrs, nowStamp()))
		if scanErr != nil {
			return fmt.Errorf("commit import job: %w", scanErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const exportJobColumns = `id, object_type, row_count, requested_by, created_at`

func scanExportJob(row pgx.Row) (*ExportJob, error) {
	var v ExportJob
	if err := row.Scan(&v.ID, &v.ObjectType, &v.RowCount, &v.RequestedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateExportJob generates CSV content for the tenant's accounts (bounded
// at maxExportRows) and records an audit row. The content is returned but
// never stored.
func (s *PgStore) CreateExportJob(
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
	job, err := scanExportJob(s.db.QueryRow(ctx, scope,
		`INSERT INTO export_jobs (tenant_id, id, object_type, row_count, requested_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING `+exportJobColumns,
		scope.TenantID, newID(), objectType, len(accounts), requestedBy, nowStamp()))
	if err != nil {
		return nil, "", fmt.Errorf("create export job: %w", err)
	}
	return job, content, nil
}

// ListExportJobs returns the tenant's export audit history, optionally
// narrowed to one object type, newest first.
func (s *PgStore) ListExportJobs(ctx context.Context, scope Scope, objectType string) ([]*ExportJob, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+exportJobColumns+` FROM export_jobs
		  WHERE tenant_id = $1 AND ($2::text = '' OR object_type = $2)
		  ORDER BY created_at DESC, id`,
		scope.TenantID, objectType)
	if err != nil {
		return nil, fmt.Errorf("list export jobs: %w", err)
	}
	defer rows.Close()

	out := make([]*ExportJob, 0, 8)
	for rows.Next() {
		job, err := scanExportJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan export job: %w", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}
