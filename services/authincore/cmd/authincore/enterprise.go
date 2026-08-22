//go:build enterprise

package main

import (
	"context"
	"log/slog"
	"time"

	httpserver "github.com/authincore/authincore/internal/api/http"
	"github.com/authincore/authincore/internal/audit"
	"github.com/authincore/authincore/internal/enterprise"
	"github.com/authincore/authincore/internal/event"
	eventstorepkg "github.com/authincore/authincore/internal/eventstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// applyEnterpriseDeps wires enterprise-only stores into the HTTP server deps.
// Compiled only with the `enterprise` build tag; the OSS build uses the no-op
// variant in enterprise_oss.go.
func applyEnterpriseDeps(d *httpserver.Deps, pool *pgxpool.Pool) {
	d.LoginPolicies = enterprise.NewLoginPolicyStore(pool)
	d.Branding = enterprise.NewBrandingStore(pool)
	d.AuditConfigs = audit.NewConfigStore(pool)
}

func startEnterpriseWorkers(ctx context.Context, es eventstorepkg.Querier, pool *pgxpool.Pool, log *slog.Logger) {
	exporter := audit.NewExporter(pool, log)
	tail := event.NewTail(es, pool, "audit_export", 500*time.Millisecond, log, exporter)
	go tail.Run(ctx)
}
