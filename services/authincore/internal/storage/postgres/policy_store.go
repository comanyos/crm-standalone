package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/authincore/authincore/internal/policy/engine"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PolicyStore implements engine.PolicyStore.
type PolicyStore struct{ db *pgxpool.Pool }

func NewPolicyStore(db *pgxpool.Pool) *PolicyStore { return &PolicyStore{db: db} }

func (s *PolicyStore) ByIDs(ctx context.Context, ids []uuid.UUID) ([]*engine.Policy, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, type, expression_code, group_ids, score_threshold
		 FROM policies WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("policies by ids: %w", err)
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func (s *PolicyStore) BoundTo(ctx context.Context, orgID uuid.UUID, bindingPoint string) ([]*engine.Policy, error) {
	rows, err := s.db.Query(ctx,
		`SELECT p.id, p.org_id, p.name, p.type, p.expression_code, p.group_ids, p.score_threshold
		 FROM policies p
		 JOIN policy_bindings pb ON pb.policy_id = p.id
		 WHERE p.org_id = $1 AND pb.binding_point = $2
		 ORDER BY pb.ord ASC`, orgID, bindingPoint)
	if err != nil {
		return nil, fmt.Errorf("policies bound to %s: %w", bindingPoint, err)
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func scanPolicies(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*engine.Policy, error) {
	var out []*engine.Policy
	for rows.Next() {
		p := &engine.Policy{}
		var groupIDsJSON []byte
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Type,
			&p.ExpressionCode, &groupIDsJSON, &p.ScoreThreshold); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		_ = json.Unmarshal(groupIDsJSON, &p.GroupIDs)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReputationStore implements engine.ReputationStore via Postgres.
type ReputationStore struct{ db *pgxpool.Pool }

func NewReputationStore(db *pgxpool.Pool) *ReputationStore { return &ReputationStore{db: db} }

func (s *ReputationStore) Score(ctx context.Context, orgID uuid.UUID, ip string) (int, error) {
	var score int
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(score, 100) FROM ip_reputation_scores WHERE org_id=$1 AND ip=$2`,
		orgID, ip).Scan(&score)
	if err != nil {
		return 100, nil // default: full score on miss
	}
	return score, nil
}

func (s *ReputationStore) Increment(ctx context.Context, orgID uuid.UUID, ip string, delta int) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO ip_reputation_scores (org_id, ip, score)
		 VALUES ($1, $2, GREATEST(0, 100 + $3))
		 ON CONFLICT (org_id, ip) DO UPDATE
		 SET score = GREATEST(0, LEAST(100, ip_reputation_scores.score + $3)), updated_at=NOW()`,
		orgID, ip, delta)
	return err
}

// GroupStore implements engine.GroupStore.
type GroupStore struct{ db *pgxpool.Pool }

func NewGroupStore(db *pgxpool.Pool) *GroupStore { return &GroupStore{db: db} }

func (s *GroupStore) IsMember(ctx context.Context, orgID, identityID, groupID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM group_members gm
			JOIN groups g ON g.id=gm.group_id
			WHERE g.org_id=$1 AND gm.identity_id=$2 AND gm.group_id=$3
		)`,
		orgID, identityID, groupID).Scan(&exists)
	return exists, err
}
