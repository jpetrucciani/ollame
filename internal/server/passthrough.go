package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/glob"
	"github.com/jpetrucciani/ollame/internal/translate"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

func passthroughAllowed(state *snapshot, path string) bool {
	if !state.config.Passthrough.Enabled {
		return false
	}
	for _, allowed := range state.config.Passthrough.Paths {
		if path == allowed || allowed == "/v1/models/{model}" && strings.HasPrefix(path, "/v1/models/") {
			return true
		}
	}
	return false
}
func (s *Server) passthroughRoutes(mux *http.ServeMux) {
	for _, path := range []string{"chat/completions", "completions", "embeddings", "responses", "responses/compact", "messages"} {
		mux.HandleFunc("POST /v1/"+path, s.passthrough)
	}
	mux.HandleFunc("POST /v1/audio/transcriptions", s.passthrough)
	mux.HandleFunc("GET /v1/models", s.providerModels)
	mux.HandleFunc("GET /v1/models/{model}", s.providerModels)
}
func providerModel(entry catalog.Entry) any {
	return map[string]any{"id": entry.Name, "object": "model", "created": entry.ModifiedAt.Unix(), "owned_by": "ollame"}
}
func (s *Server) providerModels(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	if !s.authorize(w, r, state) {
		return
	}
	if name := r.PathValue("model"); name != "" {
		entry, err := state.catalog.Resolve(name)
		if err != nil {
			passthroughError(w, r, err)
			return
		}
		writeJSON(w, 200, providerModel(entry))
		return
	}
	models := []any{}
	for _, entry := range state.catalog.Entries() {
		models = append(models, providerModel(entry))
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": models})
}
func passthroughError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := 502, inferenceMessage(err)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		status, message = 404, "model not found"
	case errors.Is(err, catalog.ErrModelRequired):
		status, message = 400, "model is required"
	case errors.Is(err, translate.ErrInvalidRequest):
		status = 400
	case errors.As(err, &tooLarge), errors.Is(err, translate.ErrBodyTooLarge):
		status, message = 413, "request body too large"
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		status = 504
	}
	localError(w, r, status, message)
}
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	if !s.authorize(w, r, state) {
		return
	}
	if !s.admit(w, r, state) {
		return
	}
	defer s.inflight.Add(-1)
	token, _ := state.tokens.Authorize(r.Header, state.config.Auth)
	if token == "" {
		token = "anonymous"
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(state.config.Upstream.RequestTimeout))
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, int64(state.config.Server.MaxBodyBytes))
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		passthroughError(w, r, err)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	var body []byte
	var entry catalog.Entry
	contentType := r.Header.Get("Content-Type")
	if path == "audio/transcriptions" {
		body, contentType, entry, err = translate.Multipart(raw, contentType, int64(state.config.Server.MaxBodyBytes), state.config.Upstream, state.catalog, token)
	} else {
		body, entry, err = translate.Passthrough(raw, path, state.config.Upstream, state.catalog, token)
	}
	if err != nil {
		passthroughError(w, r, err)
		return
	}
	id := w.Header().Get("X-Request-Id")
	details := requestDetails(r)
	details.model, details.upstreamModel = entry.Name, entry.Target
	userAgent := "ollame/" + s.version
	if client := r.UserAgent(); client != "" {
		userAgent += " (+" + client + ")"
	}
	info := upstream.RequestInfo{RequestID: id, UserAgent: userAgent, PassthroughHeaders: r.Header}
	var response *http.Response
	if path == "audio/transcriptions" {
		response, err = state.client.PostMultipart(ctx, body, contentType, state.catalog, info, int64(state.config.Server.MaxBodyBytes), int64(state.config.Limits.MaxUpstreamJSONBytes))
	} else {
		response, err = state.client.PostJSON(ctx, path, body, state.catalog, info, int64(state.config.Limits.MaxUpstreamJSONBytes))
	}
	if err != nil {
		passthroughError(w, r, err)
		return
	}
	defer response.Body.Close()
	details.upstreamStatus = response.StatusCode
	details.callID = response.Header.Get("X-Litellm-Call-Id")
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		s.recordUse(entry, 5*time.Minute)
	}
	copyProviderHeaders(w.Header(), response.Header, state.config.Passthrough.ResponseHeaders)
	w.Header().Set("X-Ollame-Version", s.version)
	w.Header().Set("X-Request-Id", id)
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	isSSE := mediaType == "text/event-stream"
	details.stream = isSSE
	controller := http.NewResponseController(w)
	written := false
	buffer := make([]byte, 32<<10)
	var events eventProgress
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if isSSE {
				if events.Feed(buffer[:n]) {
					if observer, ok := response.Body.(interface{ EventReceived() }); ok {
						observer.EventReceived()
					}
				}
			}
			if err := controller.SetWriteDeadline(time.Now().Add(time.Duration(state.config.Server.WriteStallTimeout))); err != nil {
				if isSSE {
					s.recordAbort(r, err, true)
				}
				return
			}
			if !written {
				w.WriteHeader(response.StatusCode)
				written = true
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				if isSSE {
					s.recordAbort(r, err, true)
				}
				return
			}
			if err := controller.Flush(); err != nil {
				if isSSE {
					s.recordAbort(r, err, true)
				}
				return
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if !written {
					w.WriteHeader(response.StatusCode)
				}
				return
			}
			if isSSE {
				s.recordAbort(r, readErr, false)
			}
			cancel()
			if !written {
				passthroughError(w, r, readErr)
				return
			}
			if isSSE {
				status := 502
				if errors.Is(readErr, context.DeadlineExceeded) {
					status = 504
				}
				errorJSON, err := json.Marshal(localErrorBody(r.URL.Path, status, inferenceMessage(readErr)))
				if err != nil {
					return
				}
				if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
					return
				}
				if r.URL.Path == "/v1/messages" {
					_, _ = fmt.Fprint(w, "\n\nevent: error\n")
				} else {
					_, _ = fmt.Fprint(w, "\n\n")
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", errorJSON)
				_ = controller.Flush()
			}
			return
		}
	}
}

