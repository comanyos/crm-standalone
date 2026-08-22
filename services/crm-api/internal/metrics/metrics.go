// Package metrics owns the CRM's low-cardinality Prometheus instrumentation.
// Tenant, user, record, request-id, and request-body values are deliberately
// never metric labels.
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
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crm_http_requests_total",
		Help: "HTTP and Connect RPC requests partitioned by method, safe route, and response status.",
	}, []string{"method", "route", "status"})
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "crm_http_request_duration_seconds",
		Help:    "HTTP and Connect RPC request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	OutboxProcessedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crm_outbox_processed_total",
		Help: "CRM transactional outbox delivery outcomes.",
	}, []string{"outcome"})
	JWKSValidationFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crm_jwks_validation_failures_total",
		Help: "JWT/JWKS validation failures partitioned by safe reason.",
	}, []string{"reason"})
	dbPoolAcquired = promauto.NewGauge(prometheus.GaugeOpts{Name: "crm_db_pool_acquired_connections", Help: "Current acquired CRM database pool connections."})
	dbPoolIdle     = promauto.NewGauge(prometheus.GaugeOpts{Name: "crm_db_pool_idle_connections", Help: "Current idle CRM database pool connections."})
	dbPoolTotal    = promauto.NewGauge(prometheus.GaugeOpts{Name: "crm_db_pool_total_connections", Help: "Current total CRM database pool connections."})
	dbPoolMax      = promauto.NewGauge(prometheus.GaugeOpts{Name: "crm_db_pool_max_connections", Help: "Configured maximum CRM database pool connections."})
	natsConnected  = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "crm_nats_connected", Help: "Whether a configured CRM NATS connection is currently connected."}, []string{"configured"})
)

// DBPoolStats is the supported, topology-free subset of pgx pool state.
type DBPoolStats struct{ Acquired, Idle, Total, Max int32 }

// SetDBPoolStats refreshes CRM pool gauges. It has no tenant dimension.
func SetDBPoolStats(s DBPoolStats) {
	dbPoolAcquired.Set(float64(s.Acquired))
	dbPoolIdle.Set(float64(s.Idle))
	dbPoolTotal.Set(float64(s.Total))
	dbPoolMax.Set(float64(s.Max))
}

// SetNATSStatus records an aggregate NATS connectivity state. The only label
// distinguishes an intentionally disabled local bus from an unavailable one.
func SetNATSStatus(configured, connected bool) {
	configuredLabel := "false"
	if configured {
		configuredLabel = "true"
	}
	value := 0.0
	if connected {
		value = 1
	}
	natsConnected.WithLabelValues(configuredLabel).Set(value)
}

// Handler exposes Prometheus text exposition. The caller may refresh pool
// gauges immediately before serving a scrape.
func Handler(refresh func()) http.Handler {
	h := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refresh != nil {
			refresh()
		}
		h.ServeHTTP(w, r)
	})
}

// Middleware instruments both raw HTTP and Connect RPC requests. route is a
// bounded operation name, never an arbitrary URL or tenant-owned identifier.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := safeRoute(r)
		HTTPRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

func safeRoute(r *http.Request) string {
	p := r.URL.Path
	if p == "/healthz" || p == "/readyz" || p == "/metrics" {
		return p
	}
	if len(p) > 1 && (hasPrefix(p, "/crm.v1.") || hasPrefix(p, "/platform.v1.")) {
		return p
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}
