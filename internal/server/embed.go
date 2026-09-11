package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/obs"
	"github.com/jpetrucciani/ollame/internal/translate"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

type embedRequest struct {
	Model      string          `json:"model"`
	Input      json.RawMessage `json:"input"`
	Prompt     string          `json:"prompt"`
	Dimensions *int            `json:"dimensions,omitempty"`
	KeepAlive  json.RawMessage `json:"keep_alive,omitempty"`
}

func (s *Server) embed(w http.ResponseWriter, r *http.Request)      { s.embedding(w, r, false) }
func (s *Server) embeddings(w http.ResponseWriter, r *http.Request) { s.embedding(w, r, true) }
func (s *Server) embedding(w http.ResponseWriter, r *http.Request, legacy bool) {
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
	var request embedRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		bodyError(w, err)
		return
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
	dimensions := 0
	if request.Dimensions != nil && !legacy {
		dimensions = *request.Dimensions
		if dimensions <= 0 {
			inferenceError(w, fmt.Errorf("%w: dimensions must be positive", translate.ErrInvalidRequest), request.Model)
			return
		}
	}
	var inputs []string
	scalar := legacy
	if legacy {
		if request.Prompt != "" {
			inputs = []string{request.Prompt}
		}
	} else {
		inputs, scalar, err = translate.EmbeddingInputs(request.Input, state.config.Limits.MaxEmbedInputs)
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
	}
	if len(inputs) == 0 {
		if legacy {
			writeJSON(w, 200, map[string]any{"embedding": []float64{}})
		} else {
			writeJSON(w, 200, map[string]any{"model": request.Model, "embeddings": [][]float64{}})
		}
		return
	}
	if state.config.Compat.StrictCapabilities && entry.Knowledge["embedding"] == catalog.No {
		inferenceError(w, fmt.Errorf("%w: model does not support embedding", translate.ErrInvalidRequest), request.Model)
		return
	}
	normalize := state.config.Embed.Normalize
	if legacy {
		normalize = state.config.Embed.LegacyNormalize
	}
	batchSize := state.config.Embed.BatchSize
	if batchSize <= 0 {
		batchSize = len(inputs)
	}
	collected := translate.NewEmbeddingBatches(len(inputs), int64(state.config.Limits.MaxAggregateBytes))
	defer func() {
		count, known := collected.Usage()
		details.estimatedTokens = collected.HasEstimates()
		if known || count > 0 {
			details.promptTokens = &count
		}
	}()
	var firstHeaders time.Time
	for start := 0; start < len(inputs); start += batchSize {
		end := min(start+batchSize, len(inputs))
		var input any = inputs[start:end]
		if scalar {
			input = inputs[start]
		}
		inputJSON, err := json.Marshal(input)
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		fields := translate.Fields{"encoding_format": json.RawMessage(`"float"`)}
		if dimensions > 0 {
			fields["dimensions"], err = json.Marshal(dimensions)
			if err != nil {
				inferenceError(w, err, request.Model)
				return
			}
		}
		body, err := translate.Finalize(fields, entry, translate.Fields{"input": inputJSON}, state.config.Upstream, token, state.catalog)
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		// The typed decoder requires floats even if trusted extra_body requested
		// another encoding. This is a fixed property of the translated endpoint.
		fields = nil
		if err = json.Unmarshal(body, &fields); err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		fields["encoding_format"] = json.RawMessage(`"float"`)
		body, err = json.Marshal(fields)
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		response, err := state.client.PostJSON(ctx, "embeddings", body, state.catalog, upstream.RequestInfo{RequestID: w.Header().Get("X-Request-Id"), UserAgent: "ollame/" + s.version}, int64(state.config.Limits.MaxUpstreamJSONBytes))
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		details.upstreamStatus = response.StatusCode
		details.callID = response.Header.Get("X-Litellm-Call-Id")
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			upstreamError(w, response, request.Model)
			response.Body.Close()
			return
		}
		if firstHeaders.IsZero() {
			firstHeaders = time.Now()
		}
		s.recordUse(entry, keepAlive)
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		batch, err := translate.DecodeEmbeddings(raw, end-start, dimensions, normalize, int64(state.config.Limits.MaxUpstreamJSONBytes))
		if err != nil {
			inferenceError(w, err, request.Model)
			return
		}
		if state.config.Compat.EstimateTokens {
			batch.EstimateUsage(inputs[start:end])
		}
		// Record each completed upstream batch with its own provenance, including
		// when a later batch or the aggregate validation fails. The final access
		// record may combine observed and estimated counts, but metrics must not.
		s.metrics.Usage(entry.Name, token, obs.TokenUsage{Prompt: batch.PromptTokens, Estimated: batch.Estimated})
		details.usageRecorded = true
		if err = collected.Add(batch); err != nil {
			inferenceError(w, err, request.Model)
			return
		}
	}
	vectors, err := collected.Finish()
	if err != nil {
		inferenceError(w, err, request.Model)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(time.Duration(state.config.Server.WriteStallTimeout)))
	if legacy {
		writeJSON(w, 200, map[string]any{"embedding": vectors[0]})
		return
	}
	result := map[string]any{"model": request.Model, "embeddings": vectors, "total_duration": time.Since(started).Nanoseconds(), "load_duration": firstHeaders.Sub(started).Nanoseconds()}
	if tokens, known := collected.Usage(); known {
		result["prompt_eval_count"] = tokens
	}
	writeJSON(w, 200, result)
}
