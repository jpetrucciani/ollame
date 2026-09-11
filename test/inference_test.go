package test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/stream"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

func TestRealInferenceTransport(t *testing.T) {
	baseURL := os.Getenv("OLLAME_TEST_UPSTREAM")
	if baseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("OLLAME_TEST_UPSTREAM required for real inference integration")
		}
		t.Skip("set OLLAME_TEST_UPSTREAM to the pinned real integration stack")
	}
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = baseURL
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := upstream.New(cfg.Upstream, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	models, _, err := client.Discover(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	exposed, _, err := catalog.Build(cfg, models, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("recordings/stream/request.json")
	if err != nil {
		t.Fatal(err)
	}
	info := upstream.RequestInfo{RequestID: "transport-integration", UserAgent: "ollame/test"}
	response, err := client.PostJSON(ctx, "chat/completions", raw, exposed, info, int64(cfg.Limits.MaxUpstreamJSONBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("upstream status %d", response.StatusCode)
	}
	decoder, err := stream.NewDecoder(response.Body, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	usage := false
	for {
		chunk, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
		}
		if chunk.Usage != nil {
			usage = true
		}
	}
	if strings.TrimSpace(strings.ToLower(content.String())) != "hello" || !usage {
		t.Fatalf("unexpected response: %q, usage=%v", content.String(), usage)
	}
	response.Body.Close()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["stream"] = json.RawMessage(`false`)
	delete(body, "stream_options")
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.PostJSON(ctx, "chat/completions", raw, exposed, info, 8)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if !errors.Is(err, upstream.ErrResponseTooLarge) || len(data) > 8 {
		t.Fatalf("body bound failed: bytes=%d err=%v", len(data), err)
	}
	requestCtx, stop := context.WithCancel(ctx)
	stop()
	if _, err := client.PostJSON(requestCtx, "chat/completions", raw, exposed, info, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	body["model"] = json.RawMessage(`"outside-exposed-set"`)
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PostJSON(ctx, "chat/completions", raw, exposed, info, 1024); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("target escaped: %v", err)
	}
	body["model"] = json.RawMessage(`"test-qwen3"`)
	body["fallbacks"] = json.RawMessage(`["outside-exposed-set"]`)
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PostJSON(ctx, "chat/completions", raw, exposed, info, 1024); !errors.Is(err, upstream.ErrInvalidRequest) {
		t.Fatalf("routing controls escaped: %v", err)
	}
}
