package obs

import (
	"context"
	"errors"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var errTraceExport = errors.New("trace export failed")

// Without an explicit export destination, keep correlation local. All exporter
// connection, authentication, TLS, compression and timeout settings are read by
// the official exporters from OTEL_EXPORTER_OTLP[_TRACES]_*.
func traceExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	selection := strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER"))
	if selection == "" {
		selection = "none"
		if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
			selection = "otlp"
		}
	}
	if selection == "none" {
		return nil, nil
	}
	if selection != "otlp" {
		return nil, errors.New("OTEL_TRACES_EXPORTER must be otlp or none")
	}
	protocol := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"))
	if protocol == "" {
		protocol = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	}
	var exporter sdktrace.SpanExporter
	var err error
	switch protocol {
	case "", "http/protobuf", "http/json":
		exporter, err = otlptracehttp.New(ctx)
	case "grpc":
		exporter, err = otlptracegrpc.New(ctx)
	default:
		return nil, errors.New("unsupported OTLP trace protocol")
	}
	if err != nil {
		// Exporter errors may contain endpoint credentials or TLS file paths.
		return nil, errors.New("initialize OTLP trace exporter failed")
	}
	return privateExporter{exporter}, nil
}

type privateExporter struct{ sdktrace.SpanExporter }

func (e privateExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	clean := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		clean[i] = privateSpan{span}
	}
	if err := e.SpanExporter.ExportSpans(ctx, clean); err != nil {
		return errTraceExport
	}
	return nil
}

func (e privateExporter) Shutdown(ctx context.Context) error {
	if err := e.SpanExporter.Shutdown(ctx); err != nil {
		return errTraceExport
	}
	return nil
}

// Keep only bounded HTTP facts. In particular, SDK-generated URL attributes,
// exception events and status descriptions can carry request secrets even when
// instrumentation has not explicitly opted in to capturing headers or bodies.
type privateSpan struct{ sdktrace.ReadOnlySpan }

func (s privateSpan) Name() string {
	if s.SpanKind() == trace.SpanKindClient {
		return "ollame upstream"
	}
	return "ollame request"
}
func (s privateSpan) Attributes() []attribute.KeyValue {
	var clean []attribute.KeyValue
	for _, attr := range s.ReadOnlySpan.Attributes() {
		switch string(attr.Key) {
		case "http.request.method", "http.method":
			switch attr.Value.AsString() {
			case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH", "_OTHER":
				clean = append(clean, attr)
			}
		case "http.response.status_code", "http.status_code", "http.request.body.size", "http.response.body.size", "server.port", "network.peer.port":
			if attr.Value.Type() == attribute.INT64 {
				clean = append(clean, attr)
			}
		}
	}
	return clean
}
func (s privateSpan) Status() sdktrace.Status {
	return sdktrace.Status{Code: s.ReadOnlySpan.Status().Code}
}
func (s privateSpan) Events() []sdktrace.Event { return nil }
func (s privateSpan) Links() []sdktrace.Link   { return nil }
