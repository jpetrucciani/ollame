package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// This opt-in test uses the released Collector and actual ollame executable.
// The Collector image is pinned; there is no replacement OTLP receiver.
func TestRealTraceExport(t *testing.T) {
	if os.Getenv("OLLAME_TEST_TRACING") != "1" {
		t.Skip("set OLLAME_TEST_TRACING=1 with Docker available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	config, err := filepath.Abs("integration/otel-collector.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for index, protocol := range []string{"http/protobuf", "http/json", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			docker := func(args ...string) []byte {
				t.Helper()
				output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("docker: %v\n%s", err, output)
				}
				return output
			}
			container := strings.TrimSpace(string(docker("run", "--rm", "-d", "--memory", "256m", "--cpus", "1", "-p", "127.0.0.1::4317", "-p", "127.0.0.1::4318", "-v", config+":/config.yaml:ro", "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector@sha256:e495787f07dbe432ce763ebaf5bc3d113850e9eee2250ade7a3da6a882d0d69a", "--config=/config.yaml")))
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if output, err := exec.CommandContext(cleanup, "docker", "rm", "-f", container).CombinedOutput(); err != nil {
					t.Errorf("remove collector: %v %s", err, output)
				}
			}()
			port := "4318/tcp"
			if protocol == "grpc" {
				port = "4317/tcp"
			}
			address := strings.TrimSpace(string(docker("port", container, port)))
			for deadline := time.Now().Add(5 * time.Second); ; {
				connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
				if err == nil {
					connection.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("collector not listening: %v\n%s", err, docker("logs", container))
				}
				time.Sleep(10 * time.Millisecond)
			}
			socket := filepath.Join(t.TempDir(), "api.sock")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			admin := listener.Addr().String()
			listener.Close()
			logs, err := os.Create(filepath.Join(t.TempDir(), "ollame.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Close()
			command := exec.CommandContext(ctx, binary, "serve", "--listen", "unix:"+socket, "--server-admin-listen", admin, "--upstream", "http://127.0.0.1:1/v1", "--token", "ci=trace-test-secret", "--models-require-on-start=false", "--server-drain-delay", "0s", "--server-shutdown-grace", "1s")
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "OTEL_TRACES_EXPORTER=otlp", "OTEL_EXPORTER_OTLP_ENDPOINT=http://" + address, "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=" + protocol, "OTEL_BSP_SCHEDULE_DELAY=60000", "OTEL_TRACES_SAMPLER=always_on"}
			command.Stdout, command.Stderr = logs, logs
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			api := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}}
			defer api.CloseIdleConnections()
			for deadline := time.Now().Add(5 * time.Second); ; {
				response, err := api.Get("http://localhost/")
				if err == nil {
					response.Body.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ollame did not start: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			traceID := fmt.Sprintf("4bf92f3577b34da6a3ce929d0e0e473%d", index)
			request, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/api/show?key=trace-query-secret", strings.NewReader(`{"model":"missing-model"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer trace-test-secret")
			request.Header.Set("Cookie", "trace-cookie-secret")
			request.Header.Set("User-Agent", "trace-agent-secret")
			request.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
			response, err := api.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 404 {
				t.Fatalf("show: %d", response.StatusCode)
			}
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			// The 60-second batch delay is beyond this test: spans must reach the
			// Collector through the shutdown flush rather than its periodic timer.
			var output string
			for deadline := time.Now().Add(5 * time.Second); ; {
				output = string(docker("logs", container))
				if correlatedTraceSpans(t, output, traceID) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("missing correlated server/client exports:\n%s", output)
				}
				time.Sleep(20 * time.Millisecond)
			}
			for _, secret := range []string{"trace-test-secret", "trace-query-secret", "trace-cookie-secret", "trace-agent-secret", "missing-model"} {
				if strings.Contains(output, secret) {
					t.Fatalf("trace export leaked %s", secret)
				}
			}
			if !strings.Contains(output, "ollame upstream") || !strings.Contains(output, "ollame request") {
				t.Fatal("server/client span names absent")
			}
		})
	}
}

// Parse the pinned Collector debug export to verify parentage, not merely that
// the same trace ID appears somewhere in multiple log messages.
func correlatedTraceSpans(t *testing.T, output, traceID string) bool {
	t.Helper()
	serverID := ""
	var clientParents []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Message string `json:"msg"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("collector log JSON: %v", err)
		}
		for _, block := range strings.Split(event.Message, "\nSpan #")[1:] {
			fields := make(map[string]string)
			for _, line := range strings.Split(block, "\n") {
				key, value, ok := strings.Cut(line, ":")
				if ok {
					fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
				}
			}
			if fields["Trace ID"] != traceID {
				continue
			}
			if fields["Kind"] == "Server" && fields["Parent ID"] == "00f067aa0ba902b7" {
				serverID = fields["ID"]
			}
			if fields["Kind"] == "Client" {
				clientParents = append(clientParents, fields["Parent ID"])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, parent := range clientParents {
		if serverID != "" && parent == serverID {
			return true
		}
	}
	return false
}
