package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	hostedui "github.com/authincore/authincore/hosted-ui"

	"github.com/authincore/authincore/internal/account/lockout"
	"github.com/authincore/authincore/internal/actions"
	grpcserver "github.com/authincore/authincore/internal/api/grpc"
	httpserver "github.com/authincore/authincore/internal/api/http"
	"github.com/authincore/authincore/internal/api/interceptor"
	"github.com/authincore/authincore/internal/api/middleware"
	"github.com/authincore/authincore/internal/authz"
	"github.com/authincore/authincore/internal/blueprint"
	"github.com/authincore/authincore/internal/command"
	appconfig "github.com/authincore/authincore/internal/config"
	"github.com/authincore/authincore/internal/courier"
	"github.com/authincore/authincore/internal/courier/sms"
	smtpsender "github.com/authincore/authincore/internal/courier/smtp"
	"github.com/authincore/authincore/internal/event"
	pgstore "github.com/authincore/authincore/internal/eventstore/postgres"
	"github.com/authincore/authincore/internal/fga"
	"github.com/authincore/authincore/internal/flow"
	magiclinkstrategy "github.com/authincore/authincore/internal/flow/strategy/magiclink"
	oidcstrategy "github.com/authincore/authincore/internal/flow/strategy/oidc"
	pwdstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	recovstrategy "github.com/authincore/authincore/internal/flow/strategy/recovery"
	settingsstrategy "github.com/authincore/authincore/internal/flow/strategy/settings"
	smsmfastrategy "github.com/authincore/authincore/internal/flow/strategy/smsmfa"
	totpstrategy "github.com/authincore/authincore/internal/flow/strategy/totp"
	verifstrategy "github.com/authincore/authincore/internal/flow/strategy/verification"
	passkeystrategy "github.com/authincore/authincore/internal/flow/strategy/webauthn"
	"github.com/authincore/authincore/internal/identity/schema"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/authincore/authincore/internal/metrics"
	"github.com/authincore/authincore/internal/platform"
	"github.com/authincore/authincore/internal/policy/engine"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/authincore/authincore/internal/protocol/saml"
	"github.com/authincore/authincore/internal/query"
	"github.com/authincore/authincore/internal/query/projection"
	"github.com/authincore/authincore/internal/scim"
	"github.com/authincore/authincore/internal/serviceaccount"
	"github.com/authincore/authincore/internal/storage/cache"
	"github.com/authincore/authincore/internal/storage/database"
	pgrepo "github.com/authincore/authincore/internal/storage/postgres"
	redisstore "github.com/authincore/authincore/internal/storage/redis"
	"github.com/authincore/authincore/internal/token"
	"github.com/authincore/authincore/internal/tracing"
	"github.com/authincore/authincore/internal/webhook"
	"github.com/authincore/authincore/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	appEnv := appconfig.EnvironmentFromEnv()
	if err := appconfig.ValidateProductionSafety(appEnv); err != nil {
		log.Error("startup configuration is unsafe", "environment", appEnv, "err", err)
		os.Exit(1)
	}
	log.Info("startup", "environment", appEnv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := tracing.Init(ctx, "authincore")
	if err != nil {
		log.Error("tracing init failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			log.Error("tracing shutdown failed", "err", err)
		}
	}()

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrations(ctx, log); err != nil {
			log.Error("migrations failed", "err", err)
			os.Exit(1)
		}
		log.Info("migrations applied")
		return
	}

	if err := run(ctx, log); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func runMigrations(ctx context.Context, log *slog.Logger) error {
	db, err := sql.Open("pgx", mustEnv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("open db for migrate: %w", err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	log.Info("running migrations")
	return goose.UpContext(ctx, db, ".")
}

func run(ctx context.Context, log *slog.Logger) error {
	// --- Database ---
	pool, err := database.Connect(ctx, database.Config{
		DSN:      mustEnv("DATABASE_URL"),
		MaxConns: 20,
		MinConns: 2,
	})
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	log.Info("database connected")

	// --- Cache (Redis) ---
	cacheClient, err := cache.New(cache.Config{
		Addr:     envOr("REDIS_ADDR", "localhost:6379"),
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	log.Info("cache connected")

	// --- Eventstore ---
	es := &eventstore{
		Pusher:  pgstore.NewPusher(pool),
		Querier: pgstore.NewQuerier(pool),
	}

	// --- Postgres repositories ---
	identityRepo := pgrepo.NewIdentityRepo(pool)
	sessionRepo := pgrepo.NewSessionRepo(pool)
	credStore := pgrepo.NewCredentialStore(pool)
	oidcClientStore := pgrepo.NewOIDCClientStore(pool)
	oidcTokenStore := pgrepo.NewOIDCTokenStore(pool)
	tenancyRepo := pgrepo.NewTenancyRepo(pool)
	recoveryStore := pgrepo.NewRecoveryStore(pool)
	courierQueue := pgrepo.NewCourierQueue(pool)
	verificationStore := pgrepo.NewVerificationStore(pool)
	totpStore := pgrepo.NewTOTPStore(pool)
	oidcProviderStore := pgrepo.NewOIDCProviderStore(pool)
	webauthnCredStore := pgrepo.NewWebAuthnCredentialStore(pool)
	webauthnSessionStore := pgrepo.NewWebAuthnSessionStore(pool)
	authzRoleStore := pgrepo.NewAuthzRoleStore(pool)
	authzPermStore := pgrepo.NewAuthzPermissionStore(pool)
	policyStore := pgrepo.NewPolicyStore(pool)
	reputationStore := pgrepo.NewReputationStore(pool)
	groupStore := pgrepo.NewGroupStore(pool)
	actionsStore := pgrepo.NewActionsStore(pool)
	webhookStore := pgrepo.NewWebhookStore(pool)
	adminRoleStore := pgrepo.NewAdminRoleStore(pool)
	adminAppStore := pgrepo.NewAdminApplicationStore(pool)
	blueprintStore := pgrepo.NewBlueprintStore(pool)
	magicLinkStore := pgrepo.NewMagicLinkStore(pool)
	courierTemplateStore := pgrepo.NewCourierTemplateStore(pool)
	consentStore := pgrepo.NewConsentStore(pool)
	samlConfigStore := pgrepo.NewSAMLConfigStore(pool)
	saServiceAccountStore := pgrepo.NewServiceAccountStore(pool)
	smsOTPStore := pgrepo.NewSMSOTPStore(pool)
	smsPhoneResolver := pgrepo.NewSMSPhoneResolver(pool)
	invitationStore := pgrepo.NewInvitationStore(pool)

	// --- CQRS ---
	queries := query.New(pool)
	schemaLoader := pgrepo.NewSchemaLoader(pool)
	schemaRegistry := schema.NewRegistry(schemaLoader)
	cmds := command.New(pool, es, identityRepo, sessionRepo).WithSchemaValidator(schemaRegistry)

	// --- Token service + rotating JWT signer ---
	issuer := envOr("ISSUER", "http://localhost:8080")
	rotatingSigner, err := token.NewRotatingSignerFromEnv()
	if err != nil {
		return fmt.Errorf("jwt signer: %w", err)
	}
	// Local rotation is development-only. Production replicas consume the same
	// active/previous key ring from SecretCore/KMS and rotate it externally.
	if os.Getenv("ENABLE_LOCAL_KEY_ROTATION") == "true" {
		rotationStop := make(chan struct{})
		rotatingSigner.StartRotationSchedule(24*time.Hour, rotationStop)
		defer close(rotationStop)
	}
	tokenSvc := token.NewService(rotatingSigner, issuer, 15*time.Minute, 30*24*time.Hour)

	// --- Account lockout (Redis-backed) ---
	lockoutStore := redisstore.NewLockoutStore(cacheClient)
	lockoutSvc := lockout.New(lockoutStore)

	// --- Courier (SMTP sender + Postgres queue) ---
	var courierSvc *courier.Courier
	if smtpHost := os.Getenv("SMTP_HOST"); smtpHost != "" {
		smtpPort := 587
		if os.Getenv("SMTP_TLS") == "true" {
			smtpPort = 465
		}
		sender := smtpsender.New(smtpsender.Config{
			Host:     smtpHost,
			Port:     smtpPort,
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     envOr("SMTP_FROM", "AuthinCore <no-reply@authincore.local>"),
			UseTLS:   os.Getenv("SMTP_TLS") == "true",
		})
		courierSvc = courier.New(courierQueue, sender, courierTemplateStore, log)
		log.Info("courier enabled", "smtp_host", smtpHost)
	} else {
		log.Warn("SMTP_HOST not set — courier disabled, recovery emails will be queued but not sent")
		courierSvc = courier.New(courierQueue, nil, courierTemplateStore, log)
	}

	// --- Flow engine ---
	flowRepo := flow.NewPGRepository(pool)
	hasher := pwdstrategy.NewArgon2idHasher()
	pwdStrategy := pwdstrategy.New(hasher, credStore, cmds, lockoutSvc)
	recovStrategy := recovstrategy.New(recoveryStore, credStore, hasher, courierSvc)
	verifStrategy := verifstrategy.New(verificationStore, verificationStore, courierSvc)
	settingsPwdStrategy := settingsstrategy.NewPasswordStrategy(hasher, credStore)
	settingsProfileStrategy := settingsstrategy.NewProfileStrategy(cmds)
	totpStrat := totpstrategy.New(totpStore, issuer).WithTracker(cacheClient)
	oidcStrat := oidcstrategy.New(oidcProviderStore, oidcProviderStore)

	waInstance, err := gowebauthn.New(&gowebauthn.Config{
		RPDisplayName: envOr("WEBAUTHN_RP_NAME", "AuthinCore"),
		RPID:          envOr("WEBAUTHN_RPID", "localhost"),
		RPOrigins:     []string{envOr("WEBAUTHN_ORIGIN", "http://localhost:8080")},
	})
	if err != nil {
		return fmt.Errorf("webauthn init: %w", err)
	}
	passkeyStrat := passkeystrategy.New(waInstance, webauthnCredStore, webauthnSessionStore)
	magicLinkStrat := magiclinkstrategy.New(magicLinkStore, identityRepo, courierSvc)

	// --- SMS MFA ---
	smsSender := resolveSMSSender(log)
	smsMFAStrat := smsmfastrategy.New(smsOTPStore, smsPhoneResolver, smsSender)

	flowMgr := flow.NewManager(flowRepo, pwdStrategy, recovStrategy, verifStrategy,
		settingsPwdStrategy, settingsProfileStrategy, totpStrat, oidcStrat, passkeyStrat,
		magicLinkStrat, smsMFAStrat)

	// --- Authz enforcer ---
	authzEnforcer := authz.NewEnforcer(authzRoleStore, authzPermStore)

	// Same two stores as the enforcer, so what a token claims and what a live
	// Check() decides come from one source rather than two that can disagree.
	claimsResolver := authz.NewClaimsResolver(authzRoleStore, authzPermStore)

	// --- Policy engine ---
	policyEngine := engine.New(policyStore, reputationStore, groupStore, log)

	// --- Actions runner ---
	actionsRunner := actions.NewRunner(actionsStore, log)

	// --- Webhook dispatcher ---
	webhookDispatcher := webhook.NewDispatcher(webhookStore, log)

	// --- Blueprint applier ---
	blueprintApplier := blueprint.NewApplier(blueprintStore, log, pgrepo.NewBlueprintReconcilers(pool)...)

	// --- Consent handler ---
	consentHandler := oidc.NewConsentHandler(consentStore)

	// --- SAML SP ---
	// Wire identity linker and session minter so SAML assertions complete login.
	samlLinker := saml.NewSAMLIdentityLinker(pool, cmds)
	samlMinter := saml.NewSAMLSessionMinter(cmds, tokenSvc).WithClaimsResolver(claimsResolver)
	samlSP := saml.New(samlConfigStore, samlLinker, samlMinter, issuer)
	samlIdPKeyStore := pgrepo.NewSAMLIdPKeyStore(pool)
	samlIdP := saml.NewIdP(
		pgrepo.NewSAMLSPConfigStore(pool),
		pgrepo.NewIdentityTraitsReader(pool),
		samlIdPKeyStore,
		issuer,
	)
	scimHandler := scim.NewHandler(
		pgrepo.NewSCIMUserStore(pool),
		pgrepo.NewSCIMGroupStore(pool),
		pgrepo.NewSCIMTokenValidator(pool),
		issuer+"/scim/v2",
	)

	// --- Fine-grained authorization ---
	fgaSvc := fga.NewService(pgrepo.NewFGAStore(pool), fga.DefaultModel())
	fgaHandler := fga.NewHandler(fgaSvc)

	// --- Service accounts ---
	saSvc := serviceaccount.New(saServiceAccountStore, rotatingSigner, issuer, 60*time.Minute)

	// --- Invitations ---
	invitationSvc := invitation.New(invitationStore, pgrepo.NewInvitationRoleAssigner(pool), identityRepo, courierSvc, issuer)

	// --- gRPC server (connect-go) ---
	identityGRPC := grpcserver.NewIdentityServer(cmds, queries, log)
	sessionGRPC := grpcserver.NewSessionServer(queries, tokenSvc, log)
	flowGRPC := grpcserver.NewFlowServer(flowMgr, log)
	grpcMux := grpcserver.Handler(identityGRPC, sessionGRPC, flowGRPC, nil)
	startGRPC(ctx, grpcMux, envOr("GRPC_PORT", "8081"), log)

	// --- OIDC Provider ---
	oidcProvider := oidc.NewProvider(
		oidc.Config{
			Issuer:          issuer,
			AccessTokenTTL:  15 * time.Minute,
			RefreshTokenTTL: 30 * 24 * time.Hour,
			IDTokenTTL:      15 * time.Minute,
		},
		oidcClientStore,
		oidcTokenStore,
		tokenSvc,
		rotatingSigner,
	).WithConsent(consentStore).WithSessionReader(sessionRepo).WithClaimsResolver(claimsResolver)

	// --- HTTP server ---
	authnInterceptor := interceptor.NewAuthnInterceptor(tokenSvc)
	trustedProxies, err := middleware.ParseTrustedProxyCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return fmt.Errorf("trusted proxies: %w", err)
	}
	consentHandler = consentHandler.WithAuthn(authnInterceptor)
	hostedUIFS, err := hostedui.FS()
	if err != nil {
		log.Warn("hosted login UI unavailable; serving disabled", "err", err)
	}
	// --- Platform contract adapter ---
	// Makes AuthinCore a first-class suite module: shell sidebar, health-map, Copilot.
	platformAdapter := platform.NewAdapter(issuer, pool, cacheClient).WithServices(cmds, queries, invitationSvc)

	httpDeps := httpserver.Deps{
		OIDC:            oidcProvider,
		Resolver:        tenancyRepo,
		Authn:           authnInterceptor,
		Commands:        cmds,
		Queries:         queries,
		Flows:           flowMgr,
		TokenSvc:        tokenSvc,
		RateLimiter:     cacheClient,
		TrustedProxies:  trustedProxies,
		DBPinger:        pool,
		CachePinger:     cacheClient,
		MetricsPool:     pool,
		ConsentHandler:  consentHandler,
		SAMLSP:          samlSP,
		SAMLIdP:         samlIdP,
		SCIMHandler:     scimHandler,
		FGAHandler:      fgaHandler,
		FGAService:      fgaSvc,
		ServiceAccSvc:   saSvc,
		InvitationSvc:   invitationSvc,
		AuthzEnforcer:   authzEnforcer,
		ClaimsResolver:  claimsResolver,
		PolicyEngine:    policyEngine,
		ActionsRunner:   actionsRunner,
		BlueprintSvc:    blueprintApplier,
		OIDCProviders:   oidcProviderStore,
		WebhookStore:    webhookStore,
		RoleManager:     adminRoleStore,
		AppManager:      adminAppStore,
		AdminToken:      os.Getenv("ADMIN_TOKEN"),
		HostedUI:        hostedUIFS,
		CORSOrigins:     splitList(os.Getenv("CORS_ALLOWED_ORIGINS")),
		PlatformAdapter: platformAdapter,
		Log:             log,
	}
	// Enterprise builds wire optional stores (SSO enforcement, branding); OSS no-op.
	applyEnterpriseDeps(&httpDeps, pool)
	srv := httpserver.NewServer(
		httpserver.Config{Addr: ":" + envOr("PORT", "8080")},
		httpDeps,
	)

	// --- Event projection tail (identity read model) ---
	identityProjection := projection.NewIdentityProjection(pool, log)
	eventTail := event.NewTail(es, pool, "identity_projection", 500*time.Millisecond, log, identityProjection)
	go eventTail.Run(ctx)

	// --- Webhook event tail (fanout to outbound webhooks) ---
	webhookProjection := webhook.NewProjection(webhookDispatcher)
	webhookTail := event.NewTail(es, pool, "webhook_projection", 500*time.Millisecond, log, webhookProjection)
	go webhookTail.Run(ctx)
	startEnterpriseWorkers(ctx, es, pool, log)

	// --- Courier dispatch worker ---
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				courierSvc.Dispatch(ctx)
			}
		}
	}()

	log.Info("authincore starting", "addr", ":"+envOr("PORT", "8080"), "issuer", issuer)
	return srv.Start(ctx)
}

