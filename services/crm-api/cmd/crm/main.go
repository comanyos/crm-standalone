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
	"github.com/santoshguru/crm/internal/engagement"
	"github.com/santoshguru/crm/internal/integration/calendar"
	"github.com/santoshguru/crm/internal/integration/gmail"
	"github.com/santoshguru/crm/internal/integration/workflow"
	"github.com/santoshguru/crm/internal/platform"
	"github.com/santoshguru/crm/internal/server"
	"github.com/santoshguru/crm/internal/tracing"
)

func main() {
	os.Exit(execute())
}

func execute() int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	shutdownTracing, err := tracing.Init(context.Background(), "crm-api")
	if err != nil {
		slog.Error("tracing init failed")
		return 1
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			slog.Error("tracing shutdown failed", "err", err)
		}
	}()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "err", err)
		return 1
	}

	authCtx, authCancel := context.WithTimeout(context.Background(), 10*time.Second)
	authn, err := auth.NewPostgres(authCtx, auth.Config{
		DatabaseURL: cfg.DatabaseURL,
		OTPSecret:   cfg.OTPPepper, OTPTTL: cfg.AuthOTPTTL,
		SessionTTL: cfg.AuthSessionTTL, CookieName: cfg.AuthCookieName,
		CookieSecure: cfg.AuthCookieSecure, DevOTP: cfg.AuthDevOTP,
		SMTP: auth.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword, From: cfg.SMTPFrom, UseTLS: cfg.SMTPTLS,
			RequireSTARTTLS: cfg.SMTPRequireSTARTTLS,
		},
		GoogleClientID: cfg.AuthGoogleClientID, GoogleClientSecret: cfg.AuthGoogleClientSecret,
		GoogleRedirectURL: cfg.AuthGoogleRedirectURL, LoginSuccessURL: cfg.AuthLoginSuccessURL,
		JoinResultURL:  cfg.AuthJoinResultURL,
		BootstrapEmail: cfg.AuthBootstrapEmail, BootstrapOrgName: cfg.AuthBootstrapOrgName,
		BootstrapOrgID: cfg.AuthBootstrapOrgID,
	})
	authCancel()
	if err != nil {
		slog.Error("auth init", "err", err)
		return 1
	}

	store, err := openStore(cfg)
	if err != nil {
		authn.Close()
		slog.Error("store", "err", err)
		return 1
	}
	defer func() { _ = store.Close() }()

	srv, err := server.New(cfg, store, authn)
	if err != nil {
		authn.Close()
		slog.Error("server", "err", err)
		return 1
	}
	defer srv.Close()

	// Drain the CRM event outbox onto the bus. Only opportunity.closed_won
	// is enqueued today (internal/domain/opportunity_events.go), but the
	// drain path runs regardless so extending event coverage stays a
	// domain-side change. It stops on the same
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

	// Gmail synchronisation. It shares the dispatcher's lifetime and shutdown
	// signal: both are background loops that must stop when the HTTP server
	// does, and neither may keep the process alive. A pass interrupted by
	// shutdown leaves its cursor where it was and its lease to expire, so the
	// next process resumes rather than repeats.
	gmail.Start(dispatchCtx, store, srv.GmailSyncer(), 0, slog.Default())

	// Calendar synchronisation runs as its own loop rather than sharing
	// Gmail's pass. It costs a goroutine and buys independent failure: a
	// Gmail quota problem must not stop meetings reaching customer timelines,
	// and a calendar whose sync token expired must not delay anybody's mail.
	calendar.Start(dispatchCtx, store, srv.CalendarSyncer(), 0, slog.Default())

	// Workflow automation. Unlike the two sync loops this has no
	// configuration to be missing: automation is part of the CRM rather than
	// an integration, so it always runs, and a deployment with no active
	// rules simply finds no due tenants. It shares the same shutdown signal —
	// a pass interrupted mid-way leaves its cursor where it was and its lease
	// to expire, and the idempotency key on each execution is what makes
	// resuming safe.
	workflow.Start(dispatchCtx, store, 0, slog.Default())

	// Speed-to-lead notifications and their email queue are durable database
	// work just like workflows. Claims and leases make restarts and multiple
	// replicas safe.
	engagement.Start(dispatchCtx, store, authn, cfg.LeadSLA, cfg.WebAppBaseURL, 0, slog.Default())

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("crm-plug listening",
			"addr", cfg.Addr, "mode", cfg.Mode, "edition", cfg.Edition)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	exitCode := 0
	select {
	case <-stop:
	case err := <-serverErr:
		slog.Error("server error", "err", err)
		exitCode = 1
	}
	signal.Stop(stop)

	slog.Info("shutting down")
	stopDispatch()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Error("HTTP shutdown failed", "err", err)
		exitCode = 1
	}
	return exitCode
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
