package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The PostgreSQL half of the Google integration store
// (migrations/0019_google_connections.sql).

// googleConnectionColumns is the read column list, in scan order. One list so
// Get and List cannot drift on which columns come back.
const googleConnectionColumns = `id, owner_id, google_account_email, status, granted_scopes,
	token_ciphertext, access_token_expires_at, connected_by, connected_at,
	last_refreshed_at, disconnected_at, last_error, created_at, updated_at`

func scanGoogleConnection(row pgx.Row) (*GoogleConnection, error) {
	var (
		v          GoogleConnection
		scopes     string
		ciphertext []byte
		expires    *time.Time
		connected  *time.Time
		refreshed  *time.Time
		disconnect *time.Time
	)
	if err := row.Scan(&v.ID, &v.OwnerID, &v.GoogleAccountEmail, &v.Status, &scopes,
		&ciphertext, &expires, &v.ConnectedBy, &connected,
		&refreshed, &disconnect, &v.LastError, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.GrantedScopes = splitScopes(scopes)
	v.TokenCiphertext = ciphertext
	v.AccessTokenExpiresAt = timeOrZero(expires)
	v.ConnectedAt = timeOrZero(connected)
	v.LastRefreshedAt = timeOrZero(refreshed)
	v.DisconnectedAt = timeOrZero(disconnect)
	return &v, nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// CreateGoogleOAuthState records a pending authorization before the browser
// is redirected to Google.
//
// The unique index on `state` is global rather than per-tenant, so a
// collision surfaces here as ErrConflict instead of letting two tenants own
// the same callback token. With 256 bits of entropy this cannot happen by
// chance; it is handled because "cannot happen" is not a security property.
func (s *PgStore) CreateGoogleOAuthState(
	ctx context.Context, scope Scope, v *GoogleOAuthState,
) (*GoogleOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneGoogleOAuthState(v)
	record.TenantID = scope.TenantID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	record.ConsumedAt = time.Time{}
	if err := record.Validate(); err != nil {
		return nil, err
	}

	_, err := s.db.Exec(ctx, scope,
		`INSERT INTO google_oauth_states
		     (tenant_id, state, actor_id, verifier_ciphertext, return_path, created_at, expires_at, consumed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,NULL)`,
		scope.TenantID, record.State, record.ActorID, record.VerifierCiphertext,
		record.ReturnPath, record.CreatedAt, record.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create google oauth state: %w", err)
	}
	return record, nil
}

// ConsumeGoogleOAuthState redeems a pending authorization exactly once.
//
// The check and the mark are one statement: the UPDATE's WHERE clause is what
// tests "still unconsumed and unexpired", so two concurrent callbacks race on
// the row lock and exactly one of them updates it. Reading first and then
// updating would let both observe an unconsumed state, which is a replayable
// authorization.
func (s *PgStore) ConsumeGoogleOAuthState(
	ctx context.Context, scope Scope, state string, now time.Time,
) (*GoogleOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	stamp := now.UTC().Truncate(storedTimePrecision)

	var (
		v          GoogleOAuthState
		consumedAt *time.Time
	)
	err := s.db.QueryRow(ctx, scope,
		`UPDATE google_oauth_states
		    SET consumed_at = $1
		  WHERE tenant_id = $2
		    AND state = $3
		    AND consumed_at IS NULL
		    AND expires_at > $1
		RETURNING state, actor_id, verifier_ciphertext, return_path, created_at, expires_at, consumed_at`,
		stamp, scope.TenantID, state).Scan(&v.State, &v.ActorID, &v.VerifierCiphertext, &v.ReturnPath,
		&v.CreatedAt, &v.ExpiresAt, &consumedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Consumed, expired, never existed, or another tenant's: all one
			// answer. The caller is an unauthenticated redirect and must not
			// be able to tell them apart.
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("consume google oauth state: %w", err)
	}
	v.TenantID = scope.TenantID
	v.ConsumedAt = timeOrZero(consumedAt)
	return &v, nil
}

// PurgeExpiredGoogleOAuthStates deletes states that can no longer be
// redeemed, consumed ones included: once redeemed the record has no further
// use, and keeping the sealed verifier has only downside.
func (s *PgStore) PurgeExpiredGoogleOAuthStates(
	ctx context.Context, scope Scope, now time.Time,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM google_oauth_states
		  WHERE tenant_id = $1 AND (expires_at <= $2 OR consumed_at IS NOT NULL)`,
		scope.TenantID, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge google oauth states: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ResolveGoogleOAuthStateTenant answers "which tenant does this callback
// belong to" for a redirect that carries no credentials.
//
// It runs on the drain pool for the same reason DueOutboxTenants does: there
// is no tenant to bind yet, so RLS would filter the answer away. It is held
// to the same discipline — it returns one identifier and nothing else, and
// migration 0019 grants crm_drain SELECT on this table only, never on
// google_connections, which holds the sealed credentials.
func (s *PgStore) ResolveGoogleOAuthStateTenant(ctx context.Context, state string) (string, error) {
	if state == "" {
		return "", ErrNotFound
	}
	var tenantID string
	err := s.drain.QueryRow(ctx,
		`SELECT tenant_id FROM google_oauth_states WHERE state = $1`, state).Scan(&tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("resolve google oauth state tenant: %w", err)
	}
	return tenantID, nil
}

// SaveGoogleConnection upserts the connection for v.OwnerID.
//
// ON CONFLICT on (tenant_id, owner_id) rather than a read-then-write: two
// concurrent authorizations by the same user would otherwise both find no row
// and insert, leaving two live refresh tokens for one mailbox — and a later
// disconnect would revoke only whichever one the read happened to return.
func (s *PgStore) SaveGoogleConnection(
	ctx context.Context, scope Scope, v *GoogleConnection,
) (*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneGoogleConnection(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	record.CreatedAt, record.UpdatedAt = now, now

	row := s.db.QueryRow(ctx, scope,
		`INSERT INTO google_connections
		     (tenant_id, id, owner_id, google_account_email, status, granted_scopes,
		      token_ciphertext, access_token_expires_at, connected_by, connected_at,
		      last_refreshed_at, disconnected_at, last_error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		 ON CONFLICT (tenant_id, owner_id) DO UPDATE SET
		      google_account_email    = EXCLUDED.google_account_email,
		      status                  = EXCLUDED.status,
		      granted_scopes          = EXCLUDED.granted_scopes,
		      token_ciphertext        = EXCLUDED.token_ciphertext,
		      access_token_expires_at = EXCLUDED.access_token_expires_at,
		      connected_by            = EXCLUDED.connected_by,
		      connected_at            = EXCLUDED.connected_at,
		      last_refreshed_at       = EXCLUDED.last_refreshed_at,
		      disconnected_at         = EXCLUDED.disconnected_at,
		      last_error              = EXCLUDED.last_error,
		      updated_at              = EXCLUDED.updated_at
		 RETURNING `+googleConnectionColumns,
		scope.TenantID, record.ID, record.OwnerID, record.GoogleAccountEmail,
		record.Status, joinScopes(record.GrantedScopes),
		nullBytes(record.TokenCiphertext), nullTimeValue(record.AccessTokenExpiresAt),
		record.ConnectedBy, nullTimeValue(record.ConnectedAt),
		nullTimeValue(record.LastRefreshedAt), nullTimeValue(record.DisconnectedAt),
		record.LastError, now)
	out, err := scanGoogleConnection(row)
	if err != nil {
		return nil, fmt.Errorf("save google connection: %w", err)
	}
	return out, nil
}

// GetGoogleConnection reads one owner's connection in the scope's tenant.
func (s *PgStore) GetGoogleConnection(
	ctx context.Context, scope Scope, ownerID string,
) (*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	row := s.db.QueryRow(ctx, scope,
		`SELECT `+googleConnectionColumns+` FROM google_connections
		  WHERE tenant_id = $1 AND owner_id = $2`, scope.TenantID, ownerID)
	out, err := scanGoogleConnection(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get google connection: %w", err)
	}
	return out, nil
}

// ListGoogleConnections returns the tenant's connections, ordered by owner.
func (s *PgStore) ListGoogleConnections(
	ctx context.Context, scope Scope,
) ([]*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+googleConnectionColumns+` FROM google_connections
		  WHERE tenant_id = $1 ORDER BY owner_id`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list google connections: %w", err)
	}
	defer rows.Close()

	out := make([]*GoogleConnection, 0, 4)
	for rows.Next() {
		record, err := scanGoogleConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan google connection: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// DeleteGoogleConnection erases the row entirely — the purge a user asks for
// when they want no record of the link at all, as distinct from a disconnect,
// which revokes the credential and keeps a tombstone for audit.
func (s *PgStore) DeleteGoogleConnection(ctx context.Context, scope Scope, ownerID string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM google_connections WHERE tenant_id = $1 AND owner_id = $2`,
		scope.TenantID, ownerID)
	if err != nil {
		return fmt.Errorf("delete google connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// nullBytes sends NULL rather than an empty bytea, so the table's
// "disconnected holds no tokens" CHECK is written against NULL and a
// zero-length ciphertext cannot masquerade as a credential.
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
