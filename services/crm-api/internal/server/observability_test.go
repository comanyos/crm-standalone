package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

func newObservabilityServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(config.Config{}, domain.NewMemStore(), auth.Disabled())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return s
}

func TestRequestIDPreservesSafeInboundAndReplacesUnsafeValue(t *testing.T) {
	h := newObservabilityServer(t).Handler()
	for _, tc := range []struct {
		name, inbound string
		wantSame      bool
	}{
		{"preserves safe", "trace-12345678", true},
		{"replaces unsafe", "Bearer secret-token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set("X-Request-ID", tc.inbound)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-ID")
			if got == "" {
				t.Fatal("missing response request id")
			}
			if tc.wantSame && got != tc.inbound {
				t.Fatalf("request id=%q, want %q", got, tc.inbound)
			}
			if !tc.wantSame && got == tc.inbound {
				t.Fatalf("unsafe request id was preserved")
			}
		})
	}
}

func TestRequestLogDoesNotExposeAuthenticationSecrets(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	h := newObservabilityServer(t).Handler()
	req := httptest.NewRequest(http.MethodPost, "/crm.v1.CRMService/ListAccounts?email=person@example.test", nil)
	req.Header.Set("Authorization", "Bearer TOP-SECRET-TOKEN")
	req.Header.Set("Cookie", "session=COOKIE-SECRET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	line := buf.String()
	for _, secret := range []string{"TOP-SECRET-TOKEN", "COOKIE-SECRET", "person@example.test"} {
		if strings.Contains(line, secret) {
			t.Fatalf("secret leaked in log: %s", line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("invalid JSON log: %v", err)
	}
	if record["route"] != "/crm.v1.CRMService/ListAccounts" {
		t.Fatalf("route=%v", record["route"])
	}
}

func TestMetricsEndpointAndReadiness(t *testing.T) {
	s := newObservabilityServer(t)
	h := s.Handler()
	for _, path := range []string{"/healthz", "/crm.v1.CRMService/ListAccounts"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	metrics := httptest.NewRecorder()
	h.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "crm_http_requests_total") || !strings.Contains(metrics.Body.String(), "crm_nats_connected") {
		t.Fatalf("metrics status=%d body=%s", metrics.Code, metrics.Body.String())
	}
	if strings.Contains(metrics.Body.String(), "TOP-SECRET-TOKEN") || strings.Contains(metrics.Body.String(), "person@example.test") {
		t.Fatalf("metrics leaked request data: %s", metrics.Body.String())
	}

	ready := httptest.NewRecorder()
	h.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status=%d", ready.Code)
	}

	s.readiness = func(context.Context) error { return errors.New("postgres://user:secret@private-host") }
	failed := httptest.NewRecorder()
	h.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed ready status=%d", failed.Code)
	}
	if strings.Contains(failed.Body.String(), "secret") || strings.Contains(failed.Body.String(), "private-host") {
		t.Fatalf("readiness leaked dependency details: %s", failed.Body.String())
	}
}

func TestMetricsTokenProtectsConfiguredScrapeEndpoint(t *testing.T) {
	s, err := New(config.Config{MetricsToken: "a-sufficiently-long-metrics-token"}, domain.NewMemStore(), auth.Disabled())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h := s.Handler()
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong", token: "not-the-token", want: http.StatusUnauthorized},
		{name: "correct", token: s.cfg.MetricsToken, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want %d", rec.Code, tc.want)
			}
			if strings.Contains(rec.Body.String(), tc.token) && tc.token != "" {
				t.Fatalf("metrics response leaked token: %s", rec.Body.String())
			}
		})
	}
}
