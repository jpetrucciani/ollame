package translate

import (
	"encoding/json"
	"fmt"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

// ChatRequest retains raw schemas, options, and tool definitions while decoding
// the documented scalar inputs. KeepAlive is consumed by lifecycle handling.
type ChatRequest struct {
	Model           string            `json:"model"`
	Messages        []Message         `json:"messages"`
	Stream          *bool             `json:"stream,omitempty"`
	Format          json.RawMessage   `json:"format,omitempty"`
	KeepAlive       json.RawMessage   `json:"keep_alive,omitempty"`
	Tools           []json.RawMessage `json:"tools,omitempty"`
	Options         Fields            `json:"options"`
	Think           json.RawMessage   `json:"think,omitempty"`
	Truncate        *bool             `json:"truncate,omitempty"`
	Shift           *bool             `json:"shift,omitempty"`
	DebugRenderOnly bool              `json:"_debug_render_only,omitempty"`
	Logprobs        bool              `json:"logprobs,omitempty"`
	TopLogprobs     int               `json:"top_logprobs,omitempty"`
}

type ChatResult struct {
	Body             []byte
	Entry            catalog.Entry
	DownstreamStream bool
	UpstreamStream   bool
	ThinkingVisible  bool
	ThinkingRelaxed  bool
	Dropped          []Dropped
	Synthesized      int
}

// Chat assembles an inference request against the request's captured catalog.
// Empty histories belong to local lifecycle handling and never reach upstream.
func Chat(request ChatRequest, cfg config.Config, exposed *catalog.Catalog, token string) (ChatResult, error) {
	entry, err := exposed.Resolve(request.Model)
	if err != nil {
		return ChatResult{}, err
	}
	return chat(request, cfg, exposed, entry, token)
}

func chat(request ChatRequest, cfg config.Config, exposed *catalog.Catalog, entry catalog.Entry, token string) (ChatResult, error) {
	result := ChatResult{DownstreamStream: request.Stream == nil || *request.Stream}
	if request.DebugRenderOnly {
		return result, fmt.Errorf("%w: _debug_render_only is not supported by ollame", ErrInvalidRequest)
	}
	if len(request.Messages) == 0 {
		return result, fmt.Errorf("%w: empty history requires local lifecycle handling", ErrInvalidRequest)
	}
	result.Entry = entry
	if request.TopLogprobs < 0 || request.TopLogprobs > 20 {
		return result, fmt.Errorf("%w: top_logprobs must be between 0 and 20", ErrInvalidRequest)
	}
	if len(request.Tools) > 0 && cfg.Compat.StrictCapabilities && entry.Knowledge["tools"] == catalog.No {
		return result, fmt.Errorf("%w: model does not support tools", ErrInvalidRequest)
	}
	body, dropped, err := Options(entry.Options, request.Options, entry, cfg.Compat.StrictOptions)
	if err != nil {
		return result, err
	}
	result.Dropped = append(result.Dropped, dropped...)
	messages, err := Messages(request.Messages, entry, cfg)
	if err != nil {
		return result, err
	}
	result.Dropped = append(result.Dropped, messages.Dropped...)
	result.Synthesized = messages.Synthesized
	tools, dropped, err := Tools(request.Tools)
	if err != nil {
		return result, err
	}
	result.Dropped = append(result.Dropped, dropped...)
	if len(tools) > 0 {
		body["tools"], err = json.Marshal(tools)
		if err != nil {
			return result, err
		}
	}
	format, err := Format(request.Format, cfg.Compat.JSONSchemaStrict)
	if err != nil {
		return result, err
	}
	if len(format) > 0 {
		body["response_format"] = format
	}
	var think any
	if len(request.Think) > 0 {
		if err := json.Unmarshal(request.Think, &think); err != nil {
			return result, fmt.Errorf("%w: invalid think value", ErrInvalidRequest)
		}
	}
	thinking, visible, relaxed, err := Thinking(think, entry, cfg.Compat)
	if err != nil {
		return result, err
	}
	result.ThinkingVisible, result.ThinkingRelaxed = visible, relaxed
	for key, value := range thinking {
		body[key] = value
	}
	if request.Logprobs {
		body["logprobs"] = json.RawMessage(`true`)
		body["top_logprobs"], err = json.Marshal(request.TopLogprobs)
		if err != nil {
			return result, err
		}
	}
	if request.Truncate != nil {
		result.Dropped = append(result.Dropped, Dropped{Field: "truncate", Name: "truncate"})
	}
	if request.Shift != nil {
		result.Dropped = append(result.Dropped, Dropped{Field: "shift", Name: "shift"})
	}
	result.UpstreamStream = cfg.Upstream.StreamMode == "always" || result.DownstreamStream
	protected := Fields{"n": json.RawMessage(`1`)}
	protected["messages"], err = json.Marshal(messages.Messages)
	if err != nil {
		return result, err
	}
	protected["stream"], err = json.Marshal(result.UpstreamStream)
	if err != nil {
		return result, err
	}
	if result.UpstreamStream {
		protected["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	result.Body, err = Finalize(body, entry, protected, cfg.Upstream, token, exposed)
	return result, err
}
