# Tracing

Ollame instruments API requests and upstream HTTP attempts with OpenTelemetry.
Access logs include `trace_id`; incoming W3C `traceparent` continues through the
server span and its upstream client spans. Each retry gets its own client span.
The provider belongs to the server and is shared across configuration reloads.
Tracing environment settings take effect at process startup.

Local correlation is enabled by default. Export is enabled when either OTLP
endpoint variable is set, or with `OTEL_TRACES_EXPORTER=otlp`. Set
`OTEL_TRACES_EXPORTER=none` to keep correlation without export. Set
`OTEL_SDK_DISABLED=true` to disable HTTP tracing entirely.

For an HTTP collector:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 \
OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf \
OTEL_SERVICE_NAME=ollame \
ollame serve --config ollame.toml
```

The general endpoint gets `/v1/traces` appended for HTTP. A traces-specific
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is the full destination URL and takes
precedence. Protocol selection similarly prefers
`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL` over `OTEL_EXPORTER_OTLP_PROTOCOL`.
Supported protocols are `http/protobuf` (default), `http/json`, and `grpc`.
For gRPC use the collector's gRPC endpoint, conventionally port 4317.
Unsupported exporter or protocol selections fail startup.

The official exporters handle standard OTLP headers, TLS certificates, client
certificates, compression, and timeout variables. Sampling uses
`OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG`; resource attribution uses
`OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`. Batch queue settings use the
standard `OTEL_BSP_*` variables. Keep credentials out of resource attributes.
See the official [HTTP exporter configuration](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp)
and [gRPC exporter configuration](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc).

Exported HTTP spans retain trace relationships, timing, HTTP methods, numeric
status codes, body sizes, and ports. Span names are fixed. URLs, user agents,
headers, bodies, exception events, links, and free-form status descriptions are
excluded at the export boundary. This also removes credential-bearing URLs from
transport errors. Configured resource attributes are operator-owned and exported
as configured.

Export runs through the SDK's bounded batch queue and does not block request
handling when the queue is full. Graceful shutdown gives the provider a separate
2-second flush budget. Collector outages can lose telemetry; they do not change
API responses. Shutdown flush failures produce a warning. A second termination
signal exits immediately and can discard pending spans.

The real-Collector acceptance test builds and runs ollame, then runs the pinned
Collector image with loopback-only OTLP ports. It checks all three protocols,
server/client parentage, shutdown flushing, and exclusion of synthetic request
secrets. Docker must be available:

```sh
OLLAME_TEST_TRACING=1 CGO_ENABLED=0 go test ./test -run '^TestRealTraceExport$' -count=1
```

This test covers a failed real upstream connection. Continuation into LiteLLM's
own exported spans and provider traces still requires the full integration stack.
