package translate

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestPassthroughBoundaryAndAttribution(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"allowed:latest","n":99,"messages":[{"role":"user","content":"fallbacks api_key"}],"fallbacks":["outside"],"litellm_params":{"model":"outside"},"extra_body":{"model":"outside"},"metadata":{"tags":["spoof"]},"user":"spoof","temperature":0.1234567890123456789}`)
	encoded, entry, err := Passthrough(raw, "chat/completions", cfg.Upstream, exposed, "ci")
	if err != nil {
		t.Fatal(err)
	}
	var fields Fields
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if entry.Target != "allowed" || string(fields["model"]) != `"allowed"` || string(fields["n"]) != "1" || string(fields["user"]) != `"ollame:ci"` {
		t.Fatalf("wrong protected fields: %s", encoded)
	}
	if string(fields["temperature"]) != "0.1234567890123456789" || string(fields["messages"]) != `[{"role":"user","content":"fallbacks api_key"}]` {
		t.Fatal("provider input changed")
	}
	for _, key := range []string{"fallbacks", "litellm_params", "extra_body"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("routing key survived: %s", key)
		}
	}
	if string(fields["metadata"]) != `{"tags":["ollame","token:ci"]}` {
		t.Fatal("client attribution survived")
	}
	if _, _, err := Passthrough([]byte(`{"model":"outside"}`), "completions", cfg.Upstream, exposed, "ci"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal("unexposed model accepted")
	}
}
