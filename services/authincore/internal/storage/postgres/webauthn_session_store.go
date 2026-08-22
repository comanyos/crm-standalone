package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	passkeyStrategy "github.com/authincore/authincore/internal/flow/strategy/webauthn"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebAuthnSessionStore implements passkeyStrategy.SessionStore via Postgres.
type WebAuthnSessionStore struct {
	db *pgxpool.Pool
}

// Ensure interface compliance.
var _ passkeyStrategy.SessionStore = (*WebAuthnSessionStore)(nil)

func NewWebAuthnSessionStore(db *pgxpool.Pool) *WebAuthnSessionStore {
	return &WebAuthnSessionStore{db: db}
}

func (s *WebAuthnSessionStore) SaveSession(ctx context.Context, flowID uuid.UUID, data *webauthn.SessionData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal webauthn session: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO webauthn_sessions (flow_id, session, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (flow_id) DO UPDATE SET session = EXCLUDED.session, expires_at = EXCLUDED.expires_at`,
		flowID, raw, time.Now().UTC().Add(5*time.Minute),
	)
	return err
}

func (s *WebAuthnSessionStore) GetSession(ctx context.Context, flowID uuid.UUID) (*webauthn.SessionData, error) {
	var raw []byte
	err := s.db.QueryRow(ctx,
		`SELECT session FROM webauthn_sessions WHERE flow_id = $1 AND expires_at > NOW()`,
		flowID,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("webauthn session not found or expired")
		}
		return nil, fmt.Errorf("get webauthn session: %w", err)
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, fmt.Errorf("unmarshal webauthn session: %w", err)
	}
	return &sd, nil
}

func (s *WebAuthnSessionStore) DeleteSession(ctx context.Context, flowID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM webauthn_sessions WHERE flow_id = $1`, flowID)
	return err
}
