package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/translate"
)

// Exercise production translators and the real final-send guard. A canceled
// context prevents network traffic without substituting a transport or upstream.
func FuzzJSONSendBoundary(f *testing.F) {
	for route := uint8(0); route < 9; route++ {
		for _, name := range []string{"provider/private", "alias", "visible", "blocked", "a:b", "a-b", "ALIAS:latest", ""} {
			f.Add(name, "alias", uint8(0), route)
		}
	}
	f.Fuzz(func(t *testing.T, requested, alias string, filters, route uint8) {
		if len(requested) > 1024 || len(alias) > 128 {
			return
		}
		cfg := config.Defaults()
		cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
		includes := [][]string{{"*"}, {"provider/*"}, {"visible", "a*"}, {"?isible"}}
		cfg.Models.Include = includes[int(filters)%len(includes)]
		cfg.Models.Exclude = []string{"blocked"}
		if filters&4 != 0 {
			cfg.Models.Exclude = append(cfg.Models.Exclude, "provider/*")
		}
		if filters&8 != 0 {
			cfg.Models.Modes = []string{"embedding"}
		}
		cfg.Models.Aliases = []config.Alias{{Name: alias, Target: "provider/private", HideTarget: filters&16 != 0}, {Name: "escape", Target: "blocked"}}
		exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "provider/private", Info: catalog.Info{Mode: "chat"}}, {ID: "visible", Info: catalog.Info{Mode: "embedding"}}, {ID: "blocked"}, {ID: "a:b"}, {ID: "a-b"}}, nil, time.Unix(1, 0))
		if err != nil {
			return
		} // Invalid generated aliases are rejected configuration.
		targets := map[string]bool{}
		for _, entry := range exposed.Entries() {
			targets[entry.Target] = true
		}
		if targets["blocked"] {
			t.Fatal("alias restored excluded target")
		}
		client, err := New(cfg.Upstream, "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		paths := []string{"chat/completions", "completions", "embeddings", "responses", "responses/compact", "messages"}
		path := paths[int(route)%len(paths)]
		raw, err := json.Marshal(map[string]any{"model": requested, "messages": []map[string]string{{"role": "user", "content": "hello"}}, "fallbacks": []string{"blocked"}, "api_key": "untrusted", "litellm_params": map[string]string{"model": "blocked"}})
		if err != nil {
			t.Fatal(err)
		}
		var encoded []byte
		switch route % 9 {
		case 6:
			result, translateErr := translate.Chat(translate.ChatRequest{Model: requested, Messages: []translate.Message{{Role: "user", Content: "hello"}}}, cfg, exposed, "ci")
			encoded, err, path = result.Body, translateErr, "chat/completions"
		case 7:
			result, translateErr := translate.Generate(translate.GenerateRequest{Model: requested, Prompt: "hello", Raw: filters&32 != 0}, cfg, exposed, "ci")
			encoded, err, path = result.Body, translateErr, result.Endpoint
		case 8:
			var entry catalog.Entry
			entry, err = exposed.Resolve(requested)
			if err == nil {
				encoded, err = translate.Finalize(translate.Fields{"fallbacks": json.RawMessage(`["blocked"]`)}, entry, translate.Fields{"input": json.RawMessage(`"hello"`)}, cfg.Upstream, "ci", exposed)
			}
			path = "embeddings"
		default:
			encoded, _, err = translate.Passthrough(raw, path, cfg.Upstream, exposed, "ci")
		}
		if err == nil {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			if !targets[body.Model] {
				t.Fatalf("%s produced model outside E: %q", path, body.Model)
			}
			_, err = client.PostJSON(ctx, path, encoded, exposed, RequestInfo{}, 4096)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("valid translated request rejected by final guard: %v", err)
			}
		} else if len(encoded) != 0 {
			t.Fatal("failed translation returned sendable bytes")
		}
		// Independently bypass name resolution to challenge the final guard itself.
		candidate, _ := json.Marshal(map[string]string{"model": requested})
		_, err = client.PostJSON(ctx, path, candidate, exposed, RequestInfo{}, 4096)
		if targets[requested] {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("listed target rejected: %v", err)
			}
		} else if !errors.Is(err, catalog.ErrNotFound) {
			t.Fatalf("out-of-set target reached transport: %v", err)
		}
	})
}

func FuzzMultipartSendBoundary(f *testing.F) {
	for _, name := range []string{"alias", "provider/private", "blocked", "visible", ""} {
		for _, flags := range []uint8{0, 1, 2, 4, 8} {
			f.Add(name, []byte("audio\x00bytes"), flags)
		}
	}
	f.Fuzz(func(t *testing.T, requested string, file []byte, flags uint8) {
		if len(requested) > 1024 || len(file) > 4096 {
			return
		}
		cfg := config.Defaults()
		cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
		cfg.Models.Exclude = []string{"blocked"}
		if flags&1 != 0 {
			cfg.Models.Exclude = append(cfg.Models.Exclude, "provider/*")
		}
		cfg.Models.Aliases = []config.Alias{{Name: "alias", Target: "provider/private", HideTarget: flags&2 != 0}, {Name: "escape", Target: "blocked"}}
		exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "provider/private"}, {ID: "visible"}, {ID: "blocked"}}, nil, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		writer := multipart.NewWriter(&raw)
		// A stable boundary makes arbitrary file payloads reproducible under fuzzing.
		if err := writer.SetBoundary("ollame-fuzz-boundary"); err != nil {
			t.Fatal(err)
		}
		if flags&8 == 0 {
			if err := writer.WriteField("model", requested); err != nil {
				t.Fatal(err)
			}
		}
		if flags&4 != 0 {
			if err := writer.WriteField("model", requested); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"fallbacks", "api_key", "litellm_params", "extra_body"} {
			if err := writer.WriteField(key, "blocked"); err != nil {
				t.Fatal(err)
			}
		}
		part, err := writer.CreateFormFile("file", "audio.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		encoded, contentType, _, err := translate.Multipart(raw.Bytes(), writer.FormDataContentType(), 16384, cfg.Upstream, exposed, "ci")
		if err != nil {
			if len(encoded) != 0 {
				t.Fatal("failed multipart translation returned sendable bytes")
			}
			return
		}
		// Check the actual encoded model part, not the translator's returned entry.
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(bytes.NewReader(encoded), params["boundary"])
		models, files := 0, 0
		for {
			part, err := reader.NextRawPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			value, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			if forbiddenRoutingKey(part.FormName()) {
				t.Fatal("multipart retained routing control")
			}
			switch part.FormName() {
			case "model":
				models++
				allowed := false
				for _, entry := range exposed.Entries() {
					allowed = allowed || entry.Target == string(value)
				}
				if !allowed || string(value) == "blocked" {
					t.Fatal("encoded multipart model escaped E")
				}
			case "file":
				files++
				if !bytes.Equal(value, file) {
					t.Fatal("file bytes changed")
				}
			}
		}
		if models != 1 || files != 1 {
			t.Fatal("ambiguous multipart output")
		}
		client, err := New(cfg.Upstream, "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = client.PostMultipart(ctx, encoded, contentType, exposed, RequestInfo{}, 16384, 16384)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("rewritten multipart rejected at send boundary: %v", err)
		}
	})
}
