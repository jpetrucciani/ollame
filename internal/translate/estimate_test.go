package translate

import (
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestInputTextBytes(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	translated, err := Chat(ChatRequest{Model: "allowed", Messages: []Message{{Role: "system", Content: "你好"}, {Role: "user", Content: "é"}}}, cfg, exposed, "test")
	if err != nil {
		t.Fatal(err)
	}
	size, err := InputTextBytes(translated.Body)
	if err != nil || size != 8 {
		t.Fatalf("UTF-8 input size = %d: %v", size, err)
	}
	cfg.Generate.FIM = "suffix"
	exposed, _, err = catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(GenerateRequest{Model: "allowed", Prompt: "ab", Suffix: "你好"}, cfg, exposed, "test")
	if err != nil {
		t.Fatal(err)
	}
	size, err = InputTextBytes(generated.Body)
	if err != nil || size != 8 {
		t.Fatalf("FIM size = %d: %v", size, err)
	}
	// Multimodal request content counts only text; a base64 payload must not
	// inflate text-token estimates. Tool arguments are decoded strings.
	size, err = InputTextBytes([]byte(`{"messages":[{"content":[{"type":"text","text":"é"},{"type":"image_url","image_url":{"url":"data:image/png;base64,ignored"}}],"reasoning_content":"abc","tool_calls":[{"function":{"arguments":"{}"}}]}]}`))
	if err != nil || size != 7 {
		t.Fatalf("multimodal size = %d: %v", size, err)
	}
}
