package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const pipelineColumns = `id, name, is_default, archived_at, created_at, updated_at`
const pipelineStageColumns = `id, pipeline_id, name, position, probability, terminal_type, requires_close_reason, archived_at, created_at, updated_at`

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func scanPipeline(row pgx.Row) (*Pipeline, error) {
	v := &Pipeline{}
	var archivedAt *time.Time
	err := row.Scan(&v.ID, &v.Name, &v.IsDefault, &archivedAt, &v.CreatedAt, &v.UpdatedAt)
	if archivedAt != nil {
		v.ArchivedAt = *archivedAt
	}
	return v, err
}

func scanPipelineStage(row pgx.Row) (*PipelineStage, error) {
	v := &PipelineStage{}
	var archivedAt *time.Time
	err := row.Scan(&v.ID, &v.PipelineID, &v.Name, &v.Position, &v.Probability, &v.TerminalType, &v.RequiresCloseReason, &archivedAt, &v.CreatedAt, &v.UpdatedAt)
	if archivedAt != nil {
		v.ArchivedAt = *archivedAt
	}
	return v, err
}

func (s *PgStore) CreatePipeline(ctx context.Context, scope Scope, v *Pipeline) (*Pipeline, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err := s.db.Exec(ctx, scope,
		`INSERT INTO pipelines (tenant_id, id, name, is_default, archived_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		scope.TenantID, v.ID, v.Name, v.IsDefault, nullableTime(v.ArchivedAt), v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}
	return clonePipeline(v), nil
}

func (s *PgStore) GetPipeline(ctx context.Context, scope Scope, id string) (*Pipeline, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v, err := scanPipeline(s.db.QueryRow(ctx, scope, `SELECT `+pipelineColumns+` FROM pipelines WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	return v, nil
}

func (s *PgStore) ListPipelines(ctx context.Context, scope Scope) ([]*Pipeline, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+pipelineColumns+` FROM pipelines WHERE tenant_id = $1 ORDER BY archived_at NULLS FIRST, name, id`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	defer rows.Close()
	out := make([]*Pipeline, 0)
	for rows.Next() {
		v, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pipeline: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	return out, nil
}

// defaultOpportunityPipeline returns a tenant's active default. A newly
// provisioned tenant gets the compatible Sales pipeline lazily, which keeps
// bootstrap independent of CRM data while still making every new opportunity
// pipeline-bound from its first write.
func (s *PgStore) defaultOpportunityPipeline(ctx context.Context, scope Scope) (*Pipeline, error) {
	pipelines, err := s.ListPipelines(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, pipeline := range pipelines {
		if pipeline.IsDefault && pipeline.ArchivedAt.IsZero() {
			return pipeline, nil
		}
	}
	pipeline, err := s.CreatePipeline(ctx, scope, &Pipeline{ID: systemSalesPipelineID, Name: "Sales", IsDefault: true})
	if err != nil {
		return nil, fmt.Errorf("create default Sales pipeline: %w", err)
	}
	for _, definition := range systemSalesStages {
		stage := definition
		stage.PipelineID = pipeline.ID
		if _, err := s.CreatePipelineStage(ctx, scope, &stage); err != nil {
			return nil, fmt.Errorf("create default Sales stage: %w", err)
		}
	}
	return pipeline, nil
}

func (s *PgStore) UpdatePipeline(ctx context.Context, scope Scope, v *Pipeline) (*Pipeline, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	v.UpdatedAt = time.Now().UTC()
	// The partial unique index permits one active default per tenant. Clear a
	// previous default in the same RLS-bound transaction before promoting this
	// pipeline, so the API supports an intentional default change rather than
	// leaking a raw database constraint error to the settings screen.
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if v.IsDefault && v.ArchivedAt.IsZero() {
			if _, err := tx.Exec(ctx,
				`UPDATE pipelines SET is_default = false, updated_at = $2 WHERE tenant_id = $1 AND id <> $3 AND is_default AND archived_at IS NULL`,
				scope.TenantID, v.UpdatedAt, v.ID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx,
			`UPDATE pipelines SET name = $3, is_default = $4, archived_at = $5, updated_at = $6 WHERE tenant_id = $1 AND id = $2`,
			scope.TenantID, v.ID, v.Name, v.IsDefault, nullableTime(v.ArchivedAt), v.UpdatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update pipeline: %w", err)
	}
	return s.GetPipeline(ctx, scope, v.ID)
}

func (s *PgStore) CreatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	pipeline, err := s.GetPipeline(ctx, scope, v.PipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: cannot add a stage to an archived pipeline", ErrInvalid)
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO pipeline_stages (tenant_id, pipeline_id, id, name, position, probability, terminal_type, requires_close_reason, archived_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		scope.TenantID, v.PipelineID, v.ID, v.Name, v.Position, v.Probability, v.TerminalType, v.RequiresCloseReason, nullableTime(v.ArchivedAt), v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create pipeline stage: %w", err)
	}
	return clonePipelineStage(v), nil
}

func (s *PgStore) ListPipelineStages(ctx context.Context, scope Scope, pipelineID string) ([]*PipelineStage, error) {
	if _, err := s.GetPipeline(ctx, scope, pipelineID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+pipelineStageColumns+` FROM pipeline_stages WHERE tenant_id = $1 AND pipeline_id = $2 ORDER BY position, id`, scope.TenantID, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list pipeline stages: %w", err)
	}
	defer rows.Close()
	out := make([]*PipelineStage, 0)
	for rows.Next() {
		v, err := scanPipelineStage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pipeline stage: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pipeline stages: %w", err)
	}
	return out, nil
}

func (s *PgStore) UpdatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	pipeline, err := s.GetPipeline(ctx, scope, v.PipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: cannot update a stage in an archived pipeline", ErrInvalid)
	}
	v.UpdatedAt = time.Now().UTC()
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE pipeline_stages SET name = $4, position = $5, probability = $6, terminal_type = $7, requires_close_reason = $8, archived_at = $9, updated_at = $10 WHERE tenant_id = $1 AND pipeline_id = $2 AND id = $3`,
		scope.TenantID, v.PipelineID, v.ID, v.Name, v.Position, v.Probability, v.TerminalType, v.RequiresCloseReason, nullableTime(v.ArchivedAt), v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update pipeline stage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.ListPipelineStages(ctx, scope, v.PipelineID)
	if err != nil {
		return nil, err
	}
	for _, stage := range rows {
		if stage.ID == v.ID {
			return stage, nil
		}
	}
	return nil, ErrNotFound
}
