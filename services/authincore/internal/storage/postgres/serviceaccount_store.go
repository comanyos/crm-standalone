package postgres

import (
	"context"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	sa "github.com/authincore/authincore/internal/serviceaccount"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceAccountStore implements serviceaccount.Store.
type ServiceAccountStore struct{ db *pgxpool.Pool }

func NewServiceAccountStore(db *pgxpool.Pool) *ServiceAccountStore {
	return &ServiceAccountStore{db: db}
}

func (s *ServiceAccountStore) Insert(ctx context.Context, a *sa.ServiceAccount) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO service_accounts (id, org_id, name, client_id, secret_hash, scopes, active, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.ID, a.OrgID, a.Name, a.ClientID, a.SecretHash, a.Scopes, a.Active, a.CreatedAt,
	)
	return err
}

func (s *ServiceAccountStore) ByClientID(ctx context.Context, orgID uuid.UUID, clientID string) (*sa.ServiceAccount, error) {
	a := &sa.ServiceAccount{}
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, name, client_id, secret_hash, scopes, active, created_at
		 FROM service_accounts WHERE org_id=$1 AND client_id=$2`,
		orgID, clientID,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.ClientID, &a.SecretHash, &a.Scopes, &a.Active, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "service account not found")
		}
		return nil, fmt.Errorf("service account by client_id: %w", err)
	}
	return a, nil
}

func (s *ServiceAccountStore) List(ctx context.Context, orgID uuid.UUID) ([]*sa.ServiceAccount, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, client_id, secret_hash, scopes, active, created_at
		 FROM service_accounts WHERE org_id=$1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list service accounts: %w", err)
	}
	defer rows.Close()
	var out []*sa.ServiceAccount
	for rows.Next() {
		a := &sa.ServiceAccount{}
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.ClientID, &a.SecretHash,
			&a.Scopes, &a.Active, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan service account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *ServiceAccountStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM service_accounts WHERE id=$1 AND org_id=$2`, id, orgID)
	return err
}
