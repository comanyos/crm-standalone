package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/authincore/authincore/internal/actions"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ActionsStore implements actions.Store.
type ActionsStore struct{ db *pgxpool.Pool }

func NewActionsStore(db *pgxpool.Pool) *ActionsStore { return &ActionsStore{db: db} }

func (s *ActionsStore) ByID(ctx context.Context, id uuid.UUID) (*actions.Action, error) {
	a := &actions.Action{}
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, name, script, timeout_ms, allowed_to_fail
		 FROM actions WHERE id = $1`, id).
		Scan(&a.ID, &a.OrgID, &a.Name, &a.Script, &a.TimeoutMS, &a.AllowedToFail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "action not found")
		}
		return nil, fmt.Errorf("action by id: %w", err)
	}
	return a, nil
}

func (s *ActionsStore) TargetsByTrigger(ctx context.Context, orgID uuid.UUID, trigger string) ([]*actions.ExecutionTarget, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, action_id, trigger, ord
		 FROM execution_targets
		 WHERE org_id = $1 AND trigger = $2
		 ORDER BY ord ASC`, orgID, trigger)
	if err != nil {
		return nil, fmt.Errorf("actions targets: %w", err)
	}
	defer rows.Close()
	var out []*actions.ExecutionTarget
	for rows.Next() {
		t := &actions.ExecutionTarget{}
		if err := rows.Scan(&t.ID, &t.ActionID, &t.Trigger, &t.Order); err != nil {
			return nil, fmt.Errorf("scan target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
