package platform

import (
	"context"
	"testing"

	"github.com/santoshguru/crm/internal/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestPublishMessageCarriesOnlyTraceContextAndDeduplicationID(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	ctx := tracing.ContextWithTraceParent(context.Background(), "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	msg := newPublishMessage(ctx, "crm.opportunity.tenant-a", []byte(`{"email":"person@example.test"}`), "event-1")
	if got := msg.Header.Get("traceparent"); got != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Fatalf("traceparent = %q", got)
	}
	if got := msg.Header.Get("Nats-Msg-Id"); got != "event-1" {
		t.Fatalf("Nats-Msg-Id = %q, want event-1", got)
	}
	if len(msg.Header) != 2 {
		t.Fatalf("headers = %v; only trace context and deduplication ID are allowed", msg.Header)
	}
}
