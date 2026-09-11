package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessAudioOptInValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_denied", true: "opt_in"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			socket := filepath.Join(dir, "api.sock")
			configPath := filepath.Join(dir, "config.toml")
			configText := ""
			if enabled {
				configText = "[passthrough]\npaths = [\"/v1/audio/transcriptions\"]\n"
			}
			if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, binary, "serve", "--config", configPath, "--listen", "unix:"+socket, "--server-admin-listen=", "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled", "--models-require-on-start=false", "--models-refresh-on-miss=false", "--server-max-body-bytes", "1KiB", "--server-drain-delay", "0s")
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			logPath := filepath.Join(dir, "process.log")
			logs, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Close()
			command.Stdout, command.Stderr = logs, logs
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}}
			defer client.CloseIdleConnections()
			ready := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				response, err := client.Get("http://ollame/")
				if err == nil {
					response.Body.Close()
					ready = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				output, _ := os.ReadFile(logPath)
				t.Fatalf("process did not start: %s", output)
			}
			for _, models := range [][]string{nil, {"outside"}, {"outside", "outside"}} {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				for _, model := range models {
					if err := writer.WriteField("model", model); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				response, err := client.Post("http://ollame/v1/audio/transcriptions", writer.FormDataContentType(), &body)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				want := 404
				if enabled && len(models) != 1 {
					want = 400
				}
				if response.StatusCode != want {
					t.Fatalf("models=%v got %d: %s", models, response.StatusCode, raw)
				}
				if enabled {
					var value struct {
						Error struct {
							Type string `json:"type"`
						} `json:"error"`
					}
					if err := json.Unmarshal(raw, &value); err != nil || value.Error.Type != "invalid_request_error" {
						t.Fatal("wrong audio error protocol")
					}
				} else if string(raw) != "404 page not found\n" {
					t.Fatal("default audio route was exposed")
				}
			}
			if enabled {
				response, err := client.Post("http://ollame/v1/audio/transcriptions", "multipart/form-data; boundary=x", strings.NewReader(strings.Repeat("x", 2048)))
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != 413 {
					t.Fatalf("body limit returned %d", response.StatusCode)
				}
			}
		})
	}
}
