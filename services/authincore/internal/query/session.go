package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (q *Queries) GetSession(ctx context.Context, id, orgID uuid.UUID) (*domsession.Session, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, identity_id, org_id, state, aal, authenticated_at, issued_at, expires_at
		 FROM sessions WHERE id = $1 AND org_id = $2`,
		id, orgID,
	)
	s := &domsession.Session{}
	if err := row.Scan(&s.ID, &s.IdentityID, &s.OrgID, &s.State, &s.AAL,
		&s.AuthenticatedAt, &s.IssuedAt, &s.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "session not found")
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

func (q *Queries) ListSessions(ctx context.Context, identityID, orgID uuid.UUID, activeOnly bool, pageSize int, pageToken string) ([]*domsession.Session, string, error) {
	// keyset pagination: pageToken is "issued_at|id" (RFC3339 timestamp + UUID).
	// Seek on (issued_at DESC, id DESC) so ordering and seeking match — the
	// documented core invariant from TASKS.md Phase B.
	var seekTime time.Time
	var seekID uuid.UUID
	hasSeek := false
	if pageToken != "" {
		parts := strings.SplitN(pageToken, "|", 2)
		if len(parts) == 2 {
			t, timeErr := time.Parse(time.RFC3339Nano, parts[0])
			id, idErr := uuid.Parse(parts[1])
			if timeErr == nil && idErr == nil {
				seekTime = t
				seekID = id
				hasSeek = true
			}
		}
	}

	var (
		rows pgx.Rows
		err  error
	)
	if activeOnly {
		rows, err = q.db.Query(ctx,
			`SELECT id, identity_id, org_id, state, aal, authenticated_at, issued_at, expires_at
			 FROM sessions
			 WHERE identity_id = $1 AND org_id = $6
			   AND state = 1
			   AND expires_at > NOW()
			   AND ($2 = false OR (issued_at < $3::timestamptz OR (issued_at = $3::timestamptz AND id < $4::uuid)))
			 ORDER BY issued_at DESC, id DESC
			 LIMIT $5`,
			identityID, hasSeek, seekTime, seekID, pageSize+1, orgID,
		)
	} else {
		rows, err = q.db.Query(ctx,
			`SELECT id, identity_id, org_id, state, aal, authenticated_at, issued_at, expires_at
			 FROM sessions
			 WHERE identity_id = $1 AND org_id = $6
			   AND ($2 = false OR (issued_at < $3::timestamptz OR (issued_at = $3::timestamptz AND id < $4::uuid)))
			 ORDER BY issued_at DESC, id DESC
			 LIMIT $5`,
			identityID, hasSeek, seekTime, seekID, pageSize+1, orgID,
		)
	}
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []*domsession.Session
	for rows.Next() {
		s := &domsession.Session{}
		if err := rows.Scan(&s.ID, &s.IdentityID, &s.OrgID, &s.State, &s.AAL,
			&s.AuthenticatedAt, &s.IssuedAt, &s.ExpiresAt); err != nil {
			return nil, "", fmt.Errorf("scan session: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list sessions scan: %w", err)
	}

	nextToken := ""
	if len(out) > pageSize {
		last := out[pageSize-1]
		nextToken = last.IssuedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID.String()
		out = out[:pageSize]
	}
	return out, nextToken, nil
}
