package test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcessProfilingGates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, debug := range []bool{false, true} {
		for _, level := range []string{"info", "debug"} {
			t.Run(strconv.FormatBool(debug)+"/"+level, func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				admin := listener.Addr().String()
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				socket := filepath.Join(t.TempDir(), "api.sock")
				command := exec.CommandContext(ctx, binary, "serve", "--listen", "unix:"+socket, "--server-admin-listen", admin, "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled", "--models-require-on-start=false", "--admin-debug="+strconv.FormatBool(debug), "--log-level", level, "--server-drain-delay", "0s", "--server-shutdown-grace", "100ms")
				command.Env = []string{"PATH=" + os.Getenv("PATH")}
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				finished := make(chan error, 1)
				go func() { finished <- command.Wait() }()
				defer func() { _ = command.Process.Kill() }()
				client := &http.Client{Timeout: time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
				defer client.CloseIdleConnections()
				for deadline := time.Now().Add(3 * time.Second); ; {
					response, err := client.Get("http://" + admin + "/healthz")
					if err == nil {
						response.Body.Close()
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("admin listener did not start")
					}
					time.Sleep(10 * time.Millisecond)
				}

				for _, path := range []string{"/debug/config", "/debug/catalog"} {
					response, err := client.Get("http://" + admin + path)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
					expected := 404
					if debug {
						expected = 200
					}
					if response.StatusCode != expected {
						t.Fatalf("admin %s: got %d want %d", path, response.StatusCode, expected)
					}
				}
				if debug {
					response, err := client.Get("http://" + admin + "/debug/config")
					if err != nil {
						t.Fatal(err)
					}
					var effective struct {
						Config     map[string]json.RawMessage `json:"config"`
						Provenance map[string]string          `json:"provenance"`
						Generation uint64                     `json:"generation"`
					}
					decodeErr := json.NewDecoder(response.Body).Decode(&effective)
					response.Body.Close()
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					if response.StatusCode != 200 || effective.Provenance["server.listen"] != "flag" || effective.Provenance["upstream.base_url"] != "flag" || effective.Generation != 1 {
						t.Fatal("active configuration provenance missing")
					}
					var upstream struct {
						APIKey string `json:"api_key"`
					}
					if err := json.Unmarshal(effective.Config["upstream"], &upstream); err != nil {
						t.Fatal(err)
					}
					if upstream.APIKey != "<redacted>" {
						t.Fatal("debug config lost typed redaction")
					}
				}
				want := 404
				if debug && level == "debug" {
					want = 200
				}
				for _, path := range []string{"/debug/pprof", "/debug/pprof/", "/debug/pprof/goroutine?debug=1"} {
					response, err := client.Get("http://" + admin + path)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
					expected := want
					if path == "/debug/pprof" && want == 200 {
						expected = 301
					}
					if response.StatusCode != expected {
						t.Fatalf("%s: got %d want %d", path, response.StatusCode, expected)
					}
				}
				api := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				}}}
				defer api.CloseIdleConnections()
				for _, path := range []string{"/debug/pprof/goroutine", "/debug/config", "/debug/catalog"} {
					response, err := api.Get("http://ollame" + path)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
					if response.StatusCode != 404 {
						t.Fatalf("%s exposed on API listener", path)
					}
				}
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("profiling process failed shutdown")
				}
			})
		}
	}
}
