package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/stream"
	"github.com/jpetrucciani/ollame/internal/translate"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

type responseMetadata struct {
	Model                 string            `json:"model"`
	CreatedAt             time.Time         `json:"created_at"`
	Done                  bool              `json:"done"`
	DoneReason            string            `json:"done_reason,omitempty"`
	Logprobs              []json.RawMessage `json:"logprobs,omitempty"`
	TotalDuration         int64             `json:"total_duration,omitempty"`
	LoadDuration          int64             `json:"load_duration,omitempty"`
	PromptEvalDuration    int64             `json:"prompt_eval_duration,omitempty"`
	EvalDuration          int64             `json:"eval_duration,omitempty"`
	PromptEvalCount       *int              `json:"prompt_eval_count,omitempty"`
	PromptEvalCachedCount *int              `json:"prompt_eval_cached_count,omitempty"`
	EvalCount             *int              `json:"eval_count,omitempty"`
}

type chatResponse struct {
	responseMetadata
	Message translate.Message `json:"message"`
}

type generateResponse struct {
	responseMetadata
	Response string `json:"response"`
	Thinking string `json:"thinking,omitempty"`
}

func responseEnvelope(response chatResponse, generate bool) any {
	if generate {
		return generateResponse{responseMetadata: response.responseMetadata, Response: response.Message.Content, Thinking: response.Message.Thinking}
	}
	return response
}

func responseChunk(model string, output stream.Output) chatResponse {
	return chatResponse{responseMetadata: responseMetadata{Model: model, CreatedAt: time.Now().UTC(), Logprobs: output.Logprobs}, Message: translate.Message{Role: "assistant", Content: output.Content, Thinking: output.Thinking, ToolCalls: output.ToolCalls}}
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	s.inference(w, r, false)
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	s.inference(w, r, true)
}

