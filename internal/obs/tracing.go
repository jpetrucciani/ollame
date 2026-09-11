package obs

import (
	"context"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracing owns the provider instead of changing process-global OTel state.
// Exporter configuration is kept separate from HTTP instrumentation.
type Tracing struct {
	provider *sdktrace.TracerProvider
	disabled bool
}

func NewTracing(version string) (*Tracing, error) {
	res, err := resource.New(context.Background(), resource.WithAttributes(attribute.String("service.name", "ollame"), attribute.String("service.version", version)), resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, err
	}
	disabled := strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true")
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if !disabled {
		exporter, err := traceExporter(context.Background())
		if err != nil {
			return nil, err
		}
		if exporter != nil {
			options = append(options, sdktrace.WithBatcher(exporter))
		}
	}
	return &Tracing{provider: sdktrace.NewTracerProvider(options...), disabled: disabled}, nil
}
func (t *Tracing) Close(ctx context.Context) error { return t.provider.Shutdown(ctx) }
func (t *Tracing) options() []otelhttp.Option {
	return []otelhttp.Option{otelhttp.WithTracerProvider(t.provider), otelhttp.WithPropagators(propagation.TraceContext{}), otelhttp.WithMeterProvider(noop.NewMeterProvider())}
}
func (t *Tracing) Handler(next http.Handler) http.Handler {
	if t.disabled {
		return next
	}
	return otelhttp.NewHandler(next, "ollame", t.options()...)
}
func (t *Tracing) Transport(base http.RoundTripper) http.RoundTripper {
	if t.disabled {
		return base
	}
	return otelhttp.NewTransport(base, t.options()...)
}
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
