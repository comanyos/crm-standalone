package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	oidcstrategy "github.com/authincore/authincore/internal/flow/strategy/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OIDCProviderStore implements oidcstrategy.ProviderRegistry and oidcstrategy.IdentityLinker.
type OIDCProviderStore struct {
	db *pgxpool.Pool
}

func NewOIDCProviderStore(db *pgxpool.Pool) *OIDCProviderStore {
	return &OIDCProviderStore{db: db}
}

// --- ProviderRegistry ---

func (s *OIDCProviderStore) List(ctx context.Context, orgID uuid.UUID) ([]*oidcstrategy.Provider, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, name, config FROM oidc_providers WHERE org_id = $1 AND active = true ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list oidc providers: %w", err)
	}
	defer rows.Close()

	var out []*oidcstrategy.Provider
	for rows.Next() {
		var id, name string
		var cfgJSON []byte
		if err := rows.Scan(&id, &name, &cfgJSON); err != nil {
			return nil, fmt.Errorf("scan oidc provider: %w", err)
		}
		p := &oidcstrategy.Provider{ID: id, Name: name}
		_ = json.Unmarshal(cfgJSON, p)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *OIDCProviderStore) ByID(ctx context.Context, orgID uuid.UUID, providerID string) (*oidcstrategy.Provider, error) {
	var name string
	var cfgJSON []byte
	err := s.db.QueryRow(ctx,
		`SELECT name, config FROM oidc_providers WHERE org_id = $1 AND id = $2 AND active = true`,
		orgID, providerID,
	).Scan(&name, &cfgJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "oidc provider not found")
		}
		return nil, fmt.Errorf("get oidc provider: %w", err)
	}
	p := &oidcstrategy.Provider{ID: providerID, Name: name}
	_ = json.Unmarshal(cfgJSON, p)
	return p, nil
}

// --- IdentityLinker ---

type socialLinkRow struct {
	IdentityID uuid.UUID
}

// LinkOrCreate finds an existing social link or creates a new identity + link.
func (s *OIDCProviderStore) LinkOrCreate(
	ctx context.Context,
	orgID uuid.UUID,
	p *oidcstrategy.Provider,
	subject string,
	claims json.RawMessage,
) (uuid.UUID, bool, error) {
	// Fast path: known link.
	var existingID uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT identity_id FROM social_links
		 WHERE org_id = $1 AND provider_id = $2 AND subject = $3`,
		orgID, p.ID, subject,
	).Scan(&existingID)
	if err == nil {
		// Update the cached claims.
		_, _ = s.db.Exec(ctx,
			`UPDATE social_links SET raw_claims = $1 WHERE org_id = $2 AND provider_id = $3 AND subject = $4`,
			claims, orgID, p.ID, subject,
		)
		return existingID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("check social link: %w", err)
	}

	// Extract email from claims for identity creation.
	var userInfo struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = json.Unmarshal(claims, &userInfo)

	traits, _ := json.Marshal(map[string]any{
		"email": userInfo.Email,
		"name":  userInfo.Name,
	})

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("link or create tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	newID := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO identities
		   (id, org_id, schema_id, traits, state, available_aal, created_at, updated_at)
		 VALUES ($1, $2, 'default', $3, 1, 0, NOW(), NOW())
		 ON CONFLICT DO NOTHING`,
		newID, orgID, traits,
	)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("link or create identity insert: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO social_links
		   (id, org_id, identity_id, provider_id, subject, raw_claims, created_at)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW())`,
		orgID, newID, p.ID, subject, claims,
	)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("insert social link: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, fmt.Errorf("link or create commit: %w", err)
	}
	return newID, true, nil
}

// Create inserts a new OIDC provider for the given org.
func (s *OIDCProviderStore) Create(ctx context.Context, orgID uuid.UUID, p *oidcstrategy.Provider) error {
	// #nosec G117 -- provider credentials must be persisted; production database encryption and SecretCore/KMS custody are deployment requirements.
	cfg, _ := json.Marshal(p)
	_, err := s.db.Exec(ctx,
		`INSERT INTO oidc_providers (id, org_id, name, config, active, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, true, NOW(), NOW())
		 ON CONFLICT (org_id, id) DO UPDATE SET name=$3, config=$4, active=true, updated_at=NOW()`,
		p.ID, orgID, p.Name, cfg,
	)
	return err
}

// Delete soft-deletes a provider by setting active=false.
func (s *OIDCProviderStore) Delete(ctx context.Context, orgID uuid.UUID, providerID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE oidc_providers SET active=false, updated_at=NOW() WHERE org_id=$1 AND id=$2`,
		orgID, providerID,
	)
	return err
}
