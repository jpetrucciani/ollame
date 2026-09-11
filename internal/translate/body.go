package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

// Finalize applies trusted extras and drops, then restores the fields owned by
// request translation. The caller must check the same catalog again at send time.
func Finalize(translated Fields, entry catalog.Entry, protected Fields, cfg config.Upstream, token string, exposed *catalog.Catalog) ([]byte, error) {
	if !exposed.AllowsTarget(entry.Target) {
		return nil, catalog.ErrNotFound
	}
	body := cloneFields(translated)
	for key, value := range entry.Behavior.ExtraBody {
		if protectedKey(key) {
			return nil, fmt.Errorf("%w: extra_body touches protected field %s", ErrInvalidRequest, key)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid extra_body", ErrInvalidRequest)
		}
		merged, err := mergeJSON(body[key], raw)
		if err != nil {
			return nil, err
		}
		body[key] = merged
	}
	for _, key := range entry.Behavior.DropParams {
		delete(body, key)
	}
	for key := range body {
		if protectedKey(key) || routingKey(key) {
			delete(body, key)
		}
	}
	for key, value := range protected {
		if !protectedKey(key) {
			return nil, fmt.Errorf("%w: unexpected protected field", ErrInvalidRequest)
		}
		body[key] = bytes.Clone(value)
	}
	model, err := json.Marshal(entry.Target)
	if err != nil {
		return nil, err
	}
	body["model"] = model
	if _, applicable := protected["n"]; applicable {
		body["n"] = json.RawMessage(`1`)
	}
	if cfg.ForwardClientAsUser {
		user, err := json.Marshal(cfg.UserPrefix + token)
		if err != nil {
			return nil, err
		}
		body["user"] = user
	} else {
		delete(body, "user")
	}
	if cfg.Flavor == "litellm" && len(cfg.Tags) > 0 {
		metadata := Fields{}
		if raw := body["metadata"]; len(raw) > 0 {
			if err = json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
				return nil, fmt.Errorf("%w: metadata must be an object", ErrInvalidRequest)
			}
		}
		tags := []string{}
		if raw := metadata["tags"]; len(raw) > 0 {
			if err = json.Unmarshal(raw, &tags); err != nil {
				return nil, fmt.Errorf("%w: metadata tags must be strings", ErrInvalidRequest)
			}
		}
		tags = append(tags, cfg.Tags...)
		tags = append(tags, "token:"+token)
		metadata["tags"], err = json.Marshal(tags)
		if err != nil {
			return nil, err
		}
		body["metadata"], err = json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
	}
	// Even a protected model supplied by a caller cannot replace the resolved ID.
	return json.Marshal(body)
}
func protectedKey(key string) bool {
	switch key {
	case "model", "messages", "prompt", "input", "stream", "stream_options", "n", "user":
		return true
	default:
		return false
	}
}
func routingKey(key string) bool {
	if strings.HasPrefix(key, "litellm_") {
		return true
	}
	switch key {
	case "fallbacks", "context_window_fallback_dict", "api_base", "base_url", "api_key", "mock_response":
		return true
	default:
		return false
	}
}
func cloneFields(fields Fields) Fields {
	copy := Fields{}
	for key, value := range fields {
		copy[key] = bytes.Clone(value)
	}
	return copy
}
func mergeJSON(base, update json.RawMessage) (json.RawMessage, error) {
	var left, right Fields
	if len(base) > 0 && json.Unmarshal(base, &left) == nil && left != nil && json.Unmarshal(update, &right) == nil && right != nil {
		for key, value := range right {
			merged, err := mergeJSON(left[key], value)
			if err != nil {
				return nil, err
			}
			left[key] = merged
		}
		return json.Marshal(left)
	}
	if !json.Valid(update) {
		return nil, fmt.Errorf("%w: invalid JSON merge", ErrInvalidRequest)
	}
	return bytes.Clone(update), nil
}

// Tools filters unsupported tool types while retaining function definitions as
// raw JSON, including property order and provider-supported schema extensions.
func Tools(input []json.RawMessage) ([]json.RawMessage, []Dropped, error) {
	output := make([]json.RawMessage, 0, len(input))
	dropped := []Dropped{}
	for _, raw := range input {
		var tool struct {
			Type     string          `json:"type"`
			Function json.RawMessage `json:"function"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			return nil, nil, fmt.Errorf("%w: invalid tool definition", ErrInvalidRequest)
		}
		if tool.Type != "function" {
			dropped = append(dropped, Dropped{Field: "tool_type", Name: "tools"})
			continue
		}
		function := bytes.TrimSpace(tool.Function)
		if len(function) == 0 || function[0] != '{' || !json.Valid(function) {
			return nil, nil, fmt.Errorf("%w: invalid function definition", ErrInvalidRequest)
		}
		output = append(output, bytes.Clone(raw))
	}
	return output, dropped, nil
}
