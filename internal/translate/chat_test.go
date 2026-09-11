package translate

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestChatRecordedInputStreamingModes(t *testing.T) {
	raw, err := os.ReadFile("../../test/recordings/stream/request.json")
	if err != nil {
		t.Fatal(err)
	}
	var request ChatRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: request.Model}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"always", "match"} {
		for _, stream := range []*bool{nil, new(false), new(true)} {
			cfg.Upstream.StreamMode = mode
			request.Stream = stream
			result, err := Chat(request, cfg, exposed, "recording")
			if err != nil {
				t.Fatal(err)
			}
			wantDownstream := stream == nil || *stream
			wantUpstream := mode == "always" || wantDownstream
			if result.DownstreamStream != wantDownstream || result.UpstreamStream != wantUpstream {
				t.Fatal("incorrect streaming mode")
			}
			var body struct {
				Model         string          `json:"model"`
				Stream        bool            `json:"stream"`
				StreamOptions json.RawMessage `json:"stream_options"`
				Messages      []OpenAIMessage `json:"messages"`
				N             int             `json:"n"`
			}
			if err := json.Unmarshal(result.Body, &body); err != nil {
				t.Fatal(err)
			}
			if !exposed.AllowsTarget(body.Model) || body.Stream != wantUpstream || (len(body.StreamOptions) > 0) != wantUpstream || body.N != 1 {
				t.Fatalf("invalid request: %s", result.Body)
			}
			if len(body.Messages) != len(request.Messages) {
				t.Fatal("lost recorded history")
			}
			for i, message := range body.Messages {
				var content string
				if err := json.Unmarshal(message.Content, &content); err != nil {
					t.Fatal(err)
				}
				if content != request.Messages[i].Content || message.Role != request.Messages[i].Role {
					t.Fatal("changed recorded message")
				}
			}
		}
	}
	request.Model = "outside-exposed-set"
	if _, err := Chat(request, cfg, exposed, "recording"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("unexpected resolution: %v", err)
	}
}

func TestChatRejectsInvalidInputs(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ChatRequest){
		func(r *ChatRequest) { r.DebugRenderOnly = true },
		func(r *ChatRequest) { r.Messages = nil },
		func(r *ChatRequest) { r.TopLogprobs = 21 },
		func(r *ChatRequest) { r.Think = json.RawMessage(`42`) },
		func(r *ChatRequest) { r.Options = Fields{"seed": json.RawMessage(" null ")} },
	} {
		request := ChatRequest{Model: "allowed", Messages: []Message{{Role: "user", Content: "hello"}}}
		change(&request)
		if _, err := Chat(request, cfg, exposed, "test"); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("unexpected validation result: %v", err)
		}
	}
}
