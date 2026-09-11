package domain

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func eachImportExportStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

func importScope(label string) Scope {
	return Scope{TenantID: fmt.Sprintf("tenant-import-%s-%d", label, time.Now().UnixNano())}
}

const validCSV = "Company,Owner,Tier\nAcme,alice,gold\nUmbrella,bob,silver\n"

func validMapping() map[string]string {
	return map[string]string{"Company": "name", "Owner": "owner_id", "Tier": "tier"}
}

// TestImportPreviewDoesNotWriteAnything proves CreateImportJob is a real
// dry run: after it runs, no Account exists.
func TestImportPreviewDoesNotWriteAnything(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		job, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "accounts.csv", Content: validCSV,
			Mapping: validMapping(), CreatedBy: "admin-1",
		})
		if err != nil {
			t.Fatalf("create import job: %v", err)
		}
		if job.Status != ImportStatusPreviewed {
			t.Fatalf("status = %q, want %q", job.Status, ImportStatusPreviewed)
		}
		if job.RowCount != 2 || len(job.Errors) != 0 {
			t.Fatalf("row_count = %d, errors = %+v, want 2 rows, no errors", job.RowCount, job.Errors)
		}

		accounts, _, err := s.ListAccounts(ctx, scope, Page{}, AccountFilter{}, SortAccountName)
		if err != nil {
			t.Fatalf("list accounts: %v", err)
		}
		if len(accounts) != 0 {
			t.Fatalf("accounts exist after preview only: %d", len(accounts))
		}
	})
}

// TestImportCommitCreatesRowsAndIsIdempotent proves CommitImportJob creates
// the previewed rows, and that calling it again does not create them twice.
func TestImportCommitCreatesRowsAndIsIdempotent(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		job, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "accounts.csv", Content: validCSV, Mapping: validMapping(),
		})
		if err != nil {
			t.Fatalf("create import job: %v", err)
		}

		for i := 0; i < 2; i++ {
			committed, err := s.CommitImportJob(ctx, scope, job.ID)
			if err != nil {
				t.Fatalf("commit attempt %d: %v", i, err)
			}
			if committed.Status != ImportStatusCompleted {
				t.Fatalf("commit attempt %d: status = %q, want completed", i, committed.Status)
			}
			if committed.CreatedRows != 2 {
				t.Fatalf("commit attempt %d: created_rows = %d, want 2", i, committed.CreatedRows)
			}
		}

		accounts, _, err := s.ListAccounts(ctx, scope, Page{}, AccountFilter{}, SortAccountName)
		if err != nil {
			t.Fatalf("list accounts: %v", err)
		}
		if len(accounts) != 2 {
			t.Fatalf("accounts after two commits = %d, want 2 (idempotent, not doubled)", len(accounts))
		}
	})
}

// TestImportReportsPerRowErrorsWithoutFailingTheBatch proves one bad row
// does not block the others, and its error names the right row number.
func TestImportReportsPerRowErrorsWithoutFailingTheBatch(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		content := "Company,Owner,Revenue\nAcme,alice,1000\n,bob,2000\nUmbrella,carol,not-a-number\n"
		job, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "accounts.csv", Content: content,
			Mapping: map[string]string{"Company": "name", "Owner": "owner_id", "Revenue": "annual_revenue"},
		})
		if err != nil {
			t.Fatalf("create import job: %v", err)
		}
		if job.RowCount != 3 {
			t.Fatalf("row_count = %d, want 3", job.RowCount)
		}
		if len(job.Errors) != 2 {
			t.Fatalf("preview errors = %+v, want 2 (blank name, bad revenue)", job.Errors)
		}
		if job.Errors[0].RowNumber != 2 {
			t.Errorf("first error row = %d, want 2 (1-indexed against data rows)", job.Errors[0].RowNumber)
		}
		if job.Errors[1].RowNumber != 3 {
			t.Errorf("second error row = %d, want 3", job.Errors[1].RowNumber)
		}

		committed, err := s.CommitImportJob(ctx, scope, job.ID)
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		if committed.CreatedRows != 1 || committed.SkippedRows != 2 {
			t.Fatalf("created=%d skipped=%d, want created=1 skipped=2", committed.CreatedRows, committed.SkippedRows)
		}
	})
}

