package test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
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

func TestProcessTLSAndHeaderLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ollame process test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("test certificate did not parse")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "ollame")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/ollame")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	command := exec.CommandContext(ctx, binary, "serve", "--listen", address, "--server-admin-listen=", "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled", "--models-require-on-start=false", "--server-tls-cert", certPath, "--server-tls-key", keyPath, "--server-drain-delay", "0s", "--server-shutdown-grace", "1s")
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	logPath := filepath.Join(dir, "server.jsonl")
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
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	for deadline := time.Now().Add(3 * time.Second); ; {
		response, err := client.Get("https://" + address + "/")
		if err == nil {
			raw, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != 200 || string(raw) != "Ollama is running" || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
				t.Fatal("TLS response was not authenticated")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS listener failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, err := http.NewRequestWithContext(ctx, "GET", "https://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Oversized", strings.Repeat("x", 128<<10))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 431 {
		t.Fatalf("oversized header returned %d", response.StatusCode)
	}
	plain := &http.Client{Timeout: time.Second}
	defer plain.CloseIdleConnections()
	response, err = plain.Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("plaintext reached TLS listener: %d", response.StatusCode)
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
		t.Fatal("TLS server shutdown timed out")
	}
	rawLogs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(rawLogs)), "\n") {
		var event struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("HTTP diagnostic bypassed JSON logging: %v", err)
		}
		if event.Message == "http server error" && strings.Contains(event.Detail, "TLS handshake error") {
			found = true
		}
	}
	if !found {
		t.Fatal("structured TLS diagnostic missing")
	}
}
