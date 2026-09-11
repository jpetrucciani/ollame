# Dependency choices

The accepted spec selects koanf with TOML and pflag. The initial implementation
pins the following stable releases, checked against the official repositories and
Go module proxy on 2026-09-10. The current local compiler is Go 1.26.7.

| Module                                             | Pin     | Fit                                                                |
| -------------------------------------------------- | ------- | ------------------------------------------------------------------ |
| [koanf/v2](https://github.com/knadh/koanf)         | v2.3.6  | Layered map merges without a process-global config singleton       |
| koanf/providers/confmap                            | v1.0.1  | Official in-memory provider, separate module from the same project |
| [go-toml/v2](https://github.com/pelletier/go-toml) | v2.4.3  | TOML decoding with unknown-field rejection and typed text decoding |
| [pflag](https://github.com/spf13/pflag)            | v1.0.10 | GNU-style flags, repeatable values, generated help                 |

koanf/confmap releases were published on 2026-08-04, go-toml on 2026-07-05,
and pflag on 2025-09-02. The existing approved choices avoid introducing another
framework. The implementation also uses zerolog v1.35.1 for structured logging,
Prometheus client_golang v1.24.1 for metrics, and OpenTelemetry v1.46.0 with
otelhttp v0.71.0 for tracing and OTLP export. `go.mod` and `go.sum` are the
authoritative dependency inventory and pins.

Ollame is distributed under the root MIT license. Third-party dependencies
retain their own licenses.

Config source diagnostics deliberately avoid TOML source excerpts and raw flag
values, which can contain credentials. Configuration keys and help text derive
from the typed schema, while mutable koanf objects remain local to loading.

Build and check with `CGO_ENABLED=0`. The Nix helper scripts set it explicitly,
matching the static binary requirement. The race detector requires a C toolchain
and a separate race-enabled test environment; static checks do not prove race
freedom.
