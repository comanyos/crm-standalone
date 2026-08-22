// Package tracing configures optional OpenTelemetry tracing for the CRM.
package tracing

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "crm-api"

// Init installs W3C Trace Context propagation and optional OTLP export. An
// unset OTEL_EXPORTER_OTLP_ENDPOINT intentionally leaves tracing no-op while
// retaining propagation, which makes local development inexpensive.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://"))}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" || strings.HasPrefix(endpoint, "http://") {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)), resource.WithSchemaURL(semconv.SchemaURL))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio()))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func samplingRatio() float64 {
	if raw := os.Getenv("OTEL_SAMPLING_RATIO"); raw != "" {
		if ratio, err := strconv.ParseFloat(raw, 64); err == nil && ratio >= 0 && ratio <= 1 {
			return ratio
		}
	}
	return 1
}

// HTTPMiddleware extracts a W3C traceparent at ingress and creates a server
// span. It does not put paths, headers, bodies, tenants, or PII into spans.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer(tracerName).Start(ctx, "http.server", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceParent returns the W3C traceparent for ctx. It is intended for durable
// hand-offs such as the CRM outbox: the worker can then continue the request's
// trace after the original HTTP request has ended. It returns an empty string
// when tracing is not active rather than manufacturing a correlation value.
//
// A traceparent contains opaque trace/span IDs only; it is not logged, exposed
// as a metric label, or populated from an untrusted request body.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ContextWithTraceParent extracts a traceparent stored by a durable hand-off.
// Invalid or absent values deliberately have no effect, yielding an ordinary
// local worker context instead of failing delivery.
func ContextWithTraceParent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// StartOutboxPublish starts the producer span used by the dispatcher. The
// caller supplies the context restored from the durable envelope.
func StartOutboxPublish(ctx context.Context) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer))
}