// eventstore wraps the postgres Pusher and Querier.
type eventstore struct {
	*pgstore.Pusher
	*pgstore.Querier
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "fatal: environment variable %s is required\n", key)
		os.Exit(1)
	}
	return v
}

// splitList parses a comma-separated env var into trimmed, non-empty entries.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveSMSSender picks the first configured SMS backend (Twilio > Vonage > nil).
func resolveSMSSender(log *slog.Logger) sms.Sender {
	if tw, ok := sms.NewTwilioFromEnv(); ok {
		log.Info("SMS: Twilio enabled")
		return tw
	}
	if vn, ok := sms.NewVonageFromEnv(); ok {
		log.Info("SMS: Vonage enabled")
		return vn
	}
	log.Warn("SMS_PROVIDER not configured — SMS MFA will queue codes but not send")
	return nil
}

// startGRPC runs the connect-go mux on its own port in background goroutines.
// It shuts down cleanly when ctx is cancelled.
func startGRPC(ctx context.Context, h http.Handler, port string, log *slog.Logger) {
	// The Connect listener is a separate ingress, so it needs the same safe
	// correlation, access logging, and low-cardinality HTTP metrics as port 8080.
	h = middleware.Chain(middleware.RequestID, tracing.HTTPMiddleware, metrics.Middleware, middleware.RequestLog(log))(h)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Info("gRPC/connect listening", "addr", ":"+port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("gRPC server exited", "err", err)
		}
	}()
	go func() { // #nosec G118 -- shutdown must use a fresh context after the parent is cancelled.
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Error("gRPC server shutdown", "err", err)
		}
	}()
}
