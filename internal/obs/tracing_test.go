package obs

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceID(t *testing.T) {
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("missing context produced trace ID %q", got)
	}
	id, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: spanID}))
	if got := TraceID(ctx); got != id.String() {
		t.Fatalf("unsampled remote context lost trace ID: %q", got)
	}
}

func TestTraceExportPrivacy(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	defer provider.Shutdown(context.Background())
	_, span := provider.Tracer("privacy").Start(context.Background(), "secret path", trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(attribute.String("url.full", "https://user:password@example.test/path?key=secret"), attribute.String("http.request.header.authorization", "secret"), attribute.String("http.request.method", "POST"), attribute.Int("http.response.status_code", 502))
	span.SetStatus(codes.Error, "error containing secret")
	span.RecordError(errors.New("secret response body"))
	span.End()
	readOnly, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("SDK span does not expose snapshot")
	}
	clean := privateSpan{readOnly}
	if clean.Name() != "ollame upstream" || clean.Status().Description != "" || len(clean.Events()) != 0 || len(clean.Links()) != 0 {
		t.Fatal("free-form trace data retained")
	}
	attrs := clean.Attributes()
	if len(attrs) != 2 || attrs[0].Value.AsString() != "POST" || attrs[1].Value.AsInt64() != 502 {
		t.Fatalf("unexpected exported attributes: %v", attrs)
	}
	if !clean.SpanContext().Equal(readOnly.SpanContext()) || clean.StartTime() != readOnly.StartTime() || clean.EndTime() != readOnly.EndTime() || clean.Status().Code != codes.Error {
		t.Fatal("privacy filtering changed correlation, timing or error status")
	}
}

func TestTraceExporterConfiguration(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"} {
		t.Setenv(key, "")
	}
	if exporter, err := traceExporter(context.Background()); err != nil || exporter != nil {
		t.Fatalf("default local tracing: %v %v", exporter, err)
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "unsupported-secret")
	if _, err := traceExporter(context.Background()); err == nil || strings.Contains(err.Error(), "unsupported-secret") {
		t.Fatal("invalid exporter must fail without echoing value")
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "unsupported-secret")
	if _, err := traceExporter(context.Background()); err == nil || strings.Contains(err.Error(), "unsupported-secret") {
		t.Fatal("invalid protocol must fail without echoing value")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	tracing, err := NewTracing("test")
	if err != nil {
		t.Fatal(err)
	}
	defer tracing.Close(context.Background())
	if !tracing.disabled {
		t.Fatal("SDK disable ignored")
	}
}
