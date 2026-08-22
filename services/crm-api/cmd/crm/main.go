// Command entrypoint: loads config, opens the store, and serves until
// interrupted.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/dispatch"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/platform"
	"github.com/santoshguru/crm/internal/server"
	"github.com/santoshguru/crm/internal/tracing"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	shutdownTracing, err := tracing.Init(context.Background(), "crm-api")
	if err != nil {
		slog.Error("tracing init failed")
		os.Exit(1)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	authCtx, authCancel := context.WithTimeout(context.Background(), 10*time.Second)
	authn, err := auth.FromConfig(authCtx, cfg.AuthJWKSURL, cfg.AuthIssuer, cfg.AuthAudience)
	authCancel()
	if err != nil {
		slog.Error("auth init", "err", err)
		os.Exit(1)
	}

	store, err := openStore(cfg)
	if err != nil {
		slog.Error("store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	srv, err := server.New(cfg, store, authn)
	if err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Drain the CRM event outbox onto the bus (Track A3). Nothing in the
	// domain enqueues an event yet — Track B decides which transitions
	// publish — but the drain path runs from the start so wiring it in does
	// not become a second, separate change later. It stops on the same
	// signal that shuts the HTTP server down; events in flight are left
	// `sent` with an attempt counted and a backoff set, so the next process
	// picks them up.
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	pub := srv.Publisher()
	if pub.Configured() {
		if err := pub.EnableJetStream(); err != nil {
			slog.Warn("JetStream unavailable — events are published on core NATS and are not persisted",
				"err", err, "stream", platform.StreamName)
		} else {
			slog.Info("JetStream enabled", "stream", platform.StreamName, "duplicate_window", platform.DuplicateWindow)
		}
	}
	dispatch.Start(dispatchCtx, store, pub, 0, slog.Default())

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("crm-plug listening",
			"addr", cfg.Addr, "mode", cfg.Mode, "edition", cfg.Edition)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down")
	stopDispatch()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// openStore returns the Postgres store when DATABASE_URL is set, and the in-memory
// store otherwise. The in-memory store keeps nothing across a restart, so it is a
// development default, never a deployment one.
func openStore(cfg config.Config) (domain.Store, error) {
	if cfg.DatabaseURL == "" {
		slog.Warn("DATABASE_URL not set — using the in-memory store; data is lost on restart")
		return domain.NewMemStore(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if cfg.OutboxDrainURL == "" {
		slog.Warn("OUTBOX_DRAIN_DATABASE_URL not set — the outbox dispatcher shares the application connection; " +
			"under row-level security that drains nothing and looks healthy")
	}
	store, err := domain.NewPgStoreWithDrain(ctx, cfg.DatabaseURL, cfg.OutboxDrainURL)
	if err != nil {
		return nil, err
	}
	slog.Info("connected to postgres")
	return store, nil
}
