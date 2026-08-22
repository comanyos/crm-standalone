// Package main is the authinctl CLI — admin operations: migrate, blueprint apply, etc.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/authincore/authincore/internal/blueprint"
	pgrepo "github.com/authincore/authincore/internal/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
	"github.com/pressly/goose/v3"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "migrate":
		sub := "up"
		if len(os.Args) >= 3 {
			sub = os.Args[2]
		}
		err = cmdMigrate(ctx, sub)
	case "blueprint":
		if len(os.Args) < 4 || os.Args[2] != "apply" {
			fmt.Fprintln(os.Stderr, "usage: authinctl blueprint apply <file.yaml>")
			os.Exit(1)
		}
		err = cmdBlueprintApply(ctx, os.Args[3])
	case "bootstrap":
		err = cmdBootstrap(ctx)
	case "reset-admin-password":
		err = cmdResetAdminPassword(ctx)
	case "version":
		fmt.Println("authincore dev")
	default:
		printUsage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`authinctl — authincore CLI

Commands:
  migrate [up|down|status|reset]   Run database migrations (default: up)
  blueprint apply <file.yaml>      Apply a declarative blueprint
  bootstrap                        Create/reconcile the initial CRM organization and administrator
  reset-admin-password             Replace one existing administrator's password (never creates)
  version                          Show version

reset-admin-password reads RESET_ORG_DOMAIN, RESET_ADMIN_EMAIL,
RESET_ADMIN_PASSWORD and RESET_ADMIN_CONFIRM. Set RESET_ADMIN_CONFIRM to the
same address as RESET_ADMIN_EMAIL to confirm. The bootstrap command never
changes an existing password, by design.
`)
}

func cmdMigrate(_ context.Context, sub string) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}
	migrationsDir := envOr("MIGRATIONS_DIR", "./migrations")

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	switch sub {
	case "up":
		fmt.Println("Running migrations (up) against the configured database")
		return goose.Up(db, migrationsDir)
	case "down":
		fmt.Printf("Rolling back one migration\n")
		return goose.Down(db, migrationsDir)
	case "status":
		return goose.Status(db, migrationsDir)
	case "reset":
		fmt.Printf("Resetting all migrations\n")
		return goose.Reset(db, migrationsDir)
	default:
		return fmt.Errorf("unknown migrate sub-command: %q (use up/down/status/reset)", sub)
	}
}

func cmdBlueprintApply(ctx context.Context, file string) error {
	// #nosec G304,G703 -- this is an explicit CLI file argument selected by the invoking administrator.
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}
	orgID, err := uuid.Parse(os.Getenv("ORG_ID"))
	if err != nil || orgID == uuid.Nil {
		return fmt.Errorf("ORG_ID must be a valid non-zero UUID")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	store := pgrepo.NewBlueprintStore(pool)
	applier := blueprint.NewApplier(store, slog.Default(), pgrepo.NewBlueprintReconcilers(pool)...)
	bp, diff, err := applier.Apply(ctx, orgID, string(data), strings.EqualFold(os.Getenv("DRY_RUN"), "true"))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"blueprint": bp, "diff": diff})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