func (s *Server) inference(w http.ResponseWriter, r *http.Request, generate bool) {
	started := time.Now()
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
	var request translate.ChatRequest
	var generateRequest translate.GenerateRequest
	var input any = &request
	if generate {
		input = &generateRequest
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(input); err != nil {
		bodyError(w, err)
		return
	}
	local := len(request.Messages) == 0
	if generate {
		request.Model, request.KeepAlive, request.DebugRenderOnly = generateRequest.Model, generateRequest.KeepAlive, generateRequest.DebugRenderOnly
		local = generateRequest.Prompt == "" && len(generateRequest.Images) == 0 && generateRequest.Suffix == ""
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		bodyError(w, err)
		return
	}
	entry, err := state.catalog.Resolve(request.Model)
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	keepAlive, err := parseKeepAlive(request.KeepAlive)
	details := requestDetails(r)
	details.model, details.upstreamModel = entry.Name, entry.Target
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	if request.DebugRenderOnly {
		inferenceError(w, fmt.Errorf("%w: _debug_render_only is not supported by ollame", translate.ErrInvalidRequest), request.Model)
		return
	}
	if local {
		s.recordUse(entry, keepAlive)
		response := responseChunk(request.Model, stream.Output{})
		response.Done, response.DoneReason = true, "load"
		if keepAlive == 0 {
			response.DoneReason = "unload"
		}
		writeJSON(w, 200, responseEnvelope(response, generate))
		details.doneReason = response.DoneReason
		return
	}
	var translated translate.ChatResult
	endpoint := "chat/completions"
	if generate {
		var result translate.GenerateResult
		result, err = translate.Generate(generateRequest, state.config, state.catalog, token)
		translated, endpoint = result.ChatResult, result.Endpoint
	} else {
		translated, err = translate.Chat(request, state.config, state.catalog, token)
	}
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	promptBytes := 0
	if state.config.Compat.EstimateTokens {
		promptBytes, err = translate.InputTextBytes(translated.Body)
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
	}
	id := w.Header().Get("X-Request-Id")
	details.stream = translated.DownstreamStream
	for _, dropped := range translated.Dropped {
		details.dropped = append(details.dropped, dropped.Field)
		field := translate.MetricField(dropped.Field)
		s.metrics.Drop(field)
		if field == "other" {
			name := s.redactor.UpstreamError(dropped.Name)
			if len(name) > 64 {
				name = strings.ToValidUTF8(name[:64], "")
			}
			state.logger.Debug().Str("field", name).Msg("request field dropped")
		}
	}
	s.metrics.Synthesized(translated.Synthesized)
	w.Header().Set("X-Request-Id", id)
	sent := time.Now()
	response, err := state.client.PostJSON(ctx, endpoint, translated.Body, state.catalog, upstream.RequestInfo{RequestID: id, UserAgent: "ollame/" + s.version}, int64(state.config.Limits.MaxUpstreamJSONBytes))
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	defer response.Body.Close()
	details.upstreamStatus = response.StatusCode
	details.callID = response.Header.Get("X-Litellm-Call-Id")
	headers := time.Now()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		upstreamError(w, response, request.Model)
		return
	}
	s.recordUse(entry, keepAlive)
	options := stream.Assembly{Aggregate: !translated.DownstreamStream, Visible: translated.ThinkingVisible, ThinkTags: state.config.Compat.ThinkTags, ThinkInitial: state.config.Compat.ThinkInitial, Limits: state.config.Limits, BadToolArguments: state.config.Compat.BadToolArguments}
	if entry.Behavior.ThinkTags != nil {
		options.ThinkTags = *entry.Behavior.ThinkTags
	}
	if entry.Behavior.ThinkInitial != nil {
		options.ThinkInitial = *entry.Behavior.ThinkInitial
	}
	assembly, err := stream.NewAssembler(options)
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	controller := http.NewResponseController(w)
	defer func() {
		details.doneReason = assembly.Reason
		if assembly.ContentFiltered {
			s.metrics.ContentFiltered(entry.Name)
		}
		if translated.UpstreamStream && !assembly.First.IsZero() {
			elapsed := assembly.First.Sub(started).Milliseconds()
			details.ttftMS = &elapsed
			seconds := assembly.First.Sub(sent).Seconds()
			details.ttftSeconds = &seconds
		}
		details.estimatedTokens = assembly.UsageEstimated
		if usage := assembly.Usage; usage != nil {
			details.promptTokens, details.completionTokens = &usage.PromptTokens, &usage.CompletionTokens
			if usage.PromptTokensDetails != nil {
				details.cachedTokens = usage.PromptTokensDetails.CachedTokens
			}
		}
	}()
	written := false
	emit := func(value any) (emitErr error) {
		defer func() {
			if emitErr != nil {
				s.recordAbort(r, emitErr, true)
			}
		}()
		if err := controller.SetWriteDeadline(time.Now().Add(time.Duration(state.config.Server.WriteStallTimeout))); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		written = true
		if err := json.NewEncoder(w).Encode(value); err != nil {
			return err
		}
		return controller.Flush()
	}
	emitOutput := func(output stream.Output) error {
		return emit(responseEnvelope(responseChunk(request.Model, output), generate))
	}
	if translated.UpstreamStream {
		var decoder *stream.Decoder
		decoder, err = stream.NewDecoder(response.Body, stream.MaxEventBytes)
		if err == nil {
			for {
				var chunk stream.Chunk
				chunk, err = decoder.Next()
				if errors.Is(err, io.EOF) {
					err = nil
					break
				}
				if err != nil {
					break
				}
				if err = assembly.Feed(chunk, time.Now(), emitOutput); err != nil {
					break
				}
			}
		}
	} else {
		var raw []byte
		raw, err = io.ReadAll(response.Body)
		if err == nil {
			var chunk stream.Chunk
			chunk, err = stream.DecodeAggregate(raw)
			if err == nil {
				err = assembly.Feed(chunk, time.Now(), emitOutput)
			}
		}
	}
	var output stream.Output
	if err == nil {
		output, err = assembly.Finish(emitOutput)
	}
	if err != nil {
		if translated.UpstreamStream || translated.DownstreamStream {
			s.recordAbort(r, err, false)
		}
		cancel()
		if written {
			_ = emit(map[string]string{"error": inferenceMessage(err)})
		} else {
			inferenceError(w, err, request.Model)
		}
		return
	}
	if state.config.Compat.EstimateTokens {
		assembly.EstimateUsage(promptBytes)
	}
	final := responseChunk(request.Model, output)
	final.Done, final.DoneReason = true, assembly.Reason
	final.TotalDuration = time.Since(started).Nanoseconds()
	if usage := assembly.Usage; usage != nil {
		final.PromptEvalCount, final.EvalCount = &usage.PromptTokens, &usage.CompletionTokens
		if usage.PromptTokensDetails != nil {
			final.PromptEvalCachedCount = usage.PromptTokensDetails.CachedTokens
		}
	}
	if translated.UpstreamStream {
		final.LoadDuration = headers.Sub(started).Nanoseconds()
		if !assembly.First.IsZero() {
			final.PromptEvalDuration = assembly.First.Sub(headers).Nanoseconds()
			final.EvalDuration = assembly.Last.Sub(assembly.First).Nanoseconds()
		}
	} else {
		final.EvalDuration = time.Since(sent).Nanoseconds()
	}
	if final.EvalCount != nil && *final.EvalCount > 0 && final.EvalDuration < 1 {
		final.EvalDuration = 1
	}
	if translated.DownstreamStream {
		_ = emit(responseEnvelope(final, generate))
	} else {
		_ = controller.SetWriteDeadline(time.Now().Add(time.Duration(state.config.Server.WriteStallTimeout)))
		writeJSON(w, 200, responseEnvelope(final, generate))
	}
}

