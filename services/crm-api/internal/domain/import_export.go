package domain

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CSV import and export, accounts only in v1 (M4 — Data productivity). See
// docs/CSV_IMPORT_EXPORT.md for the full product decision record — format,
// size limits, duplicate policy, the synchronous-not-asynchronous scope
// narrowing, and why there is no rollback. This file is the domain-layer
// mechanics that document implements.

const (
	// maxImportRows and maxImportFileBytes bound one import job so the
	// whole operation — preview and commit alike — finishes inside one RPC
	// call. See docs/CSV_IMPORT_EXPORT.md's "Synchronous, not asynchronous"
	// section for why that is the scope this version chose.
	maxImportRows      = 500
	maxImportFileBytes = 2 * 1024 * 1024
	// maxExportRows bounds one export response, returned inline rather
	// than through object storage this deployment does not have. Set to
	// Page's own maxPageSize (types.go) rather than a second, larger cap,
	// so an export is exactly one ListAccounts-shaped call rather than a
	// multi-page loop this version does not implement.
	maxExportRows = maxPageSize
)

// importableAccountFields is the same "which fields may an external,
// untrusted source set" allowlist merge_accounts.go's mergeableAccountFields
// answers, reused here for the identical reason.
var importableAccountFields = map[string]bool{
	"name": true, "industry": true, "tier": true, "owner_id": true,
	"website": true, "annual_revenue": true, "parent_id": true,
}

