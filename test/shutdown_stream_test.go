package test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRealShutdownStreamErrors(t *testing.T) {
	upstreamURL := os.Getenv("OLLAME_TEST_UPSTREAM")
	if upstreamURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("OLLAME_TEST_UPSTREAM required")
		}
		t.Skip("requires real LiteLLM stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, route := range []string{"/api/chat", "/v1/chat/completions"} {
		t.Run(route, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "api.sock")
			adminListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			adminAddress := adminListener.Addr().String()
			if err := adminListener.Close(); err != nil {
				t.Fatal(err)
			}
			adminClient := &http.Client{Timeout: time.Second}
			defer adminClient.CloseIdleConnections()
			command := exec.CommandContext(ctx, binary, "serve", "--listen", "unix:"+socket, "--server-admin-listen", adminAddress, "--upstream", upstreamURL, "--auth-mode", "disabled", "--server-drain-delay", "0s", "--server-shutdown-grace", "1ms")
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			logPath := filepath.Join(t.TempDir(), "access.jsonl")
			logs, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Close()
			command.Stdout, command.Stderr = logs, logs
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- command.Wait() }()
			defer func() { _ = command.Process.Kill() }()
			client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}}
			defer client.CloseIdleConnections()
			for deadline := time.Now().Add(10 * time.Second); ; {
				response, err := client.Get("http://ollame/api/tags")
				if err == nil {
					var tags struct {
						Models []json.RawMessage `json:"models"`
					}
					decodeErr := json.NewDecoder(response.Body).Decode(&tags)
					response.Body.Close()
					if decodeErr == nil && len(tags.Models) > 0 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("catalog did not load")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if route == "/api/chat" {
				response, err := client.Post("http://ollame/api/chat", "application/json", strings.NewReader(`{"model":"test-qwen3","stream":false,"think":false,"messages":[{"role":"user","content":"Reply hello."}],"options":{"num_predict":16,"num_ctx":1024,"private-option":1}}`))
				if err != nil {
					t.Fatal(err)
				}
				_, readErr := io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if readErr != nil || response.StatusCode != 200 {
					t.Fatalf("completed chat: %d %v", response.StatusCode, readErr)
				}
			}
			if route == "/api/chat" {
				for deadline := time.Now().Add(2 * time.Second); ; {
					response, err := adminClient.Get("http://" + adminAddress + "/metrics")
					if err != nil {
						t.Fatal(err)
					}
					raw, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					prompt := positiveMetric(string(raw), `ollame_tokens_total{estimated="false",kind="prompt",model="test-qwen3:latest",token="anonymous"}`)
					completion := positiveMetric(string(raw), `ollame_tokens_total{estimated="false",kind="completion",model="test-qwen3:latest",token="anonymous"}`)
					ttft := positiveMetric(string(raw), `ollame_upstream_ttft_seconds_count{model="test-qwen3:latest"}`)
					dropped := positiveMetric(string(raw), `ollame_translation_dropped_total{field="num_ctx"}`) && positiveMetric(string(raw), `ollame_translation_dropped_total{field="other"}`)
					if strings.Contains(string(raw), "private-option") {
						t.Fatal("client option name leaked into metric labels")
					}
					attempts := positiveMetric(string(raw), `ollame_upstream_requests_total{code="200",endpoint="chat/completions"}`)
					if prompt && completion && ttft && dropped && attempts {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("completed chat usage/TTFT metrics missing")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			body := `{"model":"test-qwen3","stream":true,"messages":[{"role":"user","content":"Count from 1 to 1000, one number per line. Do not stop early."}],"options":{"num_predict":1024},"max_tokens":1024}`
			abandoned, err := client.Post("http://ollame"+route, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			abandonedScanner := bufio.NewScanner(abandoned.Body)
			if !abandonedScanner.Scan() {
				abandoned.Body.Close()
				t.Fatal("no chunk before client disconnect")
			}
			abandoned.Body.Close()
			for deadline := time.Now().Add(2 * time.Second); ; {
				scrape, err := adminClient.Get("http://" + adminAddress + "/metrics")
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(scrape.Body)
				scrape.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if positiveMetric(string(raw), `ollame_stream_aborts_total{reason="client_disconnect"}`) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("client disconnect was not counted")
				}
				time.Sleep(10 * time.Millisecond)
			}
			response, err := client.Post("http://ollame"+route, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("stream returned %d", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			if !scanner.Scan() {
				t.Fatalf("no initial stream chunk: %v", scanner.Err())
			}
			started := time.Now()
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			terminal := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, `"error"`) {
					terminal = true
				}
				if strings.Contains(line, "[DONE]") || strings.Contains(line, `"done":true`) {
					t.Fatal("shutdown reported successful completion")
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("stream closed without complete framing: %v", err)
			}
			if !terminal {
				t.Fatal("shutdown did not deliver terminal error")
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown exceeded terminal error budget")
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("shutdown exceeded terminal error budget")
			}
			rawLogs, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			foundStream, foundUsage, foundShutdown := false, false, false
			for _, line := range strings.Split(strings.TrimSpace(string(rawLogs)), "\n") {
				var event struct {
					Message          string `json:"message"`
					AbortReason      string `json:"abort_reason"`
					Model            string `json:"model"`
					UpstreamModel    string `json:"upstream_model"`
					Stream           bool   `json:"stream"`
					UpstreamStatus   int    `json:"upstream_status"`
					PromptTokens     int    `json:"prompt_tokens"`
					CompletionTokens int    `json:"completion_tokens"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.Message != "request" || event.Model == "" {
					continue
				}
				if event.Model != "test-qwen3:latest" || event.UpstreamModel != "test-qwen3" || event.UpstreamStatus != 200 {
					t.Fatalf("invalid inference access event: %+v", event)
				}
				if event.Stream && event.AbortReason != "shutdown" && event.AbortReason != "client_disconnect" {
					t.Fatalf("shutdown classified as %q", event.AbortReason)
				}
				foundShutdown = foundShutdown || event.AbortReason == "shutdown"
				foundStream = foundStream || event.Stream
				foundUsage = foundUsage || event.PromptTokens > 0 && event.CompletionTokens > 0
			}
			if !foundStream || !foundShutdown || route == "/api/chat" && !foundUsage {
				t.Fatal("stream or usage access evidence missing")
			}
		})
	}
}

func positiveMetric(body, prefix string) bool {
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(line, prefix+" "); ok {
			number, err := strconv.ParseFloat(value, 64)
			return err == nil && number > 0
		}
	}
	return false
}
