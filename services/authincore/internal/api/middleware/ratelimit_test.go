package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type failingRateLimiter struct{}

func (failingRateLimiter) IncrFixedWindow(context.Context, string, time.Duration) (int64, error) {
	return 0, errors.New("redis unavailable")
}

func (failingRateLimiter) Exists(context.Context, string) (bool, error) {
	return false, errors.New("redis unavailable")
}

func TestRateLimitFailClosed(t *testing.T) {
	called := false
	handler := RateLimit(failingRateLimiter{}, RateLimitConfig{
		MaxRequests: 5,
		Window:      time.Minute,
		KeyFunc:     IPKeyFunc("login"),
		FailClosed:  true,
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if called {
		t.Fatal("protected handler ran while limiter was unavailable")
	}
}

func TestForwardedIPRequiresTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "203.0.113.20:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	if got := realIP(req, nil); got != "203.0.113.20" {
		t.Fatalf("untrusted forwarded IP accepted: got %s", got)
	}

	trusted, err := ParseTrustedProxyCIDRs("203.0.113.0/24")
	if err != nil {
		t.Fatalf("parse trusted proxies: %v", err)
	}
	if got := realIP(req, trusted); got != "198.51.100.10" {
		t.Fatalf("trusted forwarded IP ignored: got %s", got)
	}
}

func TestParseTrustedProxyCIDRsRejectsInvalidInput(t *testing.T) {
	if _, err := ParseTrustedProxyCIDRs("not-a-cidr"); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}
