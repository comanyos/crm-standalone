package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PgStore's Opportunity update path.
//
// UpdateOpportunity lives here rather than in pgstore.go with the other CRUD
// because it is not one statement: it decides a stage transition, writes the
// row, appends the append-only stage-history entry that transition produced,
// and — on the one transition into a won stage — enqueues the outbox event.
// Those four have to be one atomic step against one snapshot of the row, or
// the record and its history disagree.
//
// The previous implementation issued each of them as its own transaction
// through scopedDB, with no lock in between. Two concurrent stage advances
// therefore both read the same "from" stage, both passed the transition
// check against it, and both wrote — leaving opportunity_stage_history with
// two rows leaving the same stage and only one of them matching where the
// opportunity actually ended up. The stage history is the audit record for
// the revenue pipeline, so a branch in it is not a cosmetic problem.
//
// The fix is the shape ConvertLead already uses (pgstore_lead_conversion.go):
// one scopedDB.begin transaction, the row taken with SELECT ... FOR UPDATE,
// and every read the decision depends on served from inside it. Under READ
// COMMITTED a second caller blocks on that lock and then re-reads the
// committed row, so it sees the first transition as its own "from" stage and
// is either a legal next step or a rejected one — never a second branch.

// txReader adapts an open transaction to the reader interfaces the shared
// rules take — opportunityStageResolver, accountReader, customFieldReader,
// closedWonDeps — so a multi-statement write evaluates every rule against the
// same locked snapshot it is about to commit, rather than against whatever a
// separate connection happens to see.
//
// Every method here has a same-named sibling on PgStore that goes through
// scopedDB. The difference is not only the snapshot: a scopedDB call acquires
// its own pool connection, and doing that while a transaction still holds one
// is how a pool with N connections deadlocks under N concurrent writers.
type txReader struct {
	s  *PgStore
	tx pgx.Tx
}

func (r txReader) GetAccount(ctx context.Context, scope Scope, id string) (*Account, error) {
	return r.s.txGetAccount(ctx, r.tx, scope, id)
}

func (r txReader) GetContact(ctx context.Context, scope Scope, id string) (*Contact, error) {
	return r.s.txGetContact(ctx, r.tx, scope, id)
}

func (r txReader) GetOpportunity(ctx context.Context, scope Scope, id string) (*Opportunity, error) {
	return r.s.txGetOpportunity(ctx, r.tx, scope, id)
}

