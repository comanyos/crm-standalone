package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/blueprint"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BlueprintStore implements blueprint.Store.
type BlueprintStore struct{ db *pgxpool.Pool }

func NewBlueprintStore(db *pgxpool.Pool) *BlueprintStore { return &BlueprintStore{db: db} }

func (s *BlueprintStore) Transaction(ctx context.Context, fn func(context.Context) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(database.WithTx(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *BlueprintStore) ByOrgAndName(ctx context.Context, orgID uuid.UUID, name string) (*blueprint.Blueprint, error) {
	bp := &blueprint.Blueprint{}
	err := database.ExecutorFor(ctx, s.db).QueryRow(ctx,
		`SELECT id, org_id, name, version, state, yaml_content, COALESCE(last_error,''), last_applied, created_at
		 FROM blueprints WHERE org_id=$1 AND name=$2`, orgID, name).
		Scan(&bp.ID, &bp.OrgID, &bp.Name, &bp.Version, &bp.State,
			&bp.YAMLContent, &bp.LastError, &bp.LastApplied, &bp.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "blueprint not found")
		}
		return nil, fmt.Errorf("blueprint by org+name: %w", err)
	}
	return bp, nil
}

func (s *BlueprintStore) Delete(ctx context.Context, id, orgID uuid.UUID) error {
	_, err := database.ExecutorFor(ctx, s.db).Exec(ctx, `DELETE FROM blueprints WHERE id=$1 AND org_id=$2`, id, orgID)
	return err
}

func (s *BlueprintStore) Insert(ctx context.Context, bp *blueprint.Blueprint) error {
	_, err := database.ExecutorFor(ctx, s.db).Exec(ctx,
		`INSERT INTO blueprints (id, org_id, name, version, state, yaml_content, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		bp.ID, bp.OrgID, bp.Name, bp.Version, bp.State, bp.YAMLContent, bp.CreatedAt)
	return err
}

func (s *BlueprintStore) Update(ctx context.Context, bp *blueprint.Blueprint) error {
	now := time.Now().UTC()
	_, err := database.ExecutorFor(ctx, s.db).Exec(ctx,
		`UPDATE blueprints
		 SET version=$1, state=$2, yaml_content=$3, last_error=$4, last_applied=$5
		 WHERE id=$6 AND org_id=$7`,
		bp.Version, bp.State, bp.YAMLContent, bp.LastError, now, bp.ID, bp.OrgID)
	return err
}

func (s *BlueprintStore) List(ctx context.Context, orgID uuid.UUID) ([]*blueprint.Blueprint, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, version, state, yaml_content, COALESCE(last_error,''), last_applied, created_at
		 FROM blueprints WHERE org_id=$1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list blueprints: %w", err)
	}
	defer rows.Close()
	var out []*blueprint.Blueprint
	for rows.Next() {
		bp := &blueprint.Blueprint{}
		if err := rows.Scan(&bp.ID, &bp.OrgID, &bp.Name, &bp.Version, &bp.State,
			&bp.YAMLContent, &bp.LastError, &bp.LastApplied, &bp.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan blueprint: %w", err)
		}
		out = append(out, bp)
	}
	return out, rows.Err()
}
