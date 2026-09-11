package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRealChatHTTP(t *testing.T) {
	baseURL := os.Getenv("OLLAME_TEST_UPSTREAM")
	if baseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("OLLAME_TEST_UPSTREAM required")
		}
		t.Skip("set OLLAME_TEST_UPSTREAM to the pinned real stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, mode := range []string{"always", "match"} {
		t.Run(mode, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "api.sock")
			daemon := exec.CommandContext(ctx, binary, "serve", "--config", "integration/ollame.toml", "--listen", "unix:"+socket, "--server-admin-listen=", "--upstream", baseURL, "--auth-mode", "disabled", "--upstream-stream-mode", mode, "--embed-batch-size", "1", "--compat-estimate-tokens=true", "--compat-think-style", "chat_template_kwargs", "--server-drain-delay", "0s", "--server-shutdown-grace", "1s")
			daemon.Env = []string{"PATH=" + os.Getenv("PATH")}
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = daemon.Process.Kill(); _ = daemon.Wait() }()
			client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}}
			defer client.CloseIdleConnections()
			ready := false
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
				response, err := client.Get("http://ollame/api/tags")
				if err == nil {
					var body struct {
						Models []json.RawMessage `json:"models"`
					}
					_ = json.NewDecoder(response.Body).Decode(&body)
					response.Body.Close()
					if len(body.Models) > 0 {
						ready = true
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !ready {
				t.Fatal("catalog did not become ready")
			}
			exercisePassthrough(t, client)
			for _, body := range []string{`{"model":"test-qwen3"}`, `{"name":"test-qwen3","stream":false}`} {
				response, err := client.Post("http://ollame/api/pull", "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 200 {
					t.Fatalf("pull status %d", response.StatusCode)
				}
				scanner := bufio.NewScanner(response.Body)
				var statuses []string
				for scanner.Scan() {
					var progress struct {
						Status string `json:"status"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
						t.Fatal(err)
					}
					statuses = append(statuses, progress.Status)
				}
				response.Body.Close()
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				want := "pulling manifest,verifying sha256 digest,writing manifest,success"
				if strings.Contains(body, `"stream":false`) {
					want = "success"
				}
				if strings.Join(statuses, ",") != want {
					t.Fatalf("wrong pull progress: %v", statuses)
				}
			}
			for _, rawMode := range []bool{false, true} {
				for _, streaming := range []bool{false, true} {
					body, err := json.Marshal(map[string]any{"model": "test-qwen3:latest", "prompt": "Reply with the single word hello.", "stream": streaming, "raw": rawMode, "think": false, "logprobs": true, "top_logprobs": 2, "options": map[string]any{"temperature": 0, "num_predict": 16}})
					if err != nil {
						t.Fatal(err)
					}
					response, err := client.Post("http://ollame/api/generate", "application/json", bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					if response.StatusCode != 200 {
						data, _ := io.ReadAll(response.Body)
						response.Body.Close()
						t.Fatalf("generate raw=%v stream=%v HTTP %d: %s", rawMode, streaming, response.StatusCode, data)
					}
					scanner := bufio.NewScanner(response.Body)
					var content strings.Builder
					done := false
					probabilities := 0
					for scanner.Scan() {
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
							t.Fatal(err)
						}
						for _, forbidden := range []string{"message", "context", "tool_calls", "error"} {
							if _, ok := fields[forbidden]; ok {
								t.Fatalf("unexpected generate field %s: %s", forbidden, scanner.Bytes())
							}
						}
						var text, model string
						if err := json.Unmarshal(fields["response"], &text); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(fields["model"], &model); err != nil {
							t.Fatal(err)
						}
						if model != "test-qwen3:latest" {
							t.Fatal("generate lost requested model")
						}
						if err := json.Unmarshal(fields["done"], &done); err != nil {
							t.Fatal(err)
						}
						if raw := fields["logprobs"]; len(raw) > 0 {
							var values []struct {
								Token string   `json:"token"`
								Score *float64 `json:"logprob"`
								Bytes []int    `json:"bytes"`
							}
							if err := json.Unmarshal(raw, &values); err != nil {
								t.Fatal(err)
							}
							for _, value := range values {
								if value.Score == nil {
									t.Fatal("missing token probability")
								}
								for _, b := range value.Bytes {
									if b < 0 || b > 255 {
										t.Fatal("invalid token byte")
									}
								}
							}
							probabilities += len(values)
						}
						content.WriteString(text)
					}
					response.Body.Close()
					if err := scanner.Err(); err != nil {
						t.Fatal(err)
					}
					if !done || content.Len() == 0 {
						t.Fatalf("incomplete generate raw=%v stream=%v", rawMode, streaming)
					}
					if probabilities == 0 {
						if !rawMode || mode == "match" && !streaming {
							t.Fatalf("missing expected logprobs raw=%v stream=%v", rawMode, streaming)
						}
						t.Log("pinned LiteLLM omits streaming completion logprobs; provider acceptance remains open")
					}
					if !rawMode && strings.TrimSpace(strings.ToLower(content.String())) != "hello" {
						t.Fatalf("generate chat translation changed content: %q", content.String())
					}
				}
			}
			for _, streaming := range []bool{true, false} {
				raw, err := os.ReadFile("recordings/stream/request.json")
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				body["model"], body["stream"], body["think"] = "test-qwen3:latest", streaming, false
				body["options"] = map[string]any{"temperature": 0, "num_predict": 64}
				raw, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Post("http://ollame/api/chat", "application/x-www-form-urlencoded", bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 200 {
					data, _ := io.ReadAll(response.Body)
					response.Body.Close()
					t.Fatalf("HTTP %d: %s", response.StatusCode, data)
				}
				var content strings.Builder
				done := false
				scanner := bufio.NewScanner(response.Body)
				for scanner.Scan() {
					var chunk struct {
						Model   string `json:"model"`
						Message struct {
							Content  string `json:"content"`
							Thinking string `json:"thinking"`
						} `json:"message"`
						Done         bool   `json:"done"`
						Error        string `json:"error"`
						EvalCount    *int   `json:"eval_count"`
						LoadDuration *int64 `json:"load_duration"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
						t.Fatal(err)
					}
					if chunk.Error != "" || chunk.Model != "test-qwen3:latest" || chunk.Message.Thinking != "" {
						t.Fatalf("bad chunk: %s", scanner.Bytes())
					}
					content.WriteString(chunk.Message.Content)
					if chunk.Done {
						done = true
						if chunk.EvalCount == nil {
							t.Fatal("missing usage")
						}
						if mode == "match" && !streaming && chunk.LoadDuration != nil {
							t.Fatal("misleading match timing")
						}
					}
				}
				response.Body.Close()
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if !done || strings.TrimSpace(strings.ToLower(content.String())) != "hello" {
					t.Fatalf("incomplete response %q", content.String())
				}
			}
			response, err := client.Get("http://ollame/api/ps")
			if err != nil {
				t.Fatal(err)
			}
			var ps struct {
				Models []json.RawMessage `json:"models"`
			}
			if err := json.NewDecoder(response.Body).Decode(&ps); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if len(ps.Models) != 1 {
				t.Fatal("successful inference not tracked")
			}
			toolsRaw, err := os.ReadFile("recordings/stream/tools.request.json")
			if err != nil {
				t.Fatal(err)
			}
			var toolRequest map[string]any
			if err := json.Unmarshal(toolsRaw, &toolRequest); err != nil {
				t.Fatal(err)
			}
			toolRequest["stream"], toolRequest["think"] = false, false
			toolRequest["options"] = map[string]any{"temperature": 0, "num_predict": 128}
			toolsRaw, err = json.Marshal(toolRequest)
			if err != nil {
				t.Fatal(err)
			}
			response, err = client.Post("http://ollame/api/chat", "application/json", bytes.NewReader(toolsRaw))
			if err != nil {
				t.Fatal(err)
			}
			var toolsResponse struct {
				Done    bool `json:"done"`
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments struct {
								City string `json:"city"`
							} `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			}
			if err := json.NewDecoder(response.Body).Decode(&toolsResponse); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 200 || !toolsResponse.Done || len(toolsResponse.Message.ToolCalls) != 1 {
				t.Fatalf("tool response failed: %+v, status=%d", toolsResponse, response.StatusCode)
			}
			call := toolsResponse.Message.ToolCalls[0]
			if call.Function.Name != "get_weather" || call.Function.Arguments.City != "Indianapolis" {
				t.Fatalf("wrong tool output: %+v", call)
			}
			response, err = client.Post("http://ollame/api/chat", "application/json", strings.NewReader(`{"model":"test-qwen3","keep_alive":0}`))
			if err != nil {
				t.Fatal(err)
			}
			var lifecycle struct {
				DoneReason string `json:"done_reason"`
			}
			if err := json.NewDecoder(response.Body).Decode(&lifecycle); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if lifecycle.DoneReason != "unload" {
				t.Fatal("local unload failed")
			}
			response, err = client.Get("http://ollame/api/ps")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewDecoder(response.Body).Decode(&ps); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if len(ps.Models) != 0 {
				t.Fatal("unload left model in ps")
			}
			exerciseEmbeddings(t, client)
			_ = daemon.Process.Signal(syscall.SIGTERM)
		})
	}
}

func exercisePassthrough(t *testing.T, client *http.Client) {
	t.Helper()
	response, err := client.Get("http://ollame/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || models.Object != "list" || len(models.Data) != 2 {
		t.Fatal("wrong provider catalog")
	}
	raw, err := os.ReadFile("recordings/stream/request.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["model"], body["stream"], body["n"] = "test-qwen3:latest", false, 99
	body["fallbacks"] = []string{"outside-exposed-set"}
	body["extra_body"] = map[string]any{"model": "outside-exposed-set"}
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Post("http://ollame/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var completion struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &completion); err != nil {
		t.Fatalf("invalid provider body: %s", data)
	}
	if response.StatusCode != 200 || completion.Model != "test-qwen3" || len(completion.Choices) != 1 || strings.TrimSpace(strings.ToLower(completion.Choices[0].Message.Content)) != "hello" {
		t.Fatalf("provider response changed or boundary failed: %s", data)
	}
	if response.Header.Get("X-Request-Id") == "" || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatal("missing passthrough response headers")
	}
	body["stream"] = true
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Post("http://ollame/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("passthrough SSE headers lost")
	}
	scanner := bufio.NewScanner(response.Body)
	finished := false
	var content strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			finished = true
			continue
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.Model != "test-qwen3" {
			t.Fatal("passthrough rewrote stream model")
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !finished || strings.TrimSpace(strings.ToLower(content.String())) != "hello" {
		t.Fatal("passthrough SSE changed content or terminator")
	}
}

func exerciseEmbeddings(t *testing.T, client *http.Client) {
	t.Helper()
	post := func(path, body string) map[string]json.RawMessage {
		t.Helper()
		response, err := client.Post("http://ollame"+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("%s HTTP %d: %s", path, response.StatusCode, raw)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	fields := post("/api/embed", `{"model":"test-nomic-embed:latest","input":["search_document: one","search_document: two","search_document: one"],"dimensions":8}`)
	var vectors [][]float64
	if err := json.Unmarshal(fields["embeddings"], &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 {
		t.Fatal("lost embedding batches")
	}
	for _, vector := range vectors {
		if len(vector) != 8 {
			t.Fatal("dimension truncation failed")
		}
		norm := 0.0
		for _, value := range vector {
			norm += value * value
		}
		if math.Abs(norm-1) > 1e-6 {
			t.Fatalf("embedding is not normalized: %g", norm)
		}
	}
	for i := range vectors[0] {
		if math.Abs(vectors[0][i]-vectors[2][i]) > 1e-6 {
			t.Fatal("batch order or repeated-input identity lost")
		}
	}
	var tokens int
	if err := json.Unmarshal(fields["prompt_eval_count"], &tokens); err != nil || tokens <= 0 {
		t.Fatal("missing batch usage")
	}
	// Compare the translated sum with actual provider-reported usage through
	// byte-preserving passthrough while estimation is enabled on the daemon.
	reported := 0
	for _, text := range []string{"search_document: one", "search_document: two", "search_document: one"} {
		body, err := json.Marshal(map[string]string{"model": "test-nomic-embed", "input": text})
		if err != nil {
			t.Fatal(err)
		}
		upstream := post("/v1/embeddings", string(body))
		var usage struct {
			PromptTokens *int `json:"prompt_tokens"`
		}
		if err := json.Unmarshal(upstream["usage"], &usage); err != nil || usage.PromptTokens == nil {
			t.Fatal("real provider usage unavailable for comparison")
		}
		reported += *usage.PromptTokens
	}
	if tokens != reported {
		t.Fatalf("estimation replaced provider usage: got %d, reported %d", tokens, reported)
	}

	fields = post("/api/embeddings", `{"model":"test-nomic-embed","prompt":"search_document: one"}`)
	var vector []float64
	if err := json.Unmarshal(fields["embedding"], &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector) != 768 || len(fields) != 1 {
		t.Fatal("legacy embedding shape changed")
	}
	fields = post("/api/embed", `{"model":"test-nomic-embed","input":[]}`)
	if string(fields["embeddings"]) != "[]" {
		t.Fatal("empty embedding array not local")
	}
	fields = post("/api/embeddings", `{"model":"test-nomic-embed","prompt":""}`)
	if string(fields["embedding"]) != "[]" {
		t.Fatal("empty legacy embedding not local")
	}
}
