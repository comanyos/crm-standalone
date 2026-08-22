//go:build enterprise

package enterprise

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Branding holds per-org visual customisation.
type Branding struct {
	OrgID        uuid.UUID       `json:"org_id"`
	LogoURL      string          `json:"logo_url"`
	PrimaryColor string          `json:"primary_color"`
	Theme        json.RawMessage `json:"theme"`
	CustomCSS    string          `json:"custom_css"`
}

// BrandingStore persists per-org branding in org_branding.
type BrandingStore struct {
	db *pgxpool.Pool
}

func NewBrandingStore(db *pgxpool.Pool) *BrandingStore {
	return &BrandingStore{db: db}
}

// Get returns the branding for orgID, or a zero-value Branding if none is set.
func (s *BrandingStore) Get(ctx context.Context, orgID uuid.UUID) (Branding, error) {
	var b Branding
	var theme []byte
	err := s.db.QueryRow(ctx,
		`SELECT org_id, logo_url, primary_color, theme, custom_css
		 FROM org_branding WHERE org_id = $1`, orgID,
	).Scan(&b.OrgID, &b.LogoURL, &b.PrimaryColor, &theme, &b.CustomCSS)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Branding{OrgID: orgID, PrimaryColor: "#6366f1", Theme: json.RawMessage("{}")}, nil
		}
		return Branding{}, fmt.Errorf("branding: %w", err)
	}
	b.Theme = theme
	return b, nil
}

// Upsert creates or updates branding for orgID.
func (s *BrandingStore) Upsert(ctx context.Context, b Branding) error {
	theme := b.Theme
	if len(theme) == 0 {
		theme = json.RawMessage("{}")
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO org_branding (org_id, logo_url, primary_color, theme, custom_css, updated_at)
		 VALUES ($1, $2, $3, $4, $5, NOW())
		 ON CONFLICT (org_id) DO UPDATE
		 SET logo_url = EXCLUDED.logo_url,
		     primary_color = EXCLUDED.primary_color,
		     theme = EXCLUDED.theme,
		     custom_css = EXCLUDED.custom_css,
		     updated_at = NOW()`,
		b.OrgID, b.LogoURL, b.PrimaryColor, []byte(theme), b.CustomCSS,
	)
	return err
}
