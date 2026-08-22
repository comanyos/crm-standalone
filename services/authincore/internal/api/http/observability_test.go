package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow"
	"github.com/authincore/authincore/internal/metrics"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
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

// --- flow-method metric label bounding ---

// stubStrategy is a registered flow strategy that does nothing. Only its ID
// matters here: it is what makes one method name "known".
type stubStrategy struct{ id string }

func (s stubStrategy) ID() string { return s.id }

func (stubStrategy) PopulateFlow(_ context.Context, _ *domflow.Flow) error { return nil }

func (stubStrategy) Handle(_ context.Context, _ *domflow.Flow, _ json.RawMessage) (*uuid.UUID, error) {
	return nil, nil
}

func TestSafeFlowMethodBoundsUntrustedMetricLabel(t *testing.T) {
	s := &Server{deps: Deps{Flows: flow.NewManager(nil, stubStrategy{id: "password"})}}

	for _, tc := range []struct{ name, submitted, want string }{
		{"registered method survives", "password", "password"},
		{"unregistered method collapses", "totp", "unknown"},
		{"attacker string collapses", "a8f3e1c2-injected", "unknown"},
		{"empty method collapses", "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.safeFlowMethod(tc.submitted); got != tc.want {
				t.Fatalf("safeFlowMethod(%q)=%q, want %q", tc.submitted, got, tc.want)
			}
		})
	}
}

// A caller-chosen method must not be able to grow the exporter's series set.
// Every distinct label value is a time series held for the process's life, so
// an unbounded label here is a memory-exhaustion vector reachable one request
// at a time — rate limiting bounds the rate, not the total.
func TestUnregisteredFlowMethodsCreateNoNewSeries(t *testing.T) {
	s := &Server{deps: Deps{Flows: flow.NewManager(nil, stubStrategy{id: "password"})}}

	metrics.LoginTotal.WithLabelValues(s.safeFlowMethod("password"), "failure").Inc()
	before := loginSeriesCount(t)

	for i := range 100 {
		hostile := fmt.Sprintf("method-%d-%s", i, strings.Repeat("x", i))
		metrics.LoginTotal.WithLabelValues(s.safeFlowMethod(hostile), "failure").Inc()
	}

	// The 100 distinct hostile values share the single "unknown" series, so at
	// most one series was added.
	if after := loginSeriesCount(t); after > before+1 {
		t.Fatalf("100 unregistered methods created %d new series (before=%d, after=%d); "+
			"the label is not bounded", after-before, before, after)
	}
}

// loginSeriesCount returns how many label combinations authincore_login_total
// currently holds, read from the registry the exporter actually serves.
func loginSeriesCount(t *testing.T) int {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "authincore_login_total" {
			return len(f.GetMetric())
		}
	}
	t.Fatal("authincore_login_total is not registered")
	return 0
}
