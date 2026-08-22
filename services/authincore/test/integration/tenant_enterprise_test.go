//go:build integration && enterprise

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/authincore/authincore/internal/audit"
	"github.com/authincore/authincore/internal/enterprise"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenantIsolation_EnterpriseResources(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgA := seedOrg(t, ctx, s.cmds, "enterprise-a-"+uuid.NewString())
	orgB := seedOrg(t, ctx, s.cmds, "enterprise-b-"+uuid.NewString())

	t.Run("branding", func(t *testing.T) {
		store := enterprise.NewBrandingStore(s.pool)
		require.NoError(t, store.Upsert(ctx, enterprise.Branding{OrgID: orgA, LogoURL: "https://example.test/a.svg", PrimaryColor: "#123456", Theme: json.RawMessage(`{"company_name":"A"}`)}))
		brandingB, err := store.Get(ctx, orgB)
		require.NoError(t, err)
		assert.Empty(t, brandingB.LogoURL)
		assert.Equal(t, "#6366f1", brandingB.PrimaryColor)
	})

	t.Run("login policy", func(t *testing.T) {
		store := enterprise.NewLoginPolicyStore(s.pool)
		require.NoError(t, store.Upsert(ctx, orgA, enterprise.LoginPolicy{SSOEnforced: true}))
		policyB, err := store.Get(ctx, orgB)
		require.NoError(t, err)
		assert.False(t, policyB.SSOEnforced)
	})

	t.Run("audit exports", func(t *testing.T) {
		store := audit.NewConfigStore(s.pool)
		cfg := &audit.Config{OrgID: orgA, Type: "generic_webhook", Endpoint: "https://logs.example.test/ingest", Secret: "secret", Format: "json", Enabled: true}
		require.NoError(t, store.Create(ctx, cfg))
		configsB, err := store.List(ctx, orgB)
		require.NoError(t, err)
		assert.Empty(t, configsB)
		require.NoError(t, store.Delete(ctx, orgB, cfg.ID))
		configsA, err := store.List(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, configsA, 1)
		assert.Equal(t, cfg.ID, configsA[0].ID)
	})
}
