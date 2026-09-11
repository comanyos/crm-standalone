package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestHTTPMiddlewareContinuesInboundTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	const traceID = "0af7651916cd43dd8448eb211c80319c"
	var got string
	h := HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.SpanContextFromContext(r.Context()).TraceID().String()
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-b7ad6b7169203331-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != traceID {
		t.Fatalf("trace ID = %q, want %q", got, traceID)
	}
}

func TestTraceParentRoundTripsAcrossDurableContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	const traceID = "0af7651916cd43dd8448eb211c80319c"
	ctx := ContextWithTraceParent(context.Background(), "00-"+traceID+"-b7ad6b7169203331-01")
	ctx, span := StartOutboxPublish(ctx)
	defer span.End()
	if got := TraceParent(ctx); got[3:35] != traceID {
		t.Fatalf("outbox trace ID = %q, want %q", got[3:35], traceID)
	}
}
