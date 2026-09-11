package test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessUploadDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	socket := filepath.Join(dir, "api.sock")
	command := exec.CommandContext(ctx, binary, "serve", "--listen", "unix:"+socket, "--server-admin-listen=", "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled", "--upstream-request-timeout", "100ms", "--server-max-inflight", "1", "--models-require-on-start=false", "--models-refresh-on-miss=false")
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer client.CloseIdleConnections()
	for deadline := time.Now().Add(3 * time.Second); ; {
		response, err := client.Get("http://ollame/")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/api/chat", "/api/embed", "/api/show", "/v1/chat/completions", "/v1/messages"} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: ollame\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n", path)
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		interim, err := http.ReadResponse(reader, nil)
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		interim.Body.Close()
		if interim.StatusCode != 100 {
			conn.Close()
			t.Fatalf("%s: no body read: %d", path, interim.StatusCode)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			conn.Close()
			t.Fatalf("%s upload did not terminate: %v", path, err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 504 {
			t.Fatalf("%s: expected 504, got %d: %s", path, response.StatusCode, raw)
		}
		if path == "/v1/messages" && !strings.Contains(string(raw), `"type":"error"`) {
			t.Fatal("Anthropic timeout envelope missing")
		}
	}
	response, err := client.Post("http://ollame/api/chat", "application/json", strings.NewReader(`{"model":"unknown"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("timed-out upload retained admission: %d", response.StatusCode)
	}
}
