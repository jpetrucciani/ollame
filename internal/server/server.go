// Package server owns listeners and immutable serving snapshots.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jpetrucciani/ollame/internal/auth"
	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/obs"
	"github.com/jpetrucciani/ollame/internal/upstream"
	"github.com/rs/zerolog"
)

type snapshot struct {
	config     config.Config
	tokens     auth.Set
	catalog    *catalog.Catalog
	client     *upstream.Client
	key        string
	logger     zerolog.Logger
	generation uint64
	provenance map[string]string
}
type Server struct {
	active      atomic.Pointer[snapshot]
	draining    atomic.Bool
	inflight    atomic.Int64
	loaded      atomic.Bool
	publishMu   sync.Mutex
	authMu      sync.Mutex
	authState   auth.State
	environment map[string]string
	envTokens   []config.Token
	flagTokens  []config.Token
	source      config.Source
	candidates  chan struct{}
	version     string
	commit      string
	log         io.Writer
	redactor    config.Redactor
	metrics     *obs.Metrics
	tracing     *obs.Tracing
	refreshMu   sync.Mutex
	lastMiss    time.Time
	psMu        sync.Mutex
	recent      map[string]runningModel
}

func New(loaded config.Loaded, source config.Source, environment map[string]string, version, commit string, log io.Writer) (*Server, error) {
	cfg := loaded.Config
	if err := cfg.Validate(config.ServeProfile); err != nil {
		return nil, err
	}
	log = zerolog.SyncWriter(log)
	logger, err := newLogger(log, cfg.Log)
	if err != nil {
		return nil, err
	}
	logger = logger.With().Str("service", "ollame").Str("version", version).Str("commit", commit).Logger()
	redactor, err := config.NewRedactor()
	if err != nil {
		return nil, err
	}
	metrics, err := obs.NewMetrics()
	if err != nil {
		return nil, err
	}
	sources := auth.ReadSources(cfg.Auth, environment, loaded.EnvTokens, loaded.FlagTokens)
	tokens, _, err := auth.NewSet(sources)
	if err != nil {
		return nil, err
	}
	if cfg.Auth.Mode == "required" && tokens.Len() == 0 {
		return nil, fmt.Errorf("auth.mode=required needs at least one valid token")
	}
	key, err := upstream.ReadKey(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	tracing, err := obs.NewTracing(version)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = tracing.Close(shutdown)
		}
	}()
	client, err := upstream.New(cfg.Upstream, key, metrics, tracing)
	if err != nil {
		return nil, err
	}
	empty, _, err := catalog.Build(cfg, nil, nil, time.Now())
	if err != nil {
		client.Close()
		return nil, err
	}
	authState, _, _, err := (auth.State{}).Update(time.Now(), time.Duration(cfg.Auth.StaleGrace), sources)
	if err != nil {
		client.Close()
		return nil, err
	}
	env := make(map[string]string, len(environment))
	for key, value := range environment {
		env[key] = value
	}
	server := &Server{version: version, commit: commit, log: log, source: source, authState: authState, environment: env, envTokens: append([]config.Token{}, loaded.EnvTokens...), flagTokens: append([]config.Token{}, loaded.FlagTokens...)}
	server.candidates = make(chan struct{}, 1)
	server.redactor = redactor
	server.metrics = metrics
	server.tracing = tracing
	provenance := copyProvenance(loaded.Provenance)
	if cfg.Upstream.APIKeyFile != "" {
		provenance["upstream.api_key"] = "file"
	}
	server.active.Store(&snapshot{config: cfg, tokens: tokens, catalog: empty, client: client, key: key, logger: logger, generation: 1, provenance: provenance})
	if cfg.Log.Bodies {
		logger.Warn().Msg("body logging is enabled")
	}
	initialized = true
	return server, nil
}
func (s *Server) Run(ctx context.Context, reload <-chan struct{}) error {
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.tracing.Close(shutdown); err != nil {
			s.active.Load().logger.Warn().Err(err).Msg("trace shutdown failed")
		}
	}()
	defer func() { s.active.Load().client.Close() }()
	cfg := s.active.Load().config
	logger := s.logger()
	logger.Info().Msg("server starting")
	network, address := "tcp", cfg.Server.Listen
	if strings.HasPrefix(address, "unix:") {
		network = "unix"
		address = strings.TrimPrefix(address, "unix:")
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return fmt.Errorf("API listen: %w", err)
	}
	defer listener.Close()
	var admin net.Listener
	if cfg.Server.AdminListen != "" {
		admin, err = net.Listen("tcp", cfg.Server.AdminListen)
		if err != nil {
			return fmt.Errorf("admin listen: %w", err)
		}
		defer admin.Close()
	}
	requestCtx, cancelRequests := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelRequests(errShutdown)
	api := &http.Server{Handler: s.tracing.Handler(s.Handler()), ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout), IdleTimeout: time.Duration(cfg.Server.IdleTimeout), MaxHeaderBytes: int(cfg.Server.MaxHeaderBytes), BaseContext: func(net.Listener) context.Context { return requestCtx }}
	adminServer := &http.Server{Handler: s.AdminHandler(), ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout), IdleTimeout: time.Duration(cfg.Server.IdleTimeout), MaxHeaderBytes: int(cfg.Server.MaxHeaderBytes)}
	httpLog := log.New(httpErrorWriter{server: s}, "", 0)
	api.ErrorLog, adminServer.ErrorLog = httpLog, httpLog
	results := make(chan error, 2)
	go func() {
		if cfg.Server.TLSCert != "" {
			results <- api.ServeTLS(listener, cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			results <- api.Serve(listener)
		}
	}()
	if admin != nil {
		go func() { results <- adminServer.Serve(admin) }()
	}
	logger.Info().Str("listener", "api").Str("address", listener.Addr().String()).Bool("tls", cfg.Server.TLSCert != "").Msg("listener started")
	if admin != nil {
		logger.Info().Str("listener", "admin").Str("address", admin.Addr().String()).Msg("listener started")
	}
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	defer cancelRefresh()
	go s.refreshLoop(refreshCtx)
	go s.authReloadLoop(refreshCtx, reload)
	go s.configReloadLoop(refreshCtx)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-results:
	}
	s.publishMu.Lock()
	s.draining.Store(true)
	s.publishMu.Unlock()
	cancelRefresh()
	shutdownStarted := time.Now()
	logger = s.logger()
	logger.Info().Dur("drain_delay_ms", time.Duration(cfg.Server.DrainDelay)).Dur("shutdown_grace_ms", time.Duration(cfg.Server.ShutdownGrace)).Int64("inflight", s.inflight.Load()).Msg("server draining")
	delay := time.NewTimer(time.Duration(cfg.Server.DrainDelay))
	<-delay.C
	logger.Info().Int64("inflight", s.inflight.Load()).Msg("waiting for active requests")
	shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownGrace))
	defer cancel()
	if err = api.Shutdown(shutdown); err != nil {
		logger.Warn().Err(err).Int64("inflight", s.inflight.Load()).Msg("shutdown grace expired")
		cancelRequests(errShutdown)
		terminal, cancelTerminal := context.WithTimeout(context.Background(), 2*time.Second)
		_ = api.Shutdown(terminal)
		cancelTerminal()
		_ = api.Close()
	}
	_ = adminServer.Close()
	logger.Info().Dur("duration_ms", time.Since(shutdownStarted)).Msg("listeners stopped")
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
func (s *Server) refreshLoop(ctx context.Context) {
	s.refresh(ctx, false)
	for {
		timer := time.NewTimer(time.Duration(s.active.Load().config.Models.RefreshInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.refresh(ctx, false)
		}
	}
}
func (s *Server) refresh(ctx context.Context, miss bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.draining.Load() {
		return
	}
	if miss {
		if time.Since(s.lastMiss) < 10*time.Second {
			return
		}
		s.lastMiss = time.Now()
	}
	current := s.active.Load()
	started := time.Now()
	logger := s.logger()
	initial := !s.loaded.Load()
	level := zerolog.DebugLevel
	if initial {
		level = zerolog.InfoLevel
	}
	logger.WithLevel(level).Bool("initial", initial).Dur("fetch_timeout_ms", time.Duration(current.config.Models.FetchTimeout)).Msg("catalog discovery started")
	models, warnings, err := current.client.Discover(ctx, current.config)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.metrics.CatalogError()
		logger := s.logger()
		logger.Error().Dur("duration_ms", time.Since(started)).Dur("retry_interval_ms", time.Duration(current.config.Models.RefreshInterval)).Str("error", s.redactor.UpstreamError(err.Error())).Msg("catalog refresh failed")
		return
	}
	next, warnings2, err := catalog.Build(current.config, models, current.catalog, time.Now())
	if err != nil {
		s.metrics.CatalogError()
		logger := s.logger()
		logger.Error().Str("error", s.redactor.UpstreamError(err.Error())).Msg("catalog rebuild failed")
		return
	}
	for _, warning := range append(warnings, warnings2...) {
		logger := s.logger()
		logger.Warn().Str("detail", s.redactor.UpstreamError(warning)).Msg("catalog warning")
	}
	if s.draining.Load() {
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	latest := s.active.Load()
	if s.draining.Load() || latest.generation != current.generation {
		return
	}
	updated := *latest
	updated.catalog = next
	s.active.Store(&updated)
	s.recordCatalogMetrics(next)
	s.loaded.Store(true)
	logger.WithLevel(level).Int("models", next.Len()).Dur("duration_ms", time.Since(started)).Msg("catalog discovery completed")
	if initial {
		logger.Info().Msg("server ready")
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		state := s.requestSnapshot(r)
		if !s.authorize(w, r, state) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, state.config.Server.RootBanner)
	})
	mux.HandleFunc("GET /version", s.buildVersion)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		state := s.requestSnapshot(r)
		if !s.authorize(w, r, state) {
			return
		}
		writeJSON(w, 200, map[string]string{"version": state.config.Compat.OllamaVersion})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		state := s.requestSnapshot(r)
		if !s.authorize(w, r, state) {
			return
		}
		writeJSON(w, 200, map[string]any{"cloud": map[string]any{"disabled": true, "source": "config"}})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		state := s.requestSnapshot(r)
		if !s.authorize(w, r, state) {
			return
		}
		writeJSON(w, 200, map[string]any{"models": state.catalog.Entries()})
	})
	mux.HandleFunc("POST /api/show", s.show)
	mux.HandleFunc("POST /api/chat", s.chat)
	mux.HandleFunc("POST /api/generate", s.generate)
	mux.HandleFunc("POST /api/embed", s.embed)
	mux.HandleFunc("POST /api/embeddings", s.embeddings)
	mux.HandleFunc("GET /api/ps", s.ps)
	s.managementRoutes(mux)
	s.passthroughRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		state := s.active.Load()
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(state.config.Upstream.RequestTimeout))
		defer cancel()
		r = r.WithContext(context.WithValue(ctx, snapshotContextKey{}, state))
		r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, &accessDetails{}))
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 128 || len(r.Header.Values("X-Request-Id")) != 1 {
			var err error
			id, err = newRequestID()
			if err != nil {
				localError(w, r, 500, "cannot generate request id")
				return
			}
		}
		w.Header().Set("X-Request-Id", id)
		recorded := &accessWriter{ResponseWriter: w}
		w = recorded
		defer func() { s.logAccess(state, r, recorded, started, id) }()
		// A context deadline alone cannot interrupt a blocked request-body read.
		// Bound the socket read as well so uploads cannot retain admission slots.
		controller := http.NewResponseController(w)
		deadline, _ := ctx.Deadline()
		if err := controller.SetReadDeadline(deadline); err != nil {
			localError(w, r, 500, "cannot set request read deadline")
			return
		}
		// Leave the deadline in place for net/http's post-handler body drain;
		// net/http resets it when preparing the connection for the next request.
		w.Header().Set("X-Ollame-Version", s.version)
		w.Header().Set("X-Ollame-Commit", s.commit)
		if strings.HasPrefix(r.URL.Path, "/v1/") && !passthroughAllowed(state, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if cors(w, r, state, mux) {
			return
		}
		_, pattern := mux.Handler(r)
		route := routeLabel(pattern)
		s.metrics.Inflight(route, 1)
		defer s.metrics.Inflight(route, -1)
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, state *snapshot) bool {
	for _, path := range state.config.Auth.PublicPaths {
		if r.URL.Path == path {
			return true
		}
	}
	_, err := state.tokens.Authorize(r.Header, state.config.Auth)
	if err == nil {
		return true
	}
	s.metrics.AuthFailure(len(r.Header.Values("Authorization")) == 0 && (!state.config.Auth.AcceptXAPIKey || len(r.Header.Values("X-Api-Key")) == 0))
	w.Header().Set("WWW-Authenticate", `Bearer realm="ollame"`)
	localError(w, r, 401, "unauthorized")
	return false
}
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	s.profilingRoutes(mux)
	labeledMetrics := s.metrics.Handler(true)
	aggregateMetrics := s.metrics.Handler(false)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		state := s.active.Load()
		if !state.config.Metrics.Enabled {
			http.NotFound(w, r)
			return
		}
		if state.config.Metrics.TokenLabels {
			labeledMetrics.ServeHTTP(w, r)
		} else {
			aggregateMetrics.ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("GET /version", s.buildVersion)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.draining.Load() || s.active.Load().config.Models.RequireOnStart && !s.loaded.Load() {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	})
	mux.HandleFunc("GET /debug/config", func(w http.ResponseWriter, _ *http.Request) {
		state := s.active.Load()
		if !state.config.Admin.Debug {
			http.NotFound(w, nil)
			return
		}
		writeJSON(w, 200, map[string]any{"config": state.config.Redacted(), "provenance": state.provenance, "generation": state.generation})
	})
	mux.HandleFunc("GET /debug/catalog", func(w http.ResponseWriter, _ *http.Request) {
		state := s.active.Load()
		if !state.config.Admin.Debug {
			http.NotFound(w, nil)
			return
		}
		writeJSON(w, 200, state.catalog.Entries())
	})
	return mux
}
func (s *Server) show(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	if !s.authorize(w, r, state) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(state.config.Server.MaxBodyBytes))
	defer r.Body.Close()
	var request struct {
		Model   string `json:"model"`
		Name    string `json:"name"`
		Verbose bool   `json:"verbose"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		bodyError(w, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("trailing JSON value")
		}
		bodyError(w, err)
		return
	}
	name := request.Model
	if name == "" {
		name = request.Name
	}
	entry, err := state.catalog.Resolve(name)
	if errors.Is(err, catalog.ErrNotFound) && state.config.Models.RefreshOnMiss {
		s.refresh(r.Context(), true)
		state = s.active.Load()
		if !s.authorize(w, r, state) {
			return
		}
		entry, err = state.catalog.Resolve(name)
	}
	if err != nil {
		code := 404
		message := "model '" + name + "' not found"
		if errors.Is(err, catalog.ErrModelRequired) {
			code = 400
			message = "model is required"
		}
		writeJSON(w, code, map[string]string{"error": message})
		return
	}
	writeJSON(w, 200, showResponse(entry))
}
func isTimeout(err error) bool {
	var timeout net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout()
}
func bodyError(w http.ResponseWriter, err error) {
	if isTimeout(err) {
		writeJSON(w, 504, map[string]string{"error": "request timed out"})
		return
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, 413, map[string]string{"error": "request body too large"})
		return
	}
	writeJSON(w, 400, map[string]string{"error": "invalid JSON request body"})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
func showResponse(entry catalog.Entry) map[string]any {
	info := map[string]any{"general.architecture": entry.Architecture, "general.basename": strings.Split(entry.Name, ":")[0], entry.Architecture + ".context_length": entry.Details.ContextLength}
	if entry.Details.EmbeddingLength > 0 {
		info[entry.Architecture+".embedding_length"] = entry.Details.EmbeddingLength
	}
	if text := entry.Details.ParameterSize; len(text) > 1 {
		n, err := strconv.ParseFloat(text[:len(text)-1], 64)
		if err == nil {
			scale := map[byte]float64{'K': 1e3, 'M': 1e6, 'B': 1e9, 'T': 1e12}[text[len(text)-1]]
			info["general.parameter_count"] = int64(n * scale)
		}
	}
	template := "{{- if .System }}{{ .System }}{{ end }}{{ .Prompt }}"
	for _, capability := range entry.Capabilities {
		if capability == "tools" {
			template = "{{- if .System }}{{ .System }}{{ end }}{{- if .Tools }}{{ .Tools }}{{ end }}{{ .Prompt }}{{- if .ToolCalls }}{{ .ToolCalls }}{{ end }}"
		}
	}
	response := map[string]any{"modelfile": "# Modelfile synthesized by ollame\n# upstream: " + entry.Target + "\nFROM " + entry.Name + "\n", "template": template, "details": entry.Details, "model_info": info, "modified_at": entry.ModifiedAt}
	if len(entry.Capabilities) > 0 {
		response["capabilities"] = entry.Capabilities
	}
	modelfile := response["modelfile"].(string)
	if entry.System != "" {
		response["system"] = entry.System
		modelfile += "SYSTEM \"\"\"" + entry.System + "\"\"\"\n"
	}
	keys := make([]string, 0, len(entry.Options))
	for key := range entry.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parameters strings.Builder
	for _, key := range keys {
		value := fmt.Sprint(entry.Options[key])
		fmt.Fprintf(&parameters, "%-30s %s\n", key, value)
		modelfile += "PARAMETER " + key + " " + value + "\n"
	}
	if parameters.Len() > 0 {
		response["parameters"] = strings.TrimSuffix(parameters.String(), "\n")
	}
	response["modelfile"] = modelfile
	return response
}

// Build identity is separate from /api/version's Ollama compatibility version.
func (s *Server) buildVersion(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version, "commit": s.commit, "ollama_version": state.config.Compat.OllamaVersion})
}
