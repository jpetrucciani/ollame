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

// This smoke test runs the actual binary. The deliberately unavailable upstream
// exercises require_on_start=false; it is not an inference conformance substitute.
func TestProcessBootAuthenticationAndShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("process smoke requires a Go build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	socket := filepath.Join(t.TempDir(), "api.sock")
	adminPort, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminAddress := adminPort.Addr().String()
	if err = adminPort.Close(); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(tokenFile, []byte("rotating=rotating-credential\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "serve", "--listen", "unix:"+socket, "--server-admin-listen", adminAddress, "--upstream", "http://127.0.0.1:1/v1", "--metrics-token-labels=false", "--log-bodies=true", "--server-cors-origins", "https://client.example", "--server-max-inflight", "1", "--token", "ci=smoke-credential", "--auth-tokens-file", tokenFile, "--auth-reload-interval", "50ms", "--auth-stale-grace", "100ms", "--models-refresh-interval", "50ms", "--models-require-on-start=false", "--models-refresh-on-miss=false", "--server-drain-delay", "10ms", "--server-shutdown-grace", "1s")
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	logs, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	command.Stdout = logs
	command.Stderr = logs
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	defer func() { _ = command.Process.Kill() }()
	api := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer api.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := api.Get("http://localhost/")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		select {
		case err := <-finished:
			t.Fatalf("server exited early: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, tc := range []struct {
		method, path, origin string
		status               int
		allowed              bool
	}{
		{"OPTIONS", "/api/chat", "https://client.example", 204, true},
		{"OPTIONS", "/v1/messages", "https://client.example", 204, true},
		{"OPTIONS", "/api/chat", "https://other.example", 403, false},
		{"OPTIONS", "/unregistered", "https://client.example", 404, false},
		{"OPTIONS", "/unregistered", "https://other.example", 404, false},
		{"GET", "/api/tags", "https://client.example", 401, true},
		{"GET", "/api/tags", "https://other.example", 401, false},
	} {
		request, err := http.NewRequestWithContext(ctx, tc.method, "http://localhost"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", tc.origin)
		if tc.method == "OPTIONS" {
			request.Header.Set("Access-Control-Request-Method", "POST")
			request.Header.Set("Access-Control-Request-Headers", "authorization, content-type, x-api-key")
		}
		response, err := api.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := ""
		if tc.allowed {
			want = tc.origin
		}
		if response.StatusCode != tc.status || response.Header.Get("Access-Control-Allow-Origin") != want {
			t.Fatalf("CORS %s %s: status %d headers %v", tc.method, tc.path, response.StatusCode, response.Header)
		}
		if tc.status == 204 && !strings.Contains(response.Header.Get("Access-Control-Allow-Headers"), "authorization") {
			t.Fatal("preflight did not allow authorization")
		}
	}
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{{"GET", "/", "", 200}, {"GET", "/api/version", "", 200}, {"GET", "/api/tags", "", 401}, {"GET", "/api/tags", "wrong", 401}, {"GET", "/api/tags", "smoke-credential", 200}, {"HEAD", "/api/tags", "smoke-credential", 200}, {"POST", "/api/tags", "smoke-credential", 405}, {"GET", "/readyz", "", 404}, {"GET", "/metrics", "", 404}, {"GET", "/unregistered", "", 404}, {"GET", "/api/codex/v1/responses", "", 404}, {"POST", "/api/codex/v1/responses", "", 404}, {"DELETE", "/api/codex/session", "smoke-credential", 404}} {
		request, err := http.NewRequestWithContext(ctx, tc.method, "http://localhost"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Request-Id", "process-correlation")
		request.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		if tc.token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.token)
		}
		response, err := api.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("X-Request-Id") != "process-correlation" {
			t.Fatal("request correlation not echoed")
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.status {
			t.Fatalf("%s %s: status %d, want %d: %s", tc.method, tc.path, response.StatusCode, tc.status, body)
		}
		if tc.path == "/api/tags" && tc.method == "GET" && tc.status == 200 {
			var payload struct {
				Models []json.RawMessage `json:"models"`
			}
			if err = json.Unmarshal(body, &payload); err != nil || payload.Models == nil || len(payload.Models) != 0 {
				t.Fatal("empty catalog has wrong shape")
			}
		}
		if tc.method == "HEAD" && len(body) != 0 {
			t.Fatal("HEAD returned a body")
		}
		if strings.HasPrefix(tc.path, "/api/codex/") && (string(body) != "404 page not found\n" || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain")) {
			t.Fatal("desktop proxy rejection differs from unknown path")
		}
	}
	for _, tc := range []struct {
		method, path string
		status       int
		message      string
	}{
		{"POST", "/api/push", 501, "push is not supported by ollame; models are managed upstream"},
		{"POST", "/api/create", 501, "create is not supported by ollame; define aliases in config"},
		{"POST", "/api/copy", 501, "copy is not supported by ollame; define aliases in config"},
		{"DELETE", "/api/delete", 501, "delete is not supported by ollame; models are managed upstream"},
		{"POST", "/api/blobs/sha256:abc", 501, "blob upload is not supported by ollame; models are managed upstream"},
		{"HEAD", "/api/blobs/sha256:abc", 404, ""},
		{"POST", "/api/me", 503, "account unavailable"},
		{"POST", "/api/signout", 200, ""},
		{"DELETE", "/api/user/keys/old-key", 200, ""},
		{"POST", "/api/experimental/web_search", 501, "web search is not supported by ollame"},
		{"POST", "/api/experimental/web_fetch", 501, "web search is not supported by ollame"},
		{"GET", "/api/experimental/model-recommendations", 501, "model recommendations are not supported by ollame"},
		{"POST", "/api/pull", 404, "pull model manifest: file does not exist"},
	} {
		for _, authenticated := range []bool{false, true} {
			request, err := http.NewRequestWithContext(ctx, tc.method, "http://localhost"+tc.path, strings.NewReader(`{"model":"not-in-catalog"}`))
			if err != nil {
				t.Fatal(err)
			}
			if authenticated {
				request.Header.Set("Authorization", "Bearer smoke-credential")
			}
			response, err := api.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			status, message := tc.status, tc.message
			if !authenticated {
				status, message = 401, "unauthorized"
			}
			if response.StatusCode != status {
				t.Fatalf("%s: got %d want %d", tc.path, response.StatusCode, status)
			}
			if tc.method == "HEAD" {
				if len(raw) != 0 {
					t.Fatal("blob HEAD emitted body")
				}
				continue
			}
			var body map[string]string
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != message || message == "" && len(body) != 0 {
				t.Fatalf("%s unexpected body: %s", tc.path, raw)
			}
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		response, err := api.Post("http://localhost"+path, "application/json", strings.NewReader(`{"model":"unknown"}`))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Type  string `json:"type"`
			Error struct {
				Type      string `json:"type"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
				Route     string `json:"route"`
				Status    int    `json:"status"`
			} `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 || body.Error.Type != "authentication_error" || body.Error.Message != "unauthorized" {
			t.Fatalf("wrong protocol auth response for %s", path)
		}
		if (body.Type == "error") != (path == "/v1/messages") {
			t.Fatal("wrong protocol envelope")
		}
	}
	awaitTokenStatus := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			request, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/api/tags", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer rotating-credential")
			response, err := api.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("rotating token did not reach status %d while upstream was unavailable", want)
	}
	awaitTokenStatus(200)
	// A real 100-continue response proves the handler passed authentication and
	// started reading this request before the token is revoked.
	inflight, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer inflight.Close()
	if err := inflight.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"unknown"}`
	if _, err := fmt.Fprintf(inflight, "POST /api/chat HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer rotating-credential\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", len(body)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(inflight)
	interim, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	interim.Body.Close()
	if interim.StatusCode != 100 {
		t.Fatalf("request did not reach body read: %d", interim.StatusCode)
	}
	scraper := &http.Client{Timeout: time.Second}
	heldMetrics, err := scraper.Get("http://" + adminAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	heldBytes, err := io.ReadAll(heldMetrics.Body)
	heldMetrics.Body.Close()
	scraper.CloseIdleConnections()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(heldBytes), `ollame_inflight_requests{route="/api/chat"} 1`) {
		t.Fatal("held inference not reflected in inflight gauge")
	}
	for _, path := range []string{"/api/chat", "/api/generate", "/api/embed", "/api/embeddings", "/v1/chat/completions", "/v1/messages"} {
		request, err := http.NewRequestWithContext(ctx, "POST", "http://localhost"+path, strings.NewReader(`{"model":"unknown"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer smoke-credential")
		response, err := api.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 503 || !strings.Contains(string(raw), "server busy") {
			t.Fatalf("%s failed admission: %d %s", path, response.StatusCode, raw)
		}
	}
	if err := os.WriteFile(tokenFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitTokenStatus(401)
	if _, err := io.WriteString(inflight, body); err != nil {
		t.Fatal(err)
	}
	completed, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	completed.Body.Close()
	if completed.StatusCode != 404 {
		t.Fatalf("in-flight request lost its authenticated snapshot: %d", completed.StatusCode)
	}
	probe, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	probe.Header.Set("Authorization", "Bearer smoke-credential")
	released, err := api.Do(probe)
	if err != nil {
		t.Fatal(err)
	}
	released.Body.Close()
	if released.StatusCode != 404 {
		t.Fatalf("completed request did not release admission slot: %d", released.StatusCode)
	}
	if err := os.WriteFile(tokenFile, []byte("rotating=rotating-credential\n"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitTokenStatus(200)
	if err := os.WriteFile(tokenFile, []byte("invalid token source"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitTokenStatus(401)
	admin := &http.Client{Timeout: time.Second}
	defer admin.CloseIdleConnections()
	metricsResponse, err := admin.Get("http://" + adminAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metricBytes, err := io.ReadAll(metricsResponse.Body)
	metricsResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if metricsResponse.StatusCode != 200 {
		t.Fatal("admin metrics not available")
	}
	for _, expected := range []string{`ollame_upstream_requests_total{code="error",endpoint="models"}`, "ollame_catalog_refresh_errors_total", `ollame_catalog_last_success_timestamp_seconds 0`, `reason="stale_expired",source="file"`, `ollame_inflight_requests{route="/api/chat"} 0`, "ollame_requests_total", "ollame_request_duration_seconds_count", `ollame_auth_failures_total{reason="invalid"}`} {
		if !strings.Contains(string(metricBytes), expected) {
			t.Fatalf("missing operational metric %s", expected)
		}
	}
	if strings.Contains(string(metricBytes), `token="`) {
		t.Fatal("disabled token labels were exported")
	}
	if strings.Contains(string(metricBytes), "rotating-credential") || strings.Contains(string(metricBytes), tokenFile) {
		t.Fatal("secret value or source path escaped into metrics")
	}

	for _, tc := range []struct {
		path   string
		status int
	}{{"/healthz", 200}, {"/readyz", 200}, {"/debug/config", 404}, {"/debug/catalog", 404}} {
		response, err := admin.Get("http://" + adminAddress + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("admin %s: %d", tc.path, response.StatusCode)
		}
	}
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown exceeded process deadline")
	}
	if _, err = logs.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(logs)
	if err != nil {
		t.Fatal(err)
	}
	foundWarning := false
	foundAccess := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event struct {
			Level     string `json:"level"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			TraceID   string `json:"trace_id"`
			Route     string `json:"route"`
			Status    int    `json:"status"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("server log is not JSON: %v", err)
		}
		if event.Message == "request" && event.RequestID == "process-correlation" && event.Route == "/api/tags" && event.Status == 401 {
			if event.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
				t.Fatalf("access log lost incoming trace context: %q", event.TraceID)
			}
			foundAccess = true
		}
		if event.Level == "warn" && event.Message == "body logging is enabled" {
			foundWarning = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !foundAccess {
		t.Fatal("correlated auth failure access log missing")
	}
	if !foundWarning {
		t.Fatal("body logging startup warning missing")
	}
	if strings.Contains(string(output), "smoke-credential") || strings.Contains(string(output), "rotating-credential") {
		t.Fatal("server logged credential")
	}
}
