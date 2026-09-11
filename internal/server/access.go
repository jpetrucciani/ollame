package server

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/obs"
)

func newRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(time.Now().UnixMilli()))
	copy(raw[:6], timestamp[2:])
	encoded := new(big.Int).SetBytes(raw[:]).Text(32)
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	encoded = strings.Map(func(c rune) rune {
		if c >= 'a' {
			return rune(alphabet[int(c-'a')+10])
		}
		return c
	}, encoded)
	return strings.Repeat("0", 26-len(encoded)) + encoded, nil
}

type accessWriter struct {
	http.ResponseWriter
	status int
}

type accessContextKey struct{}
type accessDetails struct {
	model, upstreamModel, callID, doneReason     string
	stream                                       bool
	estimatedTokens                              bool
	usageRecorded                                bool
	aborted                                      bool
	abortReason                                  obs.AbortReason
	upstreamStatus                               int
	ttftMS                                       *int64
	ttftSeconds                                  *float64
	promptTokens, completionTokens, cachedTokens *int
	dropped                                      []string
}

func requestDetails(r *http.Request) *accessDetails {
	if details, ok := r.Context().Value(accessContextKey{}).(*accessDetails); ok {
		return details
	}
	return &accessDetails{}
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *accessWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *accessWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(body)
}
func (w *accessWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}
func (s *Server) logAccess(state *snapshot, r *http.Request, w *accessWriter, started time.Time, id string) {
	route := routeLabel(r.Pattern)
	token, err := state.tokens.Authorize(r.Header, state.config.Auth)
	if err != nil {
		token = "unauthenticated"
		for _, path := range state.config.Auth.PublicPaths {
			if r.URL.Path == path {
				token = "anonymous"
				break
			}
		}
	}
	if token == "" {
		token = "anonymous"
	}
	status := w.status
	if status == 0 {
		status = 200
	}
	s.metrics.Request(route, token, status, time.Since(started))
	details := requestDetails(r)
	if !details.usageRecorded {
		s.metrics.Usage(details.model, token, obs.TokenUsage{Prompt: details.promptTokens, Completion: details.completionTokens, Cached: details.cachedTokens, Estimated: details.estimatedTokens})
	}
	if details.ttftSeconds != nil {
		s.metrics.TTFT(details.model, *details.ttftSeconds)
	}
	if !state.config.Log.Access {
		return
	}
	event := state.logger.Info().Str("request_id", s.redactor.UpstreamError(id)).Str("method", r.Method).Str("route", route).Int("status", status).Str("token", token).Int64("duration_ms", time.Since(started).Milliseconds())
	if traceID := obs.TraceID(r.Context()); traceID != "" {
		event.Str("trace_id", traceID)
	}
	if details.model != "" {
		event.Str("model", details.model).Str("upstream_model", details.upstreamModel).Bool("stream", details.stream)
	}
	if details.upstreamStatus != 0 {
		event.Int("upstream_status", details.upstreamStatus)
	}
	if details.callID != "" {
		event.Str("litellm_call_id", s.redactor.UpstreamError(details.callID))
	}
	if details.doneReason != "" {
		event.Str("done_reason", details.doneReason)
	}
	if details.aborted {
		event.Str("abort_reason", string(details.abortReason))
	}
	if details.ttftMS != nil {
		event.Int64("ttft_ms", *details.ttftMS)
	}
	if details.estimatedTokens {
		event.Bool("estimated_tokens", true)
	}
	if details.promptTokens != nil {
		event.Int("prompt_tokens", *details.promptTokens)
	}
	if details.completionTokens != nil {
		event.Int("completion_tokens", *details.completionTokens)
	}
	if details.cachedTokens != nil {
		event.Int("cached_tokens", *details.cachedTokens)
	}
	if len(details.dropped) != 0 {
		event.Strs("dropped", details.dropped)
	}
	event.Msg("request")
}