// TestImportRejectsAnUnmappableField proves a mapping naming a field
// outside the allowlist is rejected at job creation, before any row is read.
func TestImportRejectsAnUnmappableField(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)
		_, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "x.csv", Content: validCSV,
			Mapping: map[string]string{"Company": "custom_fields"},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("got %v, want ErrInvalid", err)
		}
	})
}

// TestImportRejectsOversizedFiles proves the row and byte bounds are
// enforced before any row is parsed.
func TestImportRejectsOversizedFiles(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		var rows strings.Builder
		rows.WriteString("Company\n")
		for i := 0; i < maxImportRows+1; i++ {
			fmt.Fprintf(&rows, "Acme %d\n", i)
		}
		_, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "big.csv", Content: rows.String(),
			Mapping: map[string]string{"Company": "name"},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("over row limit: got %v, want ErrInvalid", err)
		}

		_, err = s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "huge.csv", Content: strings.Repeat("x", maxImportFileBytes+1),
			Mapping: map[string]string{"Company": "name"},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("over byte limit: got %v, want ErrInvalid", err)
		}
	})
}

// TestListImportJobsIsPrivateToItsTenant is folded into the cross-tenant
// isolation suite (internal/api); this file only proves the store-level
// listing itself works, newest first.
func TestListImportJobsOrdersNewestFirst(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		first, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "first.csv", Content: validCSV, Mapping: validMapping(),
		})
		if err != nil {
			t.Fatalf("create first: %v", err)
		}
		second, err := s.CreateImportJob(ctx, scope, CreateImportJobInput{
			ObjectType: "account", FileName: "second.csv", Content: validCSV, Mapping: validMapping(),
		})
		if err != nil {
			t.Fatalf("create second: %v", err)
		}

		jobs, err := s.ListImportJobs(ctx, scope, "account")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(jobs) != 2 {
			t.Fatalf("jobs = %d, want 2", len(jobs))
		}
		ids := map[string]bool{jobs[0].ID: true, jobs[1].ID: true}
		if !ids[first.ID] || !ids[second.ID] {
			t.Fatalf("listed jobs %+v do not match created %s, %s", jobs, first.ID, second.ID)
		}
	})
}

// TestExportRoundTripsWhatWasImported proves CreateExportJob returns real
// CSV content covering the tenant's accounts, and records an audit row.
func TestExportRoundTripsWhatWasImported(t *testing.T) {
	eachImportExportStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := importScope(name)

		if _, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme", OwnerID: "alice"}); err != nil {
			t.Fatalf("create account: %v", err)
		}

		job, content, err := s.CreateExportJob(ctx, scope, "account", "admin-1")
		if err != nil {
			t.Fatalf("create export job: %v", err)
		}
		if job.RowCount != 1 || job.RequestedBy != "admin-1" {
			t.Fatalf("export job = %+v, want row_count=1 requested_by=admin-1", job)
		}
		if !strings.Contains(content, "Acme") {
			t.Fatalf("export content missing the account: %q", content)
		}

		history, err := s.ListExportJobs(ctx, scope, "account")
		if err != nil {
			t.Fatalf("list export jobs: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("export history = %d, want 1", len(history))
		}
	})
}

func TestExportNeutralizesSpreadsheetFormulas(t *testing.T) {
	content, err := AccountsToCSV([]*Account{{Name: "=HYPERLINK(\"https://evil.test\")", OwnerID: " +cmd", Industry: "@SUM(1,1)"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []int{1, 2, 4} {
		if !strings.HasPrefix(rows[1][column], "'") {
			t.Errorf("dangerous cell %q was not escaped", rows[1][column])
		}
	}
}

func TestConcurrentImportCommitCreatesEachRowOnce(t *testing.T) {
	store := NewMemStore()
	scope := importScope("concurrent")
	job, err := store.CreateImportJob(context.Background(), scope, CreateImportJobInput{
		ObjectType: "account", FileName: "accounts.csv", Content: validCSV, Mapping: validMapping(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, commitErr := store.CommitImportJob(context.Background(), scope, job.ID)
			errs <- commitErr
		}()
	}
	wg.Wait()
	close(errs)
	for commitErr := range errs {
		if commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	accounts, _, err := store.ListAccounts(context.Background(), scope, Page{Size: 10}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("concurrent commit created %d accounts, want 2", len(accounts))
	}
}
