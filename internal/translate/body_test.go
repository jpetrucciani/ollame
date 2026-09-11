package translate

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestFinalizeProtectedFieldsAndRoutingControls(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := exposed.Resolve("allowed")
	if err != nil {
		t.Fatal(err)
	}
	entry.Behavior.ExtraBody = map[string]any{"metadata": map[string]any{"custom": "value", "tags": []string{"extra"}}, "temperature": 0.9}
	entry.Behavior.DropParams = []string{"model", "stream", "user", "temperature"}
	translated := Fields{"model": json.RawMessage(`"outside"`), "fallbacks": json.RawMessage(`["outside"]`), "api_key": json.RawMessage(`"secret"`), "litellm_override": json.RawMessage(`true`)}
	protected := Fields{"model": json.RawMessage(`"also-outside"`), "stream": json.RawMessage(`true`), "n": json.RawMessage(`1`), "messages": json.RawMessage(`[{"role":"user","content":"api_key is plain prompt text"}]`)}
	protected["n"] = json.RawMessage(`99`)
	raw, err := Finalize(translated, entry, protected, cfg.Upstream, "ci", exposed)
	if err != nil {
		t.Fatal(err)
	}
	var body Fields
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if string(body["model"]) != `"allowed"` || string(body["stream"]) != "true" || string(body["user"]) != `"ollame:ci"` {
		t.Fatalf("protected fields lost: %s", raw)
	}
	if string(body["n"]) != "1" {
		t.Fatalf("multiple choices allowed: %s", body["n"])
	}
	for _, key := range []string{"fallbacks", "api_key", "litellm_override", "temperature"} {
		if _, ok := body[key]; ok {
			t.Errorf("unexpected key %s", key)
		}
	}
	var metadata struct {
		Custom string   `json:"custom"`
		Tags   []string `json:"tags"`
	}
	if err = json.Unmarshal(body["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Custom != "value" || len(metadata.Tags) != 3 || metadata.Tags[2] != "token:ci" {
		t.Fatal("metadata merge failed")
	}
	entry.Target = "outside"
	if _, err = Finalize(nil, entry, protected, cfg.Upstream, "ci", exposed); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal("target escaped exposed set")
	}
}
func TestToolsPreserveDefinitions(t *testing.T) {
	input := []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"f","parameters":{"z":9007199254740993,"a":1}},"unknown":true}`), json.RawMessage(`{"type":"other"}`)}
	output, dropped, err := Tools(input)
	if err != nil || len(output) != 1 || len(dropped) != 1 {
		t.Fatal("tool filtering failed")
	}
	if string(output[0]) != string(input[0]) {
		t.Fatal("tool definition rewritten")
	}
	input[0][0] = 'x'
	if output[0][0] != '{' {
		t.Fatal("definition aliases input")
	}
}
