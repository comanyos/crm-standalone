//go:build !enterprise

package main

import (
	"context"
	"log/slog"

	httpserver "github.com/authincore/authincore/internal/api/http"
	eventstorepkg "github.com/authincore/authincore/internal/eventstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// applyEnterpriseDeps is a no-op in the OSS build: enterprise-only stores
// (SSO enforcement, branding) are not compiled in.
func applyEnterpriseDeps(_ *httpserver.Deps, _ *pgxpool.Pool) {}

func startEnterpriseWorkers(_ context.Context, _ eventstorepkg.Querier, _ *pgxpool.Pool, _ *slog.Logger) {
}
