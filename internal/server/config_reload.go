package server

import (
	"context"
	"reflect"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/obs"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

// Discovery has its own worker so the auth poller never waits on network I/O.
func (s *Server) configReloadLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.candidates:
			s.reloadConfig(ctx)
		}
	}
}

func retainRestartFields(candidate *config.Config, active config.Config) bool {
	changed := candidate.Server.Listen != active.Server.Listen || candidate.Server.AdminListen != active.Server.AdminListen || candidate.Server.TLSCert != active.Server.TLSCert || candidate.Server.TLSKey != active.Server.TLSKey
	candidate.Server.Listen = active.Server.Listen
	candidate.Server.AdminListen = active.Server.AdminListen
	candidate.Server.TLSCert = active.Server.TLSCert
	candidate.Server.TLSKey = active.Server.TLSKey
	return changed
}

func (s *Server) reloadConfig(ctx context.Context) {
	if ctx.Err() != nil || s.draining.Load() {
		return
	}
	current := s.active.Load()
	loaded, err := s.source.Load()
	if err != nil {
		return // The auth poller reports source failures.
	}
	candidate := loaded.Config
	candidate.Auth = current.config.Auth
	if retainRestartFields(&candidate, current.config) {
		logger := s.logger()
		logger.Error().Msg("listener configuration changes require restart")
	}
	if err := candidate.Validate(config.ServeProfile); err != nil {
		logger := s.logger()
		logger.Error().Msg("configuration reload rejected")
		s.metrics.Reload(obs.ReloadInvalid)
		return
	}
	key, err := upstream.ReadKey(candidate.Upstream)
	keyRetained := err != nil
	if err != nil {
		logger := s.logger()
		logger.Error().Msg("upstream key read failed; retaining active key")
		s.metrics.SourceError("upstream_key", false)
		key = current.key
	}
	if reflect.DeepEqual(candidate, current.config) && key == current.key {
		s.metrics.Reload(obs.ReloadUnchanged)
		return
	}
	client, err := upstream.New(candidate.Upstream, key, s.metrics, s.tracing)
	if err != nil {
		logger := s.logger()
		logger.Error().Msg("upstream configuration reload failed")
		s.metrics.Reload(obs.ReloadUpstreamFailed)
		return
	}
	published := false
	defer func() {
		if !published {
			client.Close()
		}
	}()
	models, warnings, err := client.Discover(ctx, candidate)
	if err != nil {
		logger := s.logger()
		logger.Error().Msg("configuration reload upstream_failed")
		s.metrics.Reload(obs.ReloadUpstreamFailed)
		return
	}
	next, catalogWarnings, err := catalog.Build(candidate, models, current.catalog, time.Now())
	if err != nil {
		logger := s.logger()
		logger.Error().Msg("configuration reload catalog failed")
		s.metrics.Reload(obs.ReloadCatalogFailed)
		return
	}
	// Recheck the concrete source after discovery. A superseded candidate must
	// never become active merely because its earlier network request succeeded.
	latestSource, err := s.source.Load()
	if err != nil || !reflect.DeepEqual(latestSource.Config, loaded.Config) {
		return
	}
	latestKey, keyErr := upstream.ReadKey(candidate.Upstream)
	if (keyErr == nil && latestKey != key) || (keyErr != nil && key != current.key) {
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	latest := s.active.Load()
	if ctx.Err() != nil || s.draining.Load() || latest.generation != current.generation {
		return
	}
	candidate.Auth = latest.config.Auth
	provenance := mergeAuthProvenance(loaded.Provenance, latest.provenance)
	for _, key := range []string{"server.listen", "server.admin_listen", "server.tls_cert", "server.tls_key"} {
		if source, ok := latest.provenance[key]; ok {
			provenance[key] = source
		} else {
			delete(provenance, key)
		}
	}
	if keyRetained {
		provenance["upstream.api_key"] = "retained"
	} else if candidate.Upstream.APIKeyFile != "" {
		provenance["upstream.api_key"] = "file"
	}
	logger, err := newLogger(s.log, candidate.Log)
	if err != nil {
		return
	}
	logger = logger.With().Str("service", "ollame").Str("version", s.version).Str("commit", s.commit).Logger()
	s.active.Store(&snapshot{config: candidate, tokens: latest.tokens, catalog: next, client: client, key: key, logger: logger, generation: latest.generation + 1, provenance: provenance})
	if candidate.Log.Bodies && !latest.config.Log.Bodies {
		logger.Warn().Msg("body logging is enabled")
	}
	s.recordCatalogMetrics(next)
	s.metrics.Reload(obs.ReloadSuccess)
	s.loaded.Store(true)
	published = true
	// CloseIdleConnections preserves active requests using the prior snapshot.
	current.client.Close()
	for _, warning := range append(warnings, catalogWarnings...) {
		logger := s.logger()
		logger.Warn().Str("detail", s.redactor.UpstreamError(warning)).Msg("catalog warning")
	}
}