// SortedImportableAccountFields returns the allowlist, sorted, for the
// mapping UI to offer.
func SortedImportableAccountFields() []string {
	out := make([]string, 0, len(importableAccountFields))
	for f := range importableAccountFields {
		out = append(out, f)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

const (
	ImportStatusPreviewed = "previewed"
	ImportStatusCompleted = "completed"
)

// ImportRowError is one row's validation or creation failure. RowNumber is
// 1-indexed against the data rows (the header is not counted), so it lines
// up with what a spreadsheet program shows for that row.
type ImportRowError struct {
	RowNumber int    `json:"row_number"`
	Message   string `json:"message"`
}

// ImportJob is a CSV import, from upload through commit. See
// docs/CSV_IMPORT_EXPORT.md for the two-call preview/commit sequence this
// models.
type ImportJob struct {
	ID string
	// ObjectType is always "account" in v1 — stored rather than hardcoded
	// so the schema does not need to change when a second entity is added.
	ObjectType string
	Status     string
	FileName   string
	// Content is the raw CSV text, bounded to maxImportFileBytes — see
	// docs/CSV_IMPORT_EXPORT.md's "No object storage" section for why it
	// lives here instead of a blob store.
	Content string
	// Mapping is CSV header text -> one of importableAccountFields.
	Mapping     map[string]string
	RowCount    int
	CreatedRows int
	SkippedRows int
	Errors      []ImportRowError
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func cloneImportJob(v *ImportJob) *ImportJob {
	if v == nil {
		return nil
	}
	c := *v
	c.Mapping = make(map[string]string, len(v.Mapping))
	for k, val := range v.Mapping {
		c.Mapping[k] = val
	}
	c.Errors = append([]ImportRowError(nil), v.Errors...)
	return &c
}

// EntityID, SetEntityID, SetTimestamps and Validate satisfy the Entity
// interface memTable[T] requires (memstore_import_export.go).
func (e *ImportJob) EntityID() string      { return e.ID }
func (e *ImportJob) SetEntityID(id string) { e.ID = id }
func (e *ImportJob) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}
func (e *ImportJob) Validate() error {
	if e.ObjectType != "account" {
		return fmt.Errorf("%w: %q is not an importable record type (only account is supported)", ErrInvalid, e.ObjectType)
	}
	if strings.TrimSpace(e.FileName) == "" {
		return fmt.Errorf("%w: an import job needs a file name", ErrInvalid)
	}
	if len(e.Content) > maxImportFileBytes {
		return fmt.Errorf("%w: import file exceeds %d bytes", ErrInvalid, maxImportFileBytes)
	}
	for _, field := range e.Mapping {
		if !importableAccountFields[field] {
			return fmt.Errorf("%w: %q is not a field an import can set (allowed: %s)",
				ErrInvalid, field, strings.Join(SortedImportableAccountFields(), ", "))
		}
	}
	return nil
}

// ExportJob is an audit record of one CSV export request. The file itself
// is never persisted — see docs/CSV_IMPORT_EXPORT.md's "No object storage"
// section — so this row is the only durable trace that the export happened,
// by whom, and how many rows it covered.
type ExportJob struct {
	ID          string
	ObjectType  string
	RowCount    int
	RequestedBy string
	CreatedAt   time.Time
}

func cloneExportJob(v *ExportJob) *ExportJob {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func (e *ExportJob) EntityID() string             { return e.ID }
func (e *ExportJob) SetEntityID(id string)        { e.ID = id }
func (e *ExportJob) SetTimestamps(_, _ time.Time) {}
func (e *ExportJob) Validate() error {
	if e.ObjectType != "account" {
		return fmt.Errorf("%w: %q is not an exportable record type (only account is supported)", ErrInvalid, e.ObjectType)
	}
	return nil
}

// exportAccountColumns is the fixed column order AccountsToCSV writes.
var exportAccountColumns = []string{
	"id", "name", "industry", "tier", "owner_id", "website", "annual_revenue",
	"parent_id", "duplicate_of_account_id", "created_at", "updated_at",
}

// AccountsToCSV renders accounts as CSV text in exportAccountColumns' fixed
// column order — the counterpart to buildImportAccount's read side, so a
// tenant's own export is itself a valid mapping-1:1 import input.
func AccountsToCSV(accounts []*Account) (string, error) {
	var buf strings.Builder
	w := csv.NewWriter(&buf)
	if err := w.Write(exportAccountColumns); err != nil {
		return "", fmt.Errorf("write export header: %w", err)
	}
	for _, a := range accounts {
		row := []string{
			a.ID, a.Name, a.Industry, a.Tier, a.OwnerID, a.Website,
			strconv.FormatFloat(a.AnnualRevenue, 'f', -1, 64), a.ParentID, a.DuplicateOfAccountID,
			a.CreatedAt.UTC().Format(time.RFC3339), a.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if err := w.Write(row); err != nil {
			return "", fmt.Errorf("write export row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("flush export: %w", err)
	}
	return buf.String(), nil
}

// parseImportCSV splits raw CSV content into a header and its data rows,
// bounded at maxImportRows data rows. A structural failure (unreadable CSV,
// no header) is returned as an error; a too-large file is rejected before
// this is even called (ImportJob.Validate).
func parseImportCSV(content string) (header []string, rows [][]string, err error) {
	reader := csv.NewReader(strings.NewReader(content))
	reader.FieldsPerRecord = -1
	all, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: could not parse CSV: %v", ErrInvalid, err)
	}
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("%w: CSV file has no header row", ErrInvalid)
	}
	header, dataRows := all[0], all[1:]
	if len(dataRows) > maxImportRows {
		return nil, nil, fmt.Errorf("%w: CSV has %d data rows, which exceeds the %d-row limit per import",
			ErrInvalid, len(dataRows), maxImportRows)
	}
	return header, dataRows, nil
}

// buildImportAccount turns one CSV data row into an Account, using mapping
// to find each field's column, and validates the fields that can be
// checked without a store lookup (a required name, a parseable
// annual_revenue). It returns a *ImportRowError rather than a Go error: a
// bad row is an expected outcome for one row among many, not a fault.
func buildImportAccount(header []string, row []string, mapping map[string]string) (*Account, *ImportRowError) {
	columnFor := make(map[string]int, len(header))
	for i, name := range header {
		columnFor[name] = i
	}
	value := func(field string) (string, bool) {
		for csvHeader, mappedField := range mapping {
			if mappedField != field {
				continue
			}
			col, ok := columnFor[csvHeader]
			if !ok || col >= len(row) {
				return "", false
			}
			return strings.TrimSpace(row[col]), true
		}
		return "", false
	}

	account := &Account{}
	if name, ok := value("name"); ok {
		account.Name = name
	}
	if account.Name == "" {
		return nil, &ImportRowError{Message: "name is required"}
	}
	if industry, ok := value("industry"); ok {
		account.Industry = industry
	}
	if tier, ok := value("tier"); ok {
		account.Tier = tier
	}
	if ownerID, ok := value("owner_id"); ok {
		account.OwnerID = ownerID
	}
	if website, ok := value("website"); ok {
		account.Website = website
	}
	if parentID, ok := value("parent_id"); ok {
		account.ParentID = parentID
	}
	if revenue, ok := value("annual_revenue"); ok && revenue != "" {
		parsed, err := strconv.ParseFloat(revenue, 64)
		if err != nil {
			return nil, &ImportRowError{Message: fmt.Sprintf("annual_revenue %q is not a number", revenue)}
		}
		account.AnnualRevenue = parsed
	}
	return account, nil
}

// previewImportRows validates every data row structurally (buildImportAccount)
// without touching the store — the dry run CreateImportJob performs. It
// returns the row-level errors and the count of rows that passed.
func previewImportRows(header []string, rows [][]string, mapping map[string]string) (validCount int, errs []ImportRowError) {
	for i, row := range rows {
		if _, rowErr := buildImportAccount(header, row, mapping); rowErr != nil {
			rowErr.RowNumber = i + 1
			errs = append(errs, *rowErr)
			continue
		}
		validCount++
	}
	return validCount, errs
}

// accountCreator is what commitImportRows needs from a store: the ordinary
// CreateAccount path, so a committed row gets exactly the same validation,
// owner requirement, parent-cycle check, dedup flagging and custom-field
// handling any other created Account gets — nothing here re-implements
// that logic.
type accountCreator interface {
	CreateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error)
}

// commitImportRows creates one Account per structurally valid row via the
// store's ordinary CreateAccount, so a row that fails a check
// buildImportAccount cannot see (an invalid parent_id, a missing owner) is
// caught here instead, as the same kind of per-row error.
func commitImportRows(
	ctx context.Context, store accountCreator, scope Scope, header []string, rows [][]string, mapping map[string]string,
) (created, skipped int, errs []ImportRowError) {
	for i, row := range rows {
		account, rowErr := buildImportAccount(header, row, mapping)
		if rowErr != nil {
			rowErr.RowNumber = i + 1
			errs = append(errs, *rowErr)
			skipped++
			continue
		}
		if _, err := store.CreateAccount(ctx, scope, account); err != nil {
			errs = append(errs, ImportRowError{RowNumber: i + 1, Message: importRowErrorMessage(err)})
			skipped++
			continue
		}
		created++
	}
	return created, skipped, errs
}

// CreateImportJobInput names everything CreateImportJob needs.
type CreateImportJobInput struct {
	ObjectType string
	FileName   string
	Content    string
	Mapping    map[string]string
	CreatedBy  string
}

// NewImportJobPreview builds and structurally validates an ImportJob in
// "previewed" status: parses the CSV and runs buildImportAccount over every
// data row without touching the store. It performs no store writes and no
// referential checks (an invalid owner_id or parent_id is only caught at
// commit, through the real CreateAccount path — see commitImportRows). A
// store's CreateImportJob persists the result this returns.
func NewImportJobPreview(in CreateImportJobInput) (*ImportJob, error) {
	job := &ImportJob{
		ObjectType: in.ObjectType, FileName: in.FileName, Content: in.Content,
		Mapping: in.Mapping, CreatedBy: in.CreatedBy, Status: ImportStatusPreviewed,
	}
	if err := job.Validate(); err != nil {
		return nil, err
	}
	header, rows, err := parseImportCSV(in.Content)
	if err != nil {
		return nil, err
	}
	validCount, errs := previewImportRows(header, rows, in.Mapping)
	job.RowCount = len(rows)
	job.Errors = errs
	job.SkippedRows = len(rows) - validCount
	return job, nil
}

// RunImportCommit re-parses an already-previewed job's stored content and
// creates one Account per structurally valid row through store, returning
// the job's post-commit fields (a store's CommitImportJob assigns them and
// persists). Called only on a job still in "previewed" status — a store's
// CommitImportJob is what makes repeating the call on a completed job
// idempotent, by returning the stored result instead of calling this again.
func RunImportCommit(
	ctx context.Context, store accountCreator, scope Scope, job *ImportJob,
) (createdRows, skippedRows int, errs []ImportRowError, err error) {
	header, rows, parseErr := parseImportCSV(job.Content)
	if parseErr != nil {
		return 0, 0, nil, parseErr
	}
	created, skipped, rowErrs := commitImportRows(ctx, store, scope, header, rows, job.Mapping)
	return created, skipped, rowErrs, nil
}

// importRowErrorMessage mirrors internal/api/bulk.go's bulkErrorMessage: a
// domain sentinel's own bounded text, or a generic fallback — never a raw
// internal error surfaced to a caller who uploaded a spreadsheet.
// Duplicated rather than shared because internal/api depends on
// internal/domain, not the reverse.
func importRowErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidTransition):
		return err.Error()
	case errors.Is(err, ErrConflict):
		return "conflicts with an existing record"
	case errors.Is(err, ErrNotFound):
		return "references a record that does not exist"
	default:
		return "internal error"
	}
}
