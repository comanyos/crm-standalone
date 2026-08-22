package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	DBQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "crm_db_query_duration_seconds",
		Help:    "CRM PostgreSQL query latency partitioned by SQL operation and result.",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "result"})
	DBQueriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crm_db_queries_total",
		Help: "CRM PostgreSQL query count partitioned by SQL operation and result.",
	}, []string{"operation", "result"})
)

type queryStartKey struct{}
type queryStart struct {
	operation string
	at        time.Time
}

// QueryTracer observes pgx queries without exporting SQL, arguments, tenant
// IDs, or connection strings. It is safe for both application and drain pools.
type QueryTracer struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, queryStart{operation: sqlOperation(data.SQL), at: time.Now()})
}

func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	result := "success"
	if data.Err != nil {
		result = "error"
	}
	DBQueriesTotal.WithLabelValues(start.operation, result).Inc()
	DBQueryDuration.WithLabelValues(start.operation, result).Observe(time.Since(start.at).Seconds())
}

func sqlOperation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "other"
	}
	switch op := strings.ToLower(fields[0]); op {
	case "select", "insert", "update", "delete", "begin", "commit", "rollback", "with":
		return op
	default:
		return "other"
	}
}
