package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsentStore implements oidc.ConsentStore using Postgres.
type ConsentStore struct{ db *pgxpool.Pool }

func NewConsentStore(db *pgxpool.Pool) *ConsentStore { return &ConsentStore{db: db} }

func (s *ConsentStore) StoreRequest(ctx context.Context, req *oidc.ConsentRequest) error {
	scopesJSON, _ := json.Marshal(req.Scopes)
	_, err := s.db.Exec(ctx,
		`INSERT INTO consent_requests
		 (id, client_id, client_name, identity_id, org_id, scopes, redirect_uri, state, code_challenge, code_challenge_method, expires_at, granted)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		req.ID, req.ClientID, req.ClientName, req.IdentityID, req.OrgID,
		scopesJSON, req.RedirectURI, req.State, req.CodeChallenge, req.CodeChallengeMethod, req.ExpiresAt, req.Granted,
	)
	return err
}

func (s *ConsentStore) GetRequest(ctx context.Context, id string) (*oidc.ConsentRequest, error) {
	var scopesJSON []byte
	req := &oidc.ConsentRequest{}
	err := s.db.QueryRow(ctx,
		`SELECT id, client_id, client_name, identity_id, org_id, scopes,
		        redirect_uri, state, code_challenge, code_challenge_method, expires_at, granted
		 FROM consent_requests WHERE id=$1`, id,
	).Scan(&req.ID, &req.ClientID, &req.ClientName, &req.IdentityID, &req.OrgID,
		&scopesJSON, &req.RedirectURI, &req.State, &req.CodeChallenge, &req.CodeChallengeMethod, &req.ExpiresAt, &req.Granted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "consent request not found")
		}
		return nil, fmt.Errorf("get consent request: %w", err)
	}
	_ = json.Unmarshal(scopesJSON, &req.Scopes)
	return req, nil
}

func (s *ConsentStore) UpdateRequest(ctx context.Context, req *oidc.ConsentRequest) error {
	scopesJSON, _ := json.Marshal(req.Scopes)
	_, err := s.db.Exec(ctx,
		`UPDATE consent_requests SET granted=$1, scopes=$2 WHERE id=$3`,
		req.Granted, scopesJSON, req.ID,
	)
	return err
}

func (s *ConsentStore) RememberedScopes(ctx context.Context, orgID, identityID uuid.UUID, clientID string) ([]string, error) {
	var scopesJSON []byte
	err := s.db.QueryRow(ctx,
		`SELECT scopes FROM consent_grants
		 WHERE org_id=$1 AND identity_id=$2 AND client_id=$3`,
		orgID, identityID, clientID,
	).Scan(&scopesJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("remembered scopes: %w", err)
	}
	var scopes []string
	_ = json.Unmarshal(scopesJSON, &scopes)
	return scopes, nil
}

func (s *ConsentStore) RememberGrant(ctx context.Context, orgID, identityID uuid.UUID, clientID string, scopes []string) error {
	scopesJSON, _ := json.Marshal(scopes)
	_, err := s.db.Exec(ctx,
		`INSERT INTO consent_grants (org_id, identity_id, client_id, scopes, updated_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (org_id, identity_id, client_id) DO UPDATE
		   SET scopes=EXCLUDED.scopes, updated_at=EXCLUDED.updated_at`,
		orgID, identityID, clientID, scopesJSON, time.Now().UTC(),
	)
	return err
}