func inferenceMessage(err error) string {
	if errors.Is(err, translate.ErrInvalidEmbeddings) {
		return err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream timeout"
	}
	if errors.Is(err, stream.ErrLimit) || errors.Is(err, upstream.ErrResponseTooLarge) {
		return "upstream response too large"
	}
	if errors.Is(err, translate.ErrInvalidRequest) {
		return strings.TrimPrefix(err.Error(), translate.ErrInvalidRequest.Error()+": ")
	}
	for _, known := range []error{stream.ErrMalformed, stream.ErrUnexpectedEOF, stream.ErrToolArguments} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	var remote *stream.RemoteError
	if errors.As(err, &remote) {
		return remote.Message
	}
	return "upstream unavailable"
}
func inferenceError(w http.ResponseWriter, err error, model string) {
	code, message := 502, inferenceMessage(err)
	switch {
	case errors.Is(err, catalog.ErrModelRequired):
		code, message = 400, "model is required"
	case errors.Is(err, catalog.ErrNotFound):
		code, message = 404, "model '"+model+"' not found"
	case errors.Is(err, translate.ErrInvalidRequest):
		code = 400
	case errors.Is(err, context.DeadlineExceeded):
		code = 504
	}
	writeJSON(w, code, map[string]string{"error": message})
}
func upstreamError(w http.ResponseWriter, response *http.Response, model string) {
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		inferenceError(w, err, model)
		return
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	message := body.Error.Message
	if message == "" {
		message = "upstream request failed"
	}
	code := 502
	switch response.StatusCode {
	case 400, 422:
		code = 400
	case 401, 403:
		message = "upstream rejected credentials"
	case 404:
		code, message = 404, "model '"+model+"' not found"
	case 408:
		code, message = 504, "upstream timeout"
	case 429:
		code = 429
		w.Header().Set("Retry-After", response.Header.Get("Retry-After"))
	default:
		message = "upstream: " + message
	}
	writeJSON(w, code, map[string]string{"error": message})
}

type runningModel struct {
	Name          string          `json:"name"`
	Model         string          `json:"model"`
	Size          int64           `json:"size"`
	Digest        string          `json:"digest"`
	Details       catalog.Details `json:"details"`
	ExpiresAt     time.Time       `json:"expires_at"`
	SizeVRAM      int64           `json:"size_vram"`
	ContextLength int             `json:"context_length"`
}

func running(entry catalog.Entry, expires time.Time) runningModel {
	return runningModel{Name: entry.Name, Model: entry.Name, Digest: entry.Digest, Details: entry.Details, ExpiresAt: expires, ContextLength: entry.Details.ContextLength}
}
func (s *Server) recordUse(entry catalog.Entry, duration time.Duration) {
	s.psMu.Lock()
	defer s.psMu.Unlock()
	if s.recent == nil {
		s.recent = make(map[string]runningModel)
	}
	now := time.Now()
	for name, model := range s.recent {
		if !model.ExpiresAt.After(now) {
			delete(s.recent, name)
		}
	}
	if duration == 0 {
		delete(s.recent, entry.Name)
		return
	}
	if duration < 0 {
		duration = 100 * 365 * 24 * time.Hour
	}
	s.recent[entry.Name] = running(entry, now.Add(duration))
}
func (s *Server) ps(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	if !s.authorize(w, r, state) {
		return
	}
	models := []runningModel{}
	now := time.Now()
	switch state.config.Management.PS {
	case "catalog":
		for _, entry := range state.catalog.Entries() {
			models = append(models, running(entry, now.Add(5*time.Minute)))
		}
	case "recent":
		s.psMu.Lock()
		for name, model := range s.recent {
			if model.ExpiresAt.After(now) {
				models = append(models, model)
			} else {
				delete(s.recent, name)
			}
		}
		s.psMu.Unlock()
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].ExpiresAt.Equal(models[j].ExpiresAt) {
			return models[i].Name < models[j].Name
		}
		return models[i].ExpiresAt.After(models[j].ExpiresAt)
	})
	writeJSON(w, 200, map[string]any{"models": models})
}
func parseKeepAlive(raw json.RawMessage) (time.Duration, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 5 * time.Minute, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		value, err := time.ParseDuration(text)
		if err == nil {
			return value, nil
		}
	} else {
		var seconds float64
		if json.Unmarshal(raw, &seconds) == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
			if seconds < 0 {
				return -1, nil
			}
			if seconds < float64(math.MaxInt64)/float64(time.Second) {
				return time.Duration(seconds * float64(time.Second)), nil
			}
		}
	}
	return 0, fmt.Errorf("%w: invalid keep_alive", translate.ErrInvalidRequest)
}
