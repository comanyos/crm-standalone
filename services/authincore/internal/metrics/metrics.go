// Package metrics defines all Prometheus metrics for AuthinCore.
// All metrics are registered on the default registry so promhttp.Handler()
// exposes them at GET /metrics without extra wiring.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTP

	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_http_requests_total",
		Help: "Total HTTP requests partitioned by method, path pattern, and status code.",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "authincore_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// Auth flows

	LoginTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_login_total",
		Help: "Login attempts partitioned by method (password, totp, passkey, …) and outcome (success, failure).",
	}, []string{"method", "outcome"})

	RegistrationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_registration_total",
		Help: "Registration attempts partitioned by outcome.",
	}, []string{"outcome"})

	// Tokens

	TokensIssued = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_tokens_issued_total",
		Help: "Access tokens minted, partitioned by grant_type.",
	}, []string{"grant_type"})

	RefreshTokenRotations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "authincore_refresh_token_rotations_total",
		Help: "Successful refresh token rotations (single-use chain).",
	})

	// Sessions

	ActiveSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "authincore_active_sessions",
		Help: "Current number of active sessions (approximate, updated on create/revoke).",
	})

	// Webhooks

	WebhookDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_webhook_deliveries_total",
		Help: "Webhook delivery attempts partitioned by outcome (delivered, failed).",
	}, []string{"outcome"})

	// Policy engine

	PolicyEvalTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_policy_eval_total",
		Help: "Policy evaluations at login partitioned by result (pass, deny, error).",
	}, []string{"result"})

	// FGA

	FGACheckTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "authincore_fga_check_total",
		Help: "Fine-grained authorization checks partitioned by result (allowed, denied).",
	}, []string{"result"})

	FGACheckDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "authincore_fga_check_duration_seconds",
		Help:    "Latency of FGA check operations.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
	})
)

// Handler returns the Prometheus metrics HTTP handler for GET /metrics.
func Handler() http.Handler {
	return promhttp.Handler()
}

// statusRecorder wraps ResponseWriter to capture the status code written.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap preserves optional ResponseWriter capabilities through
// http.ResponseController (flush, hijack, deadlines) for streaming handlers.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Middleware instruments every HTTP request with request count and latency metrics.
// path should be the registered route pattern (e.g. "POST /self-service/login")
// so high-cardinality IDs in the URL are never used as label values.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start).Seconds()
		// Use the matched pattern as the path label to avoid high cardinality.
		pattern := r.Pattern
		if pattern == "" {
			pattern = r.URL.Path
		}
		HTTPRequestsTotal.WithLabelValues(r.Method, pattern, strconv.Itoa(rec.status)).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, pattern).Observe(dur)
	})
}
