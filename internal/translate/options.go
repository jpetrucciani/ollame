// Package translate contains pure transformations of Ollama request fields.
package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

var ErrInvalidRequest = errors.New("invalid request")

// Fields preserves nested JSON number precision, member order, and unknown
// schema properties until the final outgoing body is encoded.
type Fields map[string]json.RawMessage

type Dropped struct {
	Field string
	Name  string
}

func Options(alias map[string]any, request Fields, entry catalog.Entry, strict bool) (Fields, []Dropped, error) {
	effective := Fields{}
	for key, value := range alias {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid alias option", ErrInvalidRequest)
		}
		effective[key] = raw
	}
	for key, value := range request {
		effective[key] = bytes.Clone(value)
	}
	result := Fields{}
	dropped := []Dropped{}
	keys := make([]string, 0, len(effective))
	for key := range effective {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := effective[key]
		target := key
		switch key {
		case "temperature", "top_p", "presence_penalty", "frequency_penalty":
			var number json.Number
			if err := json.Unmarshal(value, &number); err != nil || !numericLiteral(value) {
				return nil, nil, invalidOption(key)
			}
			if _, err := number.Float64(); err != nil {
				return nil, nil, invalidOption(key)
			}
		case "seed", "num_predict", "top_k":
			var number int64
			if err := json.Unmarshal(value, &number); err != nil || !numericLiteral(value) {
				return nil, nil, invalidOption(key)
			}
			if key == "num_predict" {
				if number <= 0 {
					continue
				}
				target = "max_tokens"
				if entry.Behavior.MaxTokensField != nil {
					target = *entry.Behavior.MaxTokensField
				}
			}
			if key == "top_k" && !enabled(entry.Behavior.ForwardSamplerExtras) {
				dropped = append(dropped, drop(key))
				continue
			}
		case "min_p", "typical_p", "repeat_penalty":
			if !enabled(entry.Behavior.ForwardSamplerExtras) {
				dropped = append(dropped, drop(key))
				continue
			}
			var number json.Number
			if err := json.Unmarshal(value, &number); err != nil || !numericLiteral(value) {
				return nil, nil, invalidOption(key)
			}
			if _, err := number.Float64(); err != nil {
				return nil, nil, invalidOption(key)
			}
			if key == "repeat_penalty" {
				target = "repetition_penalty"
			}
		case "stop":
			var text string
			if json.Unmarshal(value, &text) == nil && !bytes.Equal(value, []byte("null")) {
				encoded, err := json.Marshal([]string{text})
				if err != nil {
					return nil, nil, err
				}
				value = encoded
			} else {
				var values []string
				if err := json.Unmarshal(value, &values); err != nil || values == nil {
					return nil, nil, invalidOption(key)
				}
			}
		default:
			item := drop(key)
			if item.Field == "other" && strict {
				return nil, nil, fmt.Errorf("%w: unknown option: %s", ErrInvalidRequest, key)
			}
			dropped = append(dropped, item)
			continue
		}
		result[target] = bytes.Clone(value)
	}
	return result, dropped, nil
}
func invalidOption(name string) error {
	return fmt.Errorf("%w: invalid option: %s", ErrInvalidRequest, name)
}
func enabled(value *bool) bool { return value != nil && *value }

// MetricField maps client option names into the fixed metric-label vocabulary.
func MetricField(name string) string {
	switch name {
	case "temperature", "top_p", "seed", "stop", "presence_penalty", "frequency_penalty", "num_predict", "top_k", "min_p", "typical_p", "repeat_penalty", "num_ctx", "num_keep", "repeat_last_n", "num_batch", "num_gpu", "main_gpu", "use_mmap", "num_thread", "draft_num_predict", "mirostat", "mirostat_eta", "mirostat_tau", "tfs_z", "penalize_newline", "numa", "low_vram", "f16_kv", "vocab_only", "use_mlock":
		return name
	}
	return "other"
}
func drop(name string) Dropped {
	field := MetricField(name)
	if len(name) > 64 {
		name = strings.ToValidUTF8(name[:64], "")
	}
	return Dropped{Field: field, Name: name}
}

func Format(raw json.RawMessage, strict bool) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte(`""`)) {
		return nil, nil
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		if mode == "json" {
			return json.RawMessage(`{"type":"json_object"}`), nil
		}
		return nil, fmt.Errorf("%w: invalid format", ErrInvalidRequest)
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid format", ErrInvalidRequest)
	}
	return json.Marshal(struct {
		Type   string `json:"type"`
		Schema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict bool            `json:"strict"`
		} `json:"json_schema"`
	}{Type: "json_schema", Schema: struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict bool            `json:"strict"`
	}{Name: "ollama_format", Schema: bytes.Clone(raw), Strict: strict}})
}

// Thinking returns mapped parameters, effective reasoning visibility, and a
// known-no relaxation warning. Nil requests inherit the alias's configured value.
func Thinking(request any, entry catalog.Entry, cfg config.Compat) (Fields, bool, bool, error) {
	value := request
	if value == nil {
		value = entry.Think
	}
	if !config.ValidThink(value) {
		return nil, false, false, fmt.Errorf("%w: invalid think value", ErrInvalidRequest)
	}
	fields := Fields{}
	visible := true
	if b, ok := value.(bool); ok {
		visible = b
	}
	if value == nil {
		return fields, visible, false, nil
	}
	if visible && entry.Knowledge["thinking"] == catalog.No {
		if cfg.StrictCapabilities {
			return nil, visible, false, fmt.Errorf("%w: model does not support thinking", ErrInvalidRequest)
		}
		return fields, visible, true, nil
	}
	style := cfg.ThinkStyle
	if entry.Behavior.ThinkStyle != nil {
		style = *entry.Behavior.ThinkStyle
	}
	switch style {
	case "none":
		return fields, visible, false, nil
	case "chat_template_kwargs":
		raw, err := json.Marshal(map[string]bool{"enable_thinking": visible})
		if err != nil {
			return nil, visible, false, err
		}
		fields["chat_template_kwargs"] = raw
	case "reasoning_effort":
		effort := cfg.ThinkOn
		if entry.Behavior.ThinkOn != nil {
			effort = *entry.Behavior.ThinkOn
		}
		if level, ok := value.(string); ok {
			effort = level
			if level == "max" {
				effort = cfg.ThinkMax
				if entry.Behavior.ThinkMax != nil {
					effort = *entry.Behavior.ThinkMax
				}
			}
		}
		if !visible {
			effort = cfg.ThinkOff
			if entry.Behavior.ThinkOff != nil {
				effort = *entry.Behavior.ThinkOff
			}
			if effort == "omit" {
				return fields, false, false, nil
			}
		}
		raw, err := json.Marshal(effort)
		if err != nil {
			return nil, visible, false, err
		}
		fields["reasoning_effort"] = raw
	default:
		return nil, visible, false, fmt.Errorf("%w: invalid think style", ErrInvalidRequest)
	}
	return fields, visible, false, nil
}

func numericLiteral(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9')
}
