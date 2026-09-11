package test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestProcessReloadAndSecondSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	tokens := filepath.Join(dir, "tokens")
	if err := os.WriteFile(tokens, []byte("ci=signal-credential\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	writeConfig := func(inline bool) {
		t.Helper()
		body := "[auth]\n"
		if inline {
			body += "[[auth.token]]\nname = 'inline'\ntoken = 'inline-credential'\n"
		}
		if !inline {
			body += "[limits]\nmax_images = -1\n"
		}
		if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(true)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "api.sock")
	command := exec.CommandContext(ctx, binary, "serve", "--config", configPath, "--listen", "unix:"+socket, "--server-admin-listen", admin, "--upstream", "http://127.0.0.1:1/v1", "--auth-tokens-file", tokens, "--auth-reload-interval", "1h", "--auth-stale-grace", "0s", "--metrics-enabled=false", "--models-require-on-start=false", "--server-drain-delay", "30s")
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
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	defer func() { _ = command.Process.Kill() }()
	api := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer api.CloseIdleConnections()
	adminClient := &http.Client{Timeout: time.Second}
	defer adminClient.CloseIdleConnections()
	awaitStatus := func(client *http.Client, url string, want int, credential string) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if credential != "" {
				request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", credential))
			}
			response, err := client.Do(request)
			if err == nil {
				response.Body.Close()
				if response.StatusCode == want {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		output, _ := os.ReadFile(logPath)
		t.Fatalf("%s did not reach %d: %s", url, want, output)
	}
	awaitStatus(api, "http://ollame/api/tags", 200, "signal-credential")
	awaitStatus(adminClient, "http://"+admin+"/readyz", 200, "")
	awaitStatus(adminClient, "http://"+admin+"/metrics", 404, "")
	awaitStatus(api, "http://ollame/api/tags", 200, "inline-credential")
	writeConfig(false)
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitStatus(api, "http://ollame/api/tags", 401, "inline-credential")
	awaitStatus(api, "http://ollame/api/tags", 200, "signal-credential")
	writeConfig(true)
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitStatus(api, "http://ollame/api/tags", 200, "inline-credential")
	if err := os.WriteFile(configPath, []byte("[malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitStatus(api, "http://ollame/api/tags", 401, "inline-credential")
	awaitStatus(api, "http://ollame/api/tags", 200, "signal-credential")
	if err := os.WriteFile(tokens, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	awaitStatus(api, "http://ollame/api/tags", 401, "signal-credential")
	if err := command.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	awaitStatus(adminClient, "http://"+admin+"/readyz", 503, "")
	awaitStatus(api, "http://ollame/", 200, "")
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 128+int(syscall.SIGTERM) {
			t.Fatalf("second signal returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second signal did not force immediate exit")
	}
}
