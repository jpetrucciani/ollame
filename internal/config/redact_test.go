package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTypedRedaction(t *testing.T) {
	cfg := Defaults()
	cfg.Upstream.APIKey = "arbitrary-private-key"
	cfg.Upstream.BaseURL = "https://user:password@example.org/v1"
	cfg.Upstream.ExtraHeaders = map[string]string{"x-secret": "arbitrary-header-secret"}
	cfg.Auth.Tokens = []Token{{Name: "ci", Token: "arbitrary-token"}, {Name: "digest", SHA256: strings.Repeat("ff", 32)}}
	encoded, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"arbitrary-private-key", "password", "arbitrary-header-secret", "arbitrary-token", strings.Repeat("ff", 32)} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("typed secret escaped")
		}
	}
	if !strings.Contains(string(encoded), "x-secret") || !strings.Contains(string(encoded), "https://example.org/v1") {
		t.Fatal("redaction lost safe context")
	}
}
func TestUpstreamErrorRedactionBeforeTruncation(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", 2030) + " olm_" + strings.Repeat("A", 43) + " https://user:pass@example.org/v1 sk-" + strings.Repeat("x", 40)
	result := redactor.UpstreamError(text)
	if len(result) > 2048 || strings.Contains(result, "olm_") || strings.Contains(result, "sk-") || strings.Contains(result, "user:pass") {
		t.Fatal("error scrub or bound failed")
	}
}
