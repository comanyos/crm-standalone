package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionRepo implements command.SessionRepository against Postgres.
type SessionRepo struct {
	db *pgxpool.Pool
}

func NewSessionRepo(db *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{db: db}
}

func (r *SessionRepo) Insert(ctx context.Context, s *domsession.Session) error {
	methods, _ := json.Marshal(s.AuthenticationMethods)
	_, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`INSERT INTO sessions
		   (id, identity_id, org_id, state, aal, authentication_methods,
		    authenticated_at, issued_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		s.ID, s.IdentityID, s.OrgID, int(s.State), int(s.AAL), methods,
		s.AuthenticatedAt, s.IssuedAt, s.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("session insert: %w", err)
	}
	return nil
}

func (r *SessionRepo) UpdateState(ctx context.Context, id, orgID uuid.UUID, state domsession.State) error {
	ct, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`UPDATE sessions SET state=$1, updated_at=$2 WHERE id=$3 AND org_id=$4`,
		int(state), time.Now().UTC(), id, orgID,
	)
	if err != nil {
		return fmt.Errorf("session update state: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "session not found")
	}
	return nil
}

func (r *SessionRepo) UpdateExpiry(ctx context.Context, id, orgID uuid.UUID, expiresAt time.Time) error {
	ct, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`UPDATE sessions SET expires_at=$1, updated_at=$2 WHERE id=$3 AND org_id=$4`,
		expiresAt, time.Now().UTC(), id, orgID,
	)
	if err != nil {
		return fmt.Errorf("session update expiry: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "session not found")
	}
	return nil
}

// ByToken looks up an active session by its bearer token (JWT subject = identityID,
// jti = sessionID). The token is validated upstream; here we just confirm the
// session row is still active in the DB.
func (r *SessionRepo) ByToken(ctx context.Context, sessionID string) (identityID, sessID, orgID uuid.UUID, err error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domainerr.New(domainerr.CodeInvalidArgument, "invalid session id")
	}
	row := database.ExecutorFor(ctx, r.db).QueryRow(ctx,
		`SELECT identity_id, id, org_id FROM sessions
		 WHERE id=$1 AND state=$2 AND expires_at > now()`,
		sid, int(domsession.StateActive),
	)
	if scanErr := row.Scan(&identityID, &sessID, &orgID); scanErr != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, domainerr.New(domainerr.CodeNotFound, "session not found or expired")
	}
	return identityID, sessID, orgID, nil
}

// RevokeAllByIdentity sets all active sessions for an identity to inactive.
func (r *SessionRepo) RevokeAllByIdentity(ctx context.Context, identityID, orgID uuid.UUID) (int64, error) {
	ct, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`UPDATE sessions SET state=$1, updated_at=$2
		 WHERE identity_id=$3 AND org_id=$4 AND state=$5`,
		int(domsession.StateInactive), time.Now().UTC(),
		identityID, orgID, int(domsession.StateActive),
	)
	if err != nil {
		return 0, fmt.Errorf("revoke all sessions: %w", err)
	}
	return ct.RowsAffected(), nil
}
