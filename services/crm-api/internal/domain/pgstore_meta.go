package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const metaConnectionColumns = `id, meta_user_id, meta_user_name, status, granted_permissions,
 user_token_ciphertext, page_token_ciphertext, access_token_expires_at,
 business_id, business_name, page_id, page_name, form_id, form_name, connected_by, connected_at,
 last_webhook_at, last_import_at, disconnected_at, last_error, created_at, updated_at`

func scanMetaConnection(row pgx.Row) (*MetaConnection, error) {
	var v MetaConnection
	var accessExpiry, connectedAt, lastWebhookAt, lastImportAt, disconnectedAt *time.Time
	var granted string
	err := row.Scan(&v.ID, &v.MetaUserID, &v.MetaUserName, &v.Status, &granted,
		&v.UserTokenCiphertext, &v.PageTokenCiphertext, &accessExpiry,
		&v.BusinessID, &v.BusinessName,
		&v.PageID, &v.PageName, &v.FormID, &v.FormName, &v.ConnectedBy, &connectedAt,
		&lastWebhookAt, &lastImportAt, &disconnectedAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	v.GrantedPermissions = splitScopes(granted)
	v.AccessTokenExpiresAt = timeOrZero(accessExpiry)
	v.ConnectedAt = timeOrZero(connectedAt)
	v.LastWebhookAt = timeOrZero(lastWebhookAt)
	v.LastImportAt = timeOrZero(lastImportAt)
	v.DisconnectedAt = timeOrZero(disconnectedAt)
	return &v, nil
}

func (s *PgStore) CreateMetaOAuthState(ctx context.Context, scope Scope, v *MetaOAuthState) (*MetaOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMetaOAuthState(v)
	record.TenantID = scope.TenantID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	record.ConsumedAt = time.Time{}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	_, err := s.db.Exec(ctx, scope, `INSERT INTO meta_oauth_states
 (tenant_id,state,actor_id,return_path,created_at,expires_at,consumed_at)
 VALUES ($1,$2,$3,$4,$5,$6,NULL)`, scope.TenantID, record.State, record.ActorID,
		record.ReturnPath, record.CreatedAt, record.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create Meta OAuth state: %w", err)
	}
	return record, nil
}

func (s *PgStore) ConsumeMetaOAuthState(ctx context.Context, scope Scope, state string, now time.Time) (*MetaOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	stamp := now.UTC().Truncate(storedTimePrecision)
	var v MetaOAuthState
	var consumedAt *time.Time
	err := s.db.QueryRow(ctx, scope, `UPDATE meta_oauth_states SET consumed_at=$1
 WHERE tenant_id=$2 AND state=$3 AND consumed_at IS NULL AND expires_at>$1
 RETURNING state,actor_id,return_path,created_at,expires_at,consumed_at`,
		stamp, scope.TenantID, state).Scan(&v.State, &v.ActorID, &v.ReturnPath,
		&v.CreatedAt, &v.ExpiresAt, &consumedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("consume Meta OAuth state: %w", err)
	}
	v.TenantID = scope.TenantID
	v.ConsumedAt = timeOrZero(consumedAt)
	return &v, nil
}

func (s *PgStore) PurgeExpiredMetaOAuthStates(ctx context.Context, scope Scope, now time.Time) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM meta_oauth_states
 WHERE tenant_id=$1 AND (expires_at<=$2 OR consumed_at IS NOT NULL)`, scope.TenantID, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("purge Meta OAuth states: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgStore) ResolveMetaOAuthStateTenant(ctx context.Context, state string) (string, error) {
	if state == "" {
		return "", ErrNotFound
	}
	var tenantID string
	err := s.drain.QueryRow(ctx, `SELECT tenant_id FROM meta_oauth_states WHERE state=$1`, state).Scan(&tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("resolve Meta OAuth state tenant: %w", err)
	}
	return tenantID, nil
}

func (s *PgStore) SaveMetaConnection(ctx context.Context, scope Scope, v *MetaConnection) (*MetaConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMetaConnection(v)
	if record.ID == "" {
		record.ID = newID()
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	now := nowStamp()
	row := s.db.QueryRow(ctx, scope, `INSERT INTO meta_connections
 (tenant_id,id,meta_user_id,meta_user_name,status,granted_permissions,
  user_token_ciphertext,page_token_ciphertext,access_token_expires_at,
  business_id,business_name,page_id,page_name,form_id,form_name,connected_by,connected_at,last_webhook_at,
  last_import_at,disconnected_at,last_error,created_at,updated_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$22)
 ON CONFLICT (tenant_id) DO UPDATE SET
  meta_user_id=EXCLUDED.meta_user_id, meta_user_name=EXCLUDED.meta_user_name,
  status=EXCLUDED.status, granted_permissions=EXCLUDED.granted_permissions,
  user_token_ciphertext=EXCLUDED.user_token_ciphertext,
  page_token_ciphertext=EXCLUDED.page_token_ciphertext,
  access_token_expires_at=EXCLUDED.access_token_expires_at,
  business_id=EXCLUDED.business_id,business_name=EXCLUDED.business_name,
  page_id=EXCLUDED.page_id,page_name=EXCLUDED.page_name,
  form_id=EXCLUDED.form_id,form_name=EXCLUDED.form_name,
  connected_by=EXCLUDED.connected_by,connected_at=EXCLUDED.connected_at,
  last_webhook_at=EXCLUDED.last_webhook_at,last_import_at=EXCLUDED.last_import_at,
  disconnected_at=EXCLUDED.disconnected_at,last_error=EXCLUDED.last_error,
  updated_at=EXCLUDED.updated_at
 RETURNING `+metaConnectionColumns,
		scope.TenantID, record.ID, record.MetaUserID, record.MetaUserName, record.Status,
		joinScopes(record.GrantedPermissions), nullBytes(record.UserTokenCiphertext),
		nullBytes(record.PageTokenCiphertext), nullTimeValue(record.AccessTokenExpiresAt),
		record.BusinessID, record.BusinessName,
		record.PageID, record.PageName, record.FormID, record.FormName, record.ConnectedBy,
		nullTimeValue(record.ConnectedAt), nullTimeValue(record.LastWebhookAt),
		nullTimeValue(record.LastImportAt), nullTimeValue(record.DisconnectedAt),
		record.LastError, now)
	out, err := scanMetaConnection(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("save Meta connection: %w", err)
	}
	return out, nil
}

func (s *PgStore) GetMetaConnection(ctx context.Context, scope Scope) (*MetaConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanMetaConnection(s.db.QueryRow(ctx, scope,
		`SELECT `+metaConnectionColumns+` FROM meta_connections WHERE tenant_id=$1`, scope.TenantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get Meta connection: %w", err)
	}
	return out, nil
}

func (s *PgStore) DeleteMetaConnection(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM meta_connections WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete Meta connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) ResolveMetaConnections(ctx context.Context, pageID, formID string) ([]MetaConnectionRef, error) {
	if pageID == "" || formID == "" {
		return nil, ErrNotFound
	}
	rows, err := s.drain.Query(ctx, `SELECT tenant_id,id,page_token_ciphertext,business_id,page_id,form_id,status
 FROM meta_connections WHERE page_id=$1 AND form_id=$2 AND status='connected'`, pageID, formID)
	if err != nil {
		return nil, fmt.Errorf("resolve Meta webhook destinations: %w", err)
	}
	defer rows.Close()
	out := make([]MetaConnectionRef, 0, 1)
	for rows.Next() {
		var v MetaConnectionRef
		if err := rows.Scan(&v.TenantID, &v.ConnectionID, &v.PageTokenCiphertext, &v.BusinessID, &v.PageID, &v.FormID, &v.Status); err != nil {
			return nil, fmt.Errorf("scan Meta webhook destination: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func scanMetaAttribution(row pgx.Row) (*MetaLeadAttribution, error) {
	var v MetaLeadAttribution
	var providerCreatedAt *time.Time
	err := row.Scan(&v.LeadID, &v.ConnectionID, &v.ProviderLeadID, &v.Platform,
		&v.PageID, &v.FormID, &v.AdID, &v.AdName, &v.AdSetID, &v.AdSetName,
		&v.CampaignID, &v.CampaignName, &providerCreatedAt, &v.CapturedAt, &v.RawPayload)
	v.ProviderCreatedAt = timeOrZero(providerCreatedAt)
	return &v, err
}

func (s *PgStore) GetMetaLeadAttribution(ctx context.Context, scope Scope, leadID string) (*MetaLeadAttribution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanMetaAttribution(s.db.QueryRow(ctx, scope, `SELECT lead_id,connection_id,provider_lead_id,platform,
 page_id,form_id,ad_id,ad_name,adset_id,adset_name,campaign_id,campaign_name,
 provider_created_at,captured_at,raw_payload FROM meta_lead_attribution
 WHERE tenant_id=$1 AND lead_id=$2`, scope.TenantID, leadID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get Meta lead attribution: %w", err)
	}
	return out, nil
}

func (s *PgStore) CreateMetaCapturedLead(ctx context.Context, scope Scope, lead *Lead, attribution *MetaLeadAttribution) (*Lead, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if lead == nil || attribution == nil {
		return nil, false, ErrInvalid
	}
	record := cloneMetaAttribution(attribution)
	if record.CapturedAt.IsZero() {
		record.CapturedAt = nowStamp()
	}
	if err := record.Validate(); err != nil {
		return nil, false, err
	}
	var stored *Lead
	created := false
	err := s.db.transaction(ctx, scope, func(txCtx context.Context) error {
		var existingLeadID string
		err := s.db.QueryRow(txCtx, scope, `SELECT lead_id FROM meta_lead_attribution
 WHERE tenant_id=$1 AND provider_lead_id=$2`, scope.TenantID, record.ProviderLeadID).Scan(&existingLeadID)
		if err == nil {
			stored, err = s.GetLead(txCtx, scope, existingLeadID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		stored, err = s.CreateLead(txCtx, scope, lead)
		if err != nil {
			return err
		}
		record.LeadID = stored.ID
		_, err = s.db.Exec(txCtx, scope, `INSERT INTO meta_lead_attribution
 (tenant_id,lead_id,connection_id,provider_lead_id,platform,page_id,form_id,
  ad_id,ad_name,adset_id,adset_name,campaign_id,campaign_name,provider_created_at,captured_at,raw_payload)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			scope.TenantID, record.LeadID, record.ConnectionID, record.ProviderLeadID,
			record.Platform, record.PageID, record.FormID, record.AdID, record.AdName,
			record.AdSetID, record.AdSetName, record.CampaignID, record.CampaignName,
			nullTimeValue(record.ProviderCreatedAt), record.CapturedAt, record.RawPayload)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	if err == nil {
		return stored, created, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		var existingLeadID string
		lookupErr := s.db.QueryRow(ctx, scope, `SELECT lead_id FROM meta_lead_attribution
 WHERE tenant_id=$1 AND provider_lead_id=$2`, scope.TenantID, record.ProviderLeadID).Scan(&existingLeadID)
		if lookupErr == nil {
			stored, lookupErr = s.GetLead(ctx, scope, existingLeadID)
			return stored, false, lookupErr
		}
	}
	return nil, false, fmt.Errorf("create Meta-captured lead: %w", err)
}
