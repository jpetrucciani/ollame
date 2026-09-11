package translate

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestGenerateModesAndBoundary(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	request := GenerateRequest{Model: "allowed", Prompt: "prefix {{suffix}}", Suffix: "tail {{prefix}}"}
	if _, err := Generate(request, cfg, exposed, "test"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("FIM off accepted suffix")
	}
	for _, fim := range []string{"suffix", "template"} {
		cfg.Generate.FIM, cfg.Generate.FIMTemplate = fim, "PRE{{prefix}}MID{{suffix}}END"
		exposed, _, err = catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		result, err := Generate(request, cfg, exposed, "test")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Model    string          `json:"model"`
			Prompt   string          `json:"prompt"`
			Suffix   string          `json:"suffix"`
			Messages json.RawMessage `json:"messages"`
			Stream   bool            `json:"stream"`
			N        int             `json:"n"`
		}
		if err := json.Unmarshal(result.Body, &body); err != nil {
			t.Fatal(err)
		}
		if result.Endpoint != "completions" || !exposed.AllowsTarget(body.Model) || body.Messages != nil || !body.Stream || body.N != 1 {
			t.Fatal("incorrect completions request")
		}
		if fim == "suffix" && (body.Prompt != request.Prompt || body.Suffix != request.Suffix) {
			t.Fatal("suffix request changed")
		}
		if fim == "template" && (body.Prompt != "PREprefix {{suffix}}MIDtail {{prefix}}END" || body.Suffix != "") {
			t.Fatalf("recursive template substitution: %s", body.Prompt)
		}
	}
	request.Suffix, request.Raw = "", true
	for _, mode := range []string{"completions", "chat", "error"} {
		cfg.Generate.Raw = mode
		result, err := Generate(request, cfg, exposed, "test")
		if mode == "error" {
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatal("raw error mode accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if (result.Endpoint == "chat/completions") != (mode == "chat") || result.LossyRaw != (mode == "chat") {
			t.Fatal("raw mode selection failed")
		}
	}
	request.Model = "outside"
	if _, err := Generate(request, cfg, exposed, "test"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal("unexposed target accepted")
	}
}

func TestGenerateLocalAndImageRejection(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Generate(GenerateRequest{Model: "allowed"}, cfg, exposed, "test")
	if err != nil || !result.Local || len(result.Body) != 0 {
		t.Fatal("empty generate must be local")
	}
	_, err = Generate(GenerateRequest{Model: "allowed", Prompt: "hello", Raw: true, Images: []string{"AA=="}}, cfg, exposed, "test")
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("completions accepted images")
	}
}
