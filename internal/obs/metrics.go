package obs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ReloadResult string

const (
	ReloadSuccess        ReloadResult = "success"
	ReloadUnchanged      ReloadResult = "unchanged"
	ReloadInvalid        ReloadResult = "invalid"
	ReloadAuthInvalid    ReloadResult = "auth_invalid"
	ReloadUpstreamFailed ReloadResult = "upstream_failed"
	ReloadCatalogFailed  ReloadResult = "catalog_failed"
)

type Metrics struct {
	registry          *prometheus.Registry
	reloads           *prometheus.CounterVec
	sourceErrors      *prometheus.CounterVec
	refreshErrors     prometheus.Counter
	lastSuccess       prometheus.Gauge
	catalogModels     *prometheus.GaugeVec
	requestsByToken   *prometheus.CounterVec
	requestsAggregate *prometheus.CounterVec
	labeledRegistry   *prometheus.Registry
	aggregateRegistry *prometheus.Registry
	durations         *prometheus.HistogramVec
	authFailures      *prometheus.CounterVec
	inflight          *prometheus.GaugeVec
	tokensByToken     *prometheus.CounterVec
	tokensAggregate   *prometheus.CounterVec
	ttft              *prometheus.HistogramVec
	dropped           *prometheus.CounterVec
	synthesized       prometheus.Counter
	filtered          *prometheus.CounterVec
	upstreamRequests  *prometheus.CounterVec
	aborts            *prometheus.CounterVec
}

func NewMetrics() (*Metrics, error) {
	m := &Metrics{
		registry:          prometheus.NewRegistry(),
		aborts:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_stream_aborts_total", Help: "Aborted streams by bounded failure reason."}, []string{"reason"}),
		upstreamRequests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_upstream_requests_total", Help: "Upstream HTTP attempts, including retries and discovery."}, []string{"endpoint", "code"}),
		dropped:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_translation_dropped_total", Help: "Dropped input fields by fixed option name or other."}, []string{"field"}),
		synthesized:       prometheus.NewCounter(prometheus.CounterOpts{Name: "ollame_tool_results_synthesized_total", Help: "Missing tool results synthesized during translation."}),
		filtered:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_content_filtered_total", Help: "Responses terminated by upstream content filtering."}, []string{"model"}),
		reloads:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_config_reloads_total", Help: "Configuration reload outcomes."}, []string{"result"}),
		sourceErrors:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_secret_source_errors_total", Help: "Failed secret source reads and stale expiry cycles."}, []string{"source", "reason"}),
		refreshErrors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "ollame_catalog_refresh_errors_total", Help: "Failed catalog discovery or rebuild attempts."}),
		lastSuccess:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "ollame_catalog_last_success_timestamp_seconds", Help: "Unix timestamp of the last successfully published catalog."}),
		catalogModels:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ollame_catalog_models", Help: "Listed models by source in the active catalog."}, []string{"source"}),
		requestsByToken:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_requests_total", Help: "Completed API requests."}, []string{"route", "code", "token"}),
		requestsAggregate: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_requests_total", Help: "Completed API requests."}, []string{"route", "code"}),
		labeledRegistry:   prometheus.NewRegistry(),
		aggregateRegistry: prometheus.NewRegistry(),
		durations:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ollame_request_duration_seconds", Help: "API request duration in seconds.", Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 30, 60, 120, 300, 900, 3600}}, []string{"route"}),
		authFailures:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_auth_failures_total", Help: "Rejected client authentication attempts."}, []string{"reason"}),
		inflight:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ollame_inflight_requests", Help: "API requests currently executing a route handler."}, []string{"route"}),
		tokensByToken:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_tokens_total", Help: "Observed upstream token usage."}, []string{"model", "token", "kind", "estimated"}),
		tokensAggregate:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ollame_tokens_total", Help: "Observed upstream token usage."}, []string{"model", "kind", "estimated"}),
		ttft:              prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ollame_upstream_ttft_seconds", Help: "Time from upstream request send to its first streamed token.", Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 30, 60, 120, 300}}, []string{"model"}),
	}
	for _, collector := range []prometheus.Collector{m.reloads, m.sourceErrors, m.refreshErrors, m.lastSuccess, m.catalogModels, m.durations, m.authFailures, m.inflight, m.ttft, m.dropped, m.synthesized, m.filtered, m.upstreamRequests, m.aborts} {
		if err := m.registry.Register(collector); err != nil {
			return nil, err
		}
	}
	if err := m.labeledRegistry.Register(m.requestsByToken); err != nil {
		return nil, err
	}
	if err := m.aggregateRegistry.Register(m.requestsAggregate); err != nil {
		return nil, err
	}
	if err := m.labeledRegistry.Register(m.tokensByToken); err != nil {
		return nil, err
	}
	if err := m.aggregateRegistry.Register(m.tokensAggregate); err != nil {
		return nil, err
	}
	m.catalogModels.WithLabelValues("upstream").Set(0)
	m.catalogModels.WithLabelValues("alias").Set(0)
	return m, nil
}
func (m *Metrics) Handler(tokenLabels bool) http.Handler {
	requests := m.aggregateRegistry
	if tokenLabels {
		requests = m.labeledRegistry
	}
	return promhttp.HandlerFor(prometheus.Gatherers{m.registry, requests}, promhttp.HandlerOpts{})
}

// Keep both views so a token-label configuration reload neither exposes disabled
// attribution nor resets aggregate request history.
func (m *Metrics) Request(route, token string, status int, elapsed time.Duration) {
	code := strconv.Itoa(status)
	m.requestsByToken.WithLabelValues(route, code, token).Inc()
	m.requestsAggregate.WithLabelValues(route, code).Inc()
	m.durations.WithLabelValues(route).Observe(elapsed.Seconds())
}
func (m *Metrics) AuthFailure(missing bool) {
	reason := "invalid"
	if missing {
		reason = "missing"
	}
	m.authFailures.WithLabelValues(reason).Inc()
}
func (m *Metrics) Inflight(route string, delta float64) { m.inflight.WithLabelValues(route).Add(delta) }
func (m *Metrics) Reload(result ReloadResult)           { m.reloads.WithLabelValues(string(result)).Inc() }
func (m *Metrics) SourceError(source string, expired bool) {
	switch source {
	case "inline", "file", "directory", "env", "flags", "upstream_key":
	default:
		source = "other"
	}
	reason := "read_or_parse"
	if expired {
		reason = "stale_expired"
	}
	m.sourceErrors.WithLabelValues(source, reason).Inc()
}
func (m *Metrics) CatalogError() { m.refreshErrors.Inc() }
func (m *Metrics) CatalogSuccess(now time.Time, upstream, aliases int) {
	m.catalogModels.WithLabelValues("upstream").Set(float64(upstream))
	m.catalogModels.WithLabelValues("alias").Set(float64(aliases))
	m.lastSuccess.Set(float64(now.UnixNano()) / 1e9)
}
