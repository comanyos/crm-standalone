// Package main is the entry point for the authincore outpost binary.
// The outpost is a lightweight edge proxy (forward-auth for nginx/traefik/caddy)
// and optional LDAP server — inspired by authentik's outpost architecture.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	coreURL := envOr("AUTHINCORE_URL", "http://localhost:8080")
	token, err := secretEnvOrFile("OUTPOST_TOKEN", "OUTPOST_TOKEN_FILE")
	if err != nil || token == "" {
		log.Error("outpost token is required", "err", err)
		os.Exit(1)
	}
	addr := ":" + envOr("OUTPOST_PORT", "9090")

	log.Info("outpost starting", "addr", addr, "core_url", coreURL)

	mux := http.NewServeMux()

	// forward-auth endpoint: nginx/traefik call this before forwarding requests.
	mux.HandleFunc("/forward-auth", forwardAuthHandler(coreURL, token, log))

	// outpost health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("outpost server", "err", err)
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*1e9)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	log.Info("outpost stopped")
}

func secretEnvOrFile(envKey, fileKey string) (string, error) {
	if value := os.Getenv(envKey); value != "" {
		return value, nil
	}
	if path := os.Getenv(fileKey); path != "" {
		// #nosec G304,G703 -- the path is trusted process configuration supplied by the operator/Kubernetes secret mount.
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}

func forwardAuthHandler(coreURL, outpostToken string, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Validate the session cookie/bearer by calling the core's WhoAmI endpoint.
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, coreURL+"/api/v1/sessions/me", nil)
		if err != nil {
			http.Error(w, "forward-auth internal error", http.StatusInternalServerError)
			return
		}
		// Forward the original cookies + auth header.
		req.Header.Set("Cookie", r.Header.Get("Cookie"))
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		req.Header.Set("X-Outpost-Token", outpostToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Info("forward-auth deny", "remote", r.RemoteAddr, "path", r.URL.Path)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Pass: downstream nginx/traefik will forward the original request.
		w.WriteHeader(http.StatusOK)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
