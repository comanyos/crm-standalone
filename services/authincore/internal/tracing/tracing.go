// Package tracing initialises the OpenTelemetry SDK and exposes a span helper.
//
// Initialise once at startup with Init; if OTEL_EXPORTER_OTLP_ENDPOINT is not
// set the SDK runs with a no-op tracer so no code changes are needed in
// development.
package tracing

import (
	"context"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "authincore"

// Init configures the global OTel tracer.
// If OTEL_EXPORTER_OTLP_ENDPOINT is unset it registers a no-op provider so
// callers don't need to guard every Span call.
// Returns a shutdown function that must be called before process exit.
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// No exporter configured — use no-op so instrumentation compiles but is free.
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://"))}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" || strings.HasPrefix(endpoint, "http://") {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	exp, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
		resource.WithSchemaURL(semconv.SchemaURL),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio()))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns the named tracer for a package.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Start is a convenience wrapper around Tracer().Start.
func Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, spanName, opts...)
}

// samplingRatio reads OTEL_SAMPLING_RATIO env (0.0–1.0, default 1.0).
func samplingRatio() float64 {
	if v := os.Getenv("OTEL_SAMPLING_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 1.0
}
