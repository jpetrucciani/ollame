package server

import (
	"context"
	"time"

	"github.com/jpetrucciani/ollame/internal/auth"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/obs"
)

func (s *Server) authReloadLoop(ctx context.Context, reload <-chan struct{}) {
	for {
		timer := time.NewTimer(time.Duration(s.active.Load().config.Auth.ReloadInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case now := <-timer.C:
			s.reloadTokens(now)
		case _, ok := <-reload:
			timer.Stop()
			if !ok {
				reload = nil
				continue
			}
			if ctx.Err() == nil {
				s.reloadTokens(time.Now())
			}
		}
	}
}

// reloadTokens never waits for upstream discovery. Publication combines new
// authentication with the latest compatible catalog/client in one pointer swap.
func (s *Server) reloadTokens(now time.Time) {
	defer func() {
		select {
		case s.candidates <- struct{}{}:
		default:
		}
	}()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.draining.Load() {
		return
	}
	current := s.active.Load()
	authConfig := current.config.Auth
	inlineFailed := false
	candidate, loadErr := s.source.Load()
	if loadErr != nil {
		inlineFailed = true
		logger := s.logger()
		logger.Error().Msg("configuration source read failed")
		s.metrics.Reload(obs.ReloadInvalid)
	} else {
		// Validate authentication against the last-good non-auth configuration.
		// An unrelated invalid candidate must not prevent a token revocation.
		validation := current.config
		validation.Auth = candidate.Config.Auth
		if err := validation.Validate(config.ServeProfile); err != nil {
			inlineFailed = true
			logger := s.logger()
			logger.Error().Msg("authentication configuration reload rejected")
			s.metrics.Reload(obs.ReloadAuthInvalid)
		} else {
			authConfig = candidate.Config.Auth
		}
	}
	sources := auth.ReadSources(authConfig, s.environment, s.envTokens, s.flagTokens)
	if inlineFailed {
		// Cached TOML is not a successful read of the inline token source.
		// Continue reading external sources, but let inline stale grace expire.
		for i := range sources {
			if sources[i].ID == "inline" {
				sources[i].Err = auth.ErrInvalidSource
			}
		}
	}
	next, tokens, failures, err := s.authState.Update(now, time.Duration(authConfig.StaleGrace), sources)
	for _, failure := range failures {
		s.metrics.SourceError(failure.Source, failure.Expired)
		logger := s.logger()
		logger.Error().Str("source", failure.Source).Bool("expired", failure.Expired).Msg("token source read failed")
	}
	if err != nil {
		logger := s.logger()
		logger.Error().Msg("token reload rejected")
		s.metrics.Reload(obs.ReloadAuthInvalid)
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	latest := s.active.Load()
	if s.draining.Load() || latest.generation != current.generation {
		return
	}
	updated := *latest
	updated.config.Auth = authConfig
	if !inlineFailed {
		updated.provenance = mergeAuthProvenance(latest.provenance, candidate.Provenance)
	}
	updated.tokens = tokens
	s.authState = next
	s.active.Store(&updated)
}