func (r txReader) GetLead(ctx context.Context, scope Scope, id string) (*Lead, error) {
	v := &Lead{}
	err := scanLead(r.tx.QueryRow(ctx,
		`SELECT `+leadColumns+` FROM leads WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get lead: %w", err)
	}
	return v, nil
}

func (r txReader) GetPipeline(ctx context.Context, scope Scope, id string) (*Pipeline, error) {
	v, err := scanPipeline(r.tx.QueryRow(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	return v, nil
}

func (r txReader) ListPipelineStages(ctx context.Context, scope Scope, pipelineID string) ([]*PipelineStage, error) {
	if _, err := r.GetPipeline(ctx, scope, pipelineID); err != nil {
		return nil, err
	}
	rows, err := r.tx.Query(ctx,
		`SELECT `+pipelineStageColumns+` FROM pipeline_stages
		 WHERE tenant_id = $1 AND pipeline_id = $2 ORDER BY position, id`,
		scope.TenantID, pipelineID)
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

// defaultOpportunityPipeline mirrors PgStore's, provisioning the compatible
// Sales pipeline for a tenant that has none yet. Doing it inside the caller's
// transaction is what lets a first-ever write in a fresh tenant still be one
// atomic step.
func (r txReader) defaultOpportunityPipeline(ctx context.Context, scope Scope) (*Pipeline, error) {
	pipeline, err := scanPipeline(r.tx.QueryRow(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines
		 WHERE tenant_id = $1 AND is_default AND archived_at IS NULL ORDER BY id LIMIT 1`,
		scope.TenantID))
	if err == nil {
		return pipeline, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find default pipeline: %w", err)
	}

	now := nowStamp()
	pipeline = &Pipeline{ID: systemSalesPipelineID, Name: "Sales", IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if _, err := r.tx.Exec(ctx,
		`INSERT INTO pipelines (tenant_id, id, name, is_default, created_at, updated_at)
		 VALUES ($1, $2, $3, true, $4, $5)`,
		scope.TenantID, pipeline.ID, pipeline.Name, now, now); err != nil {
		return nil, fmt.Errorf("create default Sales pipeline: %w", err)
	}
	for _, definition := range systemSalesStages {
		stage := definition
		if _, err := r.tx.Exec(ctx,
			`INSERT INTO pipeline_stages (tenant_id, pipeline_id, id, name, position, probability, terminal_type, requires_close_reason, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			scope.TenantID, pipeline.ID, stage.ID, stage.Name, stage.Position, stage.Probability,
			stage.TerminalType, stage.RequiresCloseReason, now, now); err != nil {
			return nil, fmt.Errorf("create default Sales stage: %w", err)
		}
	}
	return pipeline, nil
}

// ListCustomFieldDefinitions serves CoerceCustomValues from inside the
// transaction. Reading it through the store instead would acquire a *second*
// pool connection while this one is still held by the open transaction: with
// as many concurrent writers as the pool has connections, every one of them
// holds one and waits for another, and the pool deadlocks until the contexts
// time out. Serving it from the tx also means the write is validated against
// the same snapshot it commits in.
func (r txReader) ListCustomFieldDefinitions(
	ctx context.Context, scope Scope, objectType string, includeArchived bool,
) ([]*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := r.tx.Query(ctx,
		`SELECT `+customFieldCols+` FROM custom_field_definitions
		  WHERE tenant_id = $1 AND object_type = $2
		    AND ($3 OR archived_at IS NULL)
		  ORDER BY field_key`,
		scope.TenantID, normalizeObjectType(objectType), includeArchived)
	if err != nil {
		return nil, fmt.Errorf("list custom fields: %w", err)
	}
	defer rows.Close()

	out := make([]*CustomFieldDefinition, 0, 8)
	for rows.Next() {
		v, err := scanCustomFieldDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// enqueueOutboxEvent writes the outbox row inside the caller's transaction,
// which is the whole point of an outbox: the event and the state change it
// describes commit together or not at all. Same statement as PgStore's, so
// the ON CONFLICT dedup key still applies.
func (r txReader) enqueueOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error {
	envelope, err := json.Marshal(ev.Envelope)
	if err != nil {
		return fmt.Errorf("%w: marshal envelope: %v", ErrInvalid, err)
	}
	_, err = r.tx.Exec(ctx,
		`INSERT INTO event_outbox (
		     tenant_id, id,
		     event_type, entity_type, entity_id, source_version,
		     correlation_id, causation_id, actor, occurred_at,
		     envelope, status, created_at, updated_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), now())
		 ON CONFLICT (tenant_id, entity_type, entity_id, source_version) DO NOTHING`,
		scope.TenantID, ev.ID,
		ev.Envelope.Type, ev.Envelope.EntityType, ev.Envelope.EntityID, ev.Envelope.SourceVersion,
		ev.Envelope.CorrelationID, ev.Envelope.CausationID, ev.Envelope.Actor, ev.Envelope.OccurredAt,
		envelope, ev.Status)
	if err != nil {
		return fmt.Errorf("enqueue event: %w", err)
	}
	return nil
}

// UpdateOpportunity replaces an existing Opportunity, preserving created_at.
// It enforces the configured stage state machine, re-checks the account_id
// reference, applies the close-reason/close-date rule on a fresh transition
// into a terminal stage, records stage history, and — on the specific
// transition into a won stage — enqueues opportunity.closed_won.
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's, or the write is rejected with ErrConflict. A caller that does not
// round-trip UpdatedAt skips the check, which is what makes this safe to add
// to an existing endpoint. See UpdateAccount for the same contract.
//
// Everything above happens in one transaction with the row locked, so a
// concurrent caller serializes behind it rather than deciding its transition
// from a stage that is no longer current. See the file comment.
func (s *PgStore) UpdateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}

	tx, err := s.db.begin(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	r := txReader{s: s, tx: tx}

	existing := &Opportunity{}
	err = scanOpportunity(tx.QueryRow(ctx,
		`SELECT `+opportunityColumns+` FROM opportunities WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		scope.TenantID, v.ID), existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get opportunity for update: %w", err)
	}

	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Opportunity %s was modified since it was last read", ErrConflict, v.ID)
	}

	stage, err := prepareOpportunityStage(ctx, r, scope, v, existing)
	if err != nil {
		return nil, err
	}
	fromStage, err := currentPipelineStage(ctx, r, scope, existing.PipelineID, existing.StageID)
	if err != nil {
		return nil, err
	}
	if err := checkConfiguredStageTransition(fromStage, stage); err != nil {
		return nil, err
	}
	// account_id is re-checked on every update, not only when it changes: a
	// dangling reference introduced after this Opportunity was created (the
	// account it named was deleted) must be caught here too.
	if err := requireAccount(ctx, r, scope, v.AccountID); err != nil {
		return nil, err
	}

	becameClosed := existing.StageID != v.StageID && stage.TerminalType != "open"
	if becameClosed {
		if err := applyConfiguredClosingRules(v, stage); err != nil {
			return nil, err
		}
	}
	custom, err := CoerceCustomValues(ctx, r, scope, "opportunity", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()

	tag, err := tx.Exec(ctx,
		`UPDATE opportunities SET name = $3, account_id = $4, stage = $5, pipeline_id = $6, stage_id = $7,
			amount = $8, probability = $9, close_date = $10, close_reason = $11, updated_at = $12, custom_fields = $13
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID, v.Name, v.AccountID, v.Stage, v.PipelineID, v.StageID, v.Amount,
		v.Probability, v.CloseDate, v.CloseReason, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("update opportunity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}

	updated := &Opportunity{}
	if err := scanOpportunity(tx.QueryRow(ctx,
		`SELECT `+opportunityColumns+` FROM opportunities WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID), updated); err != nil {
		return nil, fmt.Errorf("re-read opportunity: %w", err)
	}

	if existing.StageID != updated.StageID {
		if _, err := tx.Exec(ctx,
			`INSERT INTO opportunity_stage_history (tenant_id, id, opportunity_id, from_stage, to_stage, changed_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			scope.TenantID, newID(), updated.ID, existing.Stage, updated.Stage, nowStamp()); err != nil {
			return nil, fmt.Errorf("record opportunity stage history: %w", err)
		}
	}

	if becameClosed && stage.TerminalType == "won" {
		if err := enqueueOpportunityClosedWon(ctx, r, scope, updated); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// MoveOpportunityToPipeline is the cross-pipeline counterpart to
// UpdateOpportunity. The opportunity row stays locked from the version check
// through the destination validation, update, and history insert, so a
// competing stage move cannot make the audit trail branch from stale state.
func (s *PgStore) MoveOpportunityToPipeline(ctx context.Context, scope Scope, in MoveOpportunityToPipelineInput) (*Opportunity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if in.OpportunityID == "" {
		return nil, ErrNotFound
	}
	tx, err := s.db.begin(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing := &Opportunity{}
	err = scanOpportunity(tx.QueryRow(ctx,
		`SELECT `+opportunityColumns+` FROM opportunities WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		scope.TenantID, in.OpportunityID), existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get opportunity for pipeline move: %w", err)
	}
	if in.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: updated_at is required", ErrInvalid)
	}
	if !in.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Opportunity %s was modified since it was last read", ErrConflict, in.OpportunityID)
	}

	r := txReader{s: s, tx: tx}
	destination, err := validateOpportunityPipelineMove(ctx, r, scope, existing, in.PipelineID, in.StageID)
	if err != nil {
		return nil, err
	}
	now := pipelineMoveStamp(existing.UpdatedAt)
	stage := stageDisplay(in.PipelineID, destination)
	tag, err := tx.Exec(ctx,
		`UPDATE opportunities SET stage = $3, pipeline_id = $4, stage_id = $5, updated_at = $6
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, existing.ID, stage, in.PipelineID, destination.ID, now)
	if err != nil {
		return nil, fmt.Errorf("move opportunity to pipeline: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	updated := cloneOpportunity(existing)
	updated.PipelineID = in.PipelineID
	updated.StageID = destination.ID
	updated.Stage = stage
	updated.UpdatedAt = now
	if _, err := tx.Exec(ctx,
		`INSERT INTO opportunity_stage_history (tenant_id, id, opportunity_id, from_stage, to_stage, changed_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		scope.TenantID, newID(), existing.ID, existing.Stage, stage, now); err != nil {
		return nil, fmt.Errorf("record opportunity stage history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit pipeline move: %w", err)
	}
	return updated, nil
}
