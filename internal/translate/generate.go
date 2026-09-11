package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

type GenerateRequest struct {
	Model           string          `json:"model"`
	Prompt          string          `json:"prompt"`
	Suffix          string          `json:"suffix,omitempty"`
	System          string          `json:"system,omitempty"`
	Template        string          `json:"template,omitempty"`
	Context         json.RawMessage `json:"context,omitempty"`
	Raw             bool            `json:"raw,omitempty"`
	Images          []string        `json:"images,omitempty"`
	Stream          *bool           `json:"stream,omitempty"`
	Format          json.RawMessage `json:"format,omitempty"`
	KeepAlive       json.RawMessage `json:"keep_alive,omitempty"`
	Options         Fields          `json:"options"`
	Think           json.RawMessage `json:"think,omitempty"`
	Truncate        *bool           `json:"truncate,omitempty"`
	Shift           *bool           `json:"shift,omitempty"`
	DebugRenderOnly bool            `json:"_debug_render_only,omitempty"`
	Logprobs        bool            `json:"logprobs,omitempty"`
	TopLogprobs     int             `json:"top_logprobs,omitempty"`
}

type GenerateResult struct {
	ChatResult
	Endpoint string
	Local    bool
	LossyRaw bool
}

func Generate(request GenerateRequest, cfg config.Config, exposed *catalog.Catalog, token string) (GenerateResult, error) {
	result := GenerateResult{Endpoint: "chat/completions"}
	entry, err := exposed.Resolve(request.Model)
	if err != nil {
		return result, err
	}
	result.Entry = entry
	result.DownstreamStream = request.Stream == nil || *request.Stream
	if request.DebugRenderOnly {
		return result, fmt.Errorf("%w: _debug_render_only is not supported by ollame", ErrInvalidRequest)
	}
	if request.Prompt == "" && len(request.Images) == 0 && request.Suffix == "" {
		result.Local = true
		return result, nil
	}
	fim, template := cfg.Generate.FIM, cfg.Generate.FIMTemplate
	if entry.Behavior.FIM != nil {
		fim = *entry.Behavior.FIM
	}
	if entry.Behavior.FIMTemplate != nil {
		template = *entry.Behavior.FIMTemplate
	}
	prompt := request.Prompt
	if request.Suffix != "" {
		result.Endpoint = "completions"
		switch fim {
		case "suffix":
		case "template":
			// Replace placeholders in the template once. Prompt text that happens
			// to contain another placeholder remains literal user input.
			prompt = strings.NewReplacer("{{prefix}}", request.Prompt, "{{suffix}}", request.Suffix).Replace(template)
		default:
			return result, fmt.Errorf("%w: %q does not support insert", ErrInvalidRequest, request.Model)
		}
	} else if request.Raw {
		switch cfg.Generate.Raw {
		case "completions":
			result.Endpoint = "completions"
		case "chat":
			result.LossyRaw = true
		default:
			return result, fmt.Errorf("%w: raw mode is not supported by ollame", ErrInvalidRequest)
		}
	}
	if result.Endpoint == "chat/completions" {
		messages := []Message{}
		if result.LossyRaw {
			entry.System = ""
		}
		if request.System != "" && !result.LossyRaw {
			messages = append(messages, Message{Role: "system", Content: request.System})
		}
		messages = append(messages, Message{Role: "user", Content: request.Prompt, Images: request.Images})
		result.ChatResult, err = chat(ChatRequest{Model: request.Model, Messages: messages, Stream: request.Stream, Format: request.Format, KeepAlive: request.KeepAlive, Options: request.Options, Think: request.Think, Truncate: request.Truncate, Shift: request.Shift, Logprobs: request.Logprobs, TopLogprobs: request.TopLogprobs}, cfg, exposed, entry, token)
		if err != nil {
			return result, err
		}
	} else {
		if len(request.Images) > 0 {
			return result, fmt.Errorf("%w: images are not supported in completions mode", ErrInvalidRequest)
		}
		if request.TopLogprobs < 0 || request.TopLogprobs > 20 {
			return result, fmt.Errorf("%w: top_logprobs must be between 0 and 20", ErrInvalidRequest)
		}
		body, dropped, err := Options(entry.Options, request.Options, entry, cfg.Compat.StrictOptions)
		if err != nil {
			return result, err
		}
		result.Dropped = append(result.Dropped, dropped...)
		if request.Suffix != "" && fim == "suffix" {
			body["suffix"], err = json.Marshal(request.Suffix)
			if err != nil {
				return result, err
			}
		}
		body["echo"] = json.RawMessage(`false`)
		if request.Logprobs {
			body["logprobs"], err = json.Marshal(request.TopLogprobs)
			if err != nil {
				return result, err
			}
		}
		result.UpstreamStream = cfg.Upstream.StreamMode == "always" || result.DownstreamStream
		protected := Fields{"n": json.RawMessage(`1`)}
		protected["prompt"], err = json.Marshal(prompt)
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
		if err != nil {
			return result, err
		}
		for key, present := range map[string]bool{"format": len(request.Format) > 0, "think": len(request.Think) > 0, "truncate": request.Truncate != nil, "shift": request.Shift != nil, "system": request.System != ""} {
			if present {
				result.Dropped = append(result.Dropped, Dropped{Field: key, Name: key})
			}
		}
	}
	if request.Template != "" {
		result.Dropped = append(result.Dropped, Dropped{Field: "template", Name: "template"})
	}
	if len(request.Context) > 0 {
		result.Dropped = append(result.Dropped, Dropped{Field: "context", Name: "context"})
	}
	return result, nil
}
