package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OIDCTokenStore implements oidc.TokenStore.
// Auth codes are stored in the oauth2_auth_codes table; refresh tokens in oauth2_refresh_tokens.
// Both tables are created in migration 002 (below).
type OIDCTokenStore struct {
	db *pgxpool.Pool
}

func NewOIDCTokenStore(db *pgxpool.Pool) *OIDCTokenStore {
	return &OIDCTokenStore{db: db}
}

func (s *OIDCTokenStore) StoreAuthCode(ctx context.Context, code *oidc.AuthCode) error {
	scopes, _ := json.Marshal(code.Scopes)
	_, err := s.db.Exec(ctx,
		`INSERT INTO oauth2_auth_codes
		   (code, client_id, identity_id, org_id, session_id,
		    redirect_uri, scopes, code_challenge, code_challenge_method, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		code.Code, code.ClientID, code.IdentityID, code.OrgID, code.SessionID,
		code.RedirectURI, scopes, code.CodeChallenge, code.CodeChallengeMethod, code.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("store auth code: %w", err)
	}
	return nil
}

func (s *OIDCTokenStore) ConsumeAuthCode(ctx context.Context, code string) (*oidc.AuthCode, error) {
	var ac oidc.AuthCode
	var scopesJSON []byte

	// Single atomic CTE: select + delete in one round-trip.
	err := s.db.QueryRow(ctx,
		`WITH deleted AS (
		   DELETE FROM oauth2_auth_codes WHERE code = $1 AND expires_at > NOW()
		   RETURNING code, client_id, identity_id, org_id, session_id,
		             redirect_uri, scopes, code_challenge, code_challenge_method, expires_at
		 ) SELECT * FROM deleted`,
		code,
	).Scan(
		&ac.Code, &ac.ClientID, &ac.IdentityID, &ac.OrgID, &ac.SessionID,
		&ac.RedirectURI, &scopesJSON, &ac.CodeChallenge, &ac.CodeChallengeMethod, &ac.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "auth code not found or expired")
		}
		return nil, fmt.Errorf("consume auth code: %w", err)
	}
	_ = json.Unmarshal(scopesJSON, &ac.Scopes)
	return &ac, nil
}

func (s *OIDCTokenStore) StoreRefreshToken(ctx context.Context, rt *oidc.RefreshToken) error {
	scopes, _ := json.Marshal(rt.Scopes)
	_, err := s.db.Exec(ctx,
		`INSERT INTO oauth2_refresh_tokens
		   (token, identity_id, session_id, org_id, client_id, scopes, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		rt.Token, rt.IdentityID, rt.SessionID, rt.OrgID, rt.ClientID, scopes, rt.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("store refresh token: %w", err)
	}
	return nil
}

func (s *OIDCTokenStore) ConsumeRefreshToken(ctx context.Context, token string) (*oidc.RefreshToken, error) {
	var rt oidc.RefreshToken
	var scopesJSON []byte

	err := s.db.QueryRow(ctx,
		`WITH deleted AS (
		   DELETE FROM oauth2_refresh_tokens WHERE token = $1 AND expires_at > NOW() AND revoked_at IS NULL
		   RETURNING token, identity_id, session_id, org_id, client_id, scopes, expires_at
		 ) SELECT * FROM deleted`,
		token,
	).Scan(
		&rt.Token, &rt.IdentityID, &rt.SessionID, &rt.OrgID, &rt.ClientID, &scopesJSON, &rt.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "refresh token not found, expired, or revoked")
		}
		return nil, fmt.Errorf("consume refresh token: %w", err)
	}
	_ = json.Unmarshal(scopesJSON, &rt.Scopes)
	return &rt, nil
}

func (s *OIDCTokenStore) RevokeRefreshToken(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE oauth2_refresh_tokens SET revoked_at = $1 WHERE token = $2`,
		time.Now().UTC(), token,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}
