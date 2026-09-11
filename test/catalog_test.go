package test

import (
	"context"
	"encoding/json"
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

func TestRealCatalogListAndShow(t *testing.T) {
	upstreamURL := os.Getenv("OLLAME_TEST_UPSTREAM")
	if upstreamURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("OLLAME_TEST_UPSTREAM required for real catalog integration")
		}
		t.Skip("set OLLAME_TEST_UPSTREAM to the real pinned integration stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	models := exec.CommandContext(ctx, binary, "models", "--upstream", upstreamURL, "--json")
	models.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := models.Output()
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		Name          string `json:"name"`
		Upstream      string `json:"upstream"`
		ContextLength int    `json:"context_length"`
	}
	if err = json.Unmarshal(output, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "test-nomic-embed:latest" || listed[0].ContextLength != 2048 || listed[1].Name != "test-qwen3:latest" || listed[1].Upstream != "test-qwen3" || listed[1].ContextLength != 4096 {
		t.Fatalf("unexpected real catalog: %s", output)
	}
	socket := filepath.Join(t.TempDir(), "api.sock")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	daemon := exec.CommandContext(ctx, binary, "serve", "--config", configPath, "--auth-reload-interval", "1h", "--listen", "unix:"+socket, "--server-admin-listen=", "--upstream", upstreamURL, "--auth-mode", "disabled", "--server-drain-delay", "0s", "--server-shutdown-grace", "1s")
	daemon.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = daemon.Process.Kill() }()
	finished := make(chan error, 1)
	go func() { finished <- daemon.Wait() }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer client.CloseIdleConnections()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get("http://localhost/api/tags")
		if err == nil {
			decodeErr := json.NewDecoder(response.Body).Decode(&tags)
			response.Body.Close()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(tags.Models) > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("real catalog did not become available")
		}
		select {
		case err := <-finished:
			t.Fatalf("daemon exited: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	response, err := client.Post("http://localhost/api/show", "application/x-www-form-urlencoded", strings.NewReader(`{"model":"test-qwen3","verbose":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var show map[string]json.RawMessage
	if err = json.NewDecoder(response.Body).Decode(&show); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("show failed: %v", show)
	}
	for _, field := range []string{"details", "model_info", "template", "capabilities"} {
		if _, ok := show[field]; !ok {
			t.Errorf("show omitted %s", field)
		}
	}
	if _, ok := show["tensors"]; ok {
		t.Fatal("show added empty tensors")
	}
	if err := os.WriteFile(configPath, []byte("[models]\ninclude = ['test-qwen3']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		response, err := client.Get("http://localhost/api/tags")
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(response.Body).Decode(&tags)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(tags.Models) == 1 && tags.Models[0].Name == "test-qwen3:latest" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGHUP did not publish filtered catalog")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown timeout")
	}
}