func copyProviderHeaders(target, source http.Header, patterns []string) {
	hop := map[string]bool{"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "content-length": true}
	for _, value := range source.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			hop[strings.ToLower(strings.TrimSpace(key))] = true
		}
	}
	compiled := []glob.Pattern{}
	for _, pattern := range patterns {
		if matcher, err := glob.Compile(strings.ToLower(pattern)); err == nil {
			compiled = append(compiled, matcher)
		}
	}
	// A nil header entry prevents net/http from inventing a sniffed Content-Type.
	target["Content-Type"] = nil
	for key, values := range source {
		lower := strings.ToLower(key)
		if hop[lower] {
			continue
		}
		allowed := lower == "content-type"
		for _, pattern := range compiled {
			if pattern.Match(lower) {
				allowed = true
				break
			}
		}
		if allowed {
			target[key] = append([]string{}, values...)
		}
	}
}

// eventProgress observes SSE framing without modifying or parsing event bytes.
// It retains only a prefix and counters, including across CRLF chunk boundaries.
type eventProgress struct {
	prefix [5]byte
	length int
	data   bool
	skipLF bool
}

func (p *eventProgress) Feed(chunk []byte) bool {
	complete := false
	for _, b := range chunk {
		if p.skipLF {
			p.skipLF = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' || b == '\n' {
			if p.length == 0 {
				complete = complete || p.data
				p.data = false
			} else if p.length >= 5 && string(p.prefix[:]) == "data:" || p.length == 4 && string(p.prefix[:4]) == "data" {
				p.data = true
			}
			p.length = 0
			p.skipLF = b == '\r'
			continue
		}
		if p.length < 5 {
			p.prefix[p.length] = b
		}
		// Saturation avoids integer overflow on unbounded provider lines.
		if p.length < 5 {
			p.length++
		}
	}
	return complete
}
