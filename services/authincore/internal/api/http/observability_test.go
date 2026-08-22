package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingPinger struct{}

func (failingPinger) Ping(_ context.Context) error {
	return errors.New("postgres://user:secret@private-host")
}

func TestReadyzDoesNotExposeDependencyDetails(t *testing.T) {
	s := &Server{deps: Deps{DBPinger: failingPinger{}}}
	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rec.Code)
	}
	for _, secret := range []string{"secret", "private-host", "postgres://"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("readiness leaked %q: %s", secret, rec.Body.String())
		}
	}
}
