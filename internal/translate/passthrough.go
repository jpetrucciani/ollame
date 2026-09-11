package translate

import (
	"encoding/json"
	"fmt"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

// Passthrough validates the model and removes client routing/attribution fields.
// Alias prompts, options, and extra_body do not apply to provider-native requests.
func Passthrough(raw []byte, path string, cfg config.Upstream, exposed *catalog.Catalog, token string) ([]byte, catalog.Entry, error) {
	var fields Fields
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, catalog.Entry{}, fmt.Errorf("%w: invalid JSON request body", ErrInvalidRequest)
	}
	var model string
	if value, ok := fields["model"]; ok && json.Unmarshal(value, &model) != nil {
		return nil, catalog.Entry{}, fmt.Errorf("%w: model must be a string", ErrInvalidRequest)
	}
	entry, err := exposed.Resolve(model)
	if err != nil {
		return nil, catalog.Entry{}, err
	}
	for key := range fields {
		if routingKey(key) {
			delete(fields, key)
		}
	}
	// SDK extra_body normally merges into the top-level JSON object. An actual
	// nested extra_body would create a second, unvalidated routing option source.
	delete(fields, "extra_body")
	delete(fields, "metadata")
	delete(fields, "user")
	fields["model"], err = json.Marshal(entry.Target)
	if err != nil {
		return nil, catalog.Entry{}, err
	}
	if path == "chat/completions" || path == "completions" {
		fields["n"] = json.RawMessage(`1`)
	}
	if cfg.ForwardClientAsUser {
		fields["user"], err = json.Marshal(cfg.UserPrefix + token)
		if err != nil {
			return nil, catalog.Entry{}, err
		}
	}
	metadata := map[string]any{}
	if cfg.Flavor == "litellm" && len(cfg.Tags) > 0 {
		tags := append([]string{}, cfg.Tags...)
		metadata["tags"] = append(tags, "token:"+token)
	}
	fields["metadata"], err = json.Marshal(metadata)
	if err != nil {
		return nil, catalog.Entry{}, err
	}
	if !exposed.AllowsTarget(entry.Target) {
		return nil, catalog.Entry{}, catalog.ErrNotFound
	}
	encoded, err := json.Marshal(fields)
	return encoded, entry, err
}
