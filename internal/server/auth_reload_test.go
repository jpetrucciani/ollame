package server

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func TestTokenReloadRevokesAndRetainsInflightSnapshot(t *testing.T) {
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
	cfg.Auth.TokensFile = filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(cfg.Auth.TokensFile, []byte("ci=secret-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := config.NewSource("", config.Input{TOML: raw})
	s, err := New(config.Loaded{Config: cfg}, source, nil, "test", "test-commit", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.active.Load().client.Close()
	before := s.active.Load()
	if before.tokens.Len() != 1 {
		t.Fatal("initial token missing")
	}
	if err := os.WriteFile(cfg.Auth.TokensFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.reloadTokens(time.Now())
	after := s.active.Load()
	if after == before || after.tokens.Len() != 0 || before.tokens.Len() != 1 {
		t.Fatal("revocation or snapshot isolation failed")
	}
	if after.client != before.client || after.catalog != before.catalog {
		t.Fatal("auth reload replaced upstream state")
	}
}

func TestTokenReloadStaleGraceAndShutdown(t *testing.T) {
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
	cfg.Auth.TokensFile = filepath.Join(t.TempDir(), "tokens")
	cfg.Auth.StaleGrace = config.Duration(time.Minute)
	if err := os.WriteFile(cfg.Auth.TokensFile, []byte("ci=secret-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := config.NewSource("", config.Input{TOML: raw})
	s, err := New(config.Loaded{Config: cfg}, source, nil, "test", "test-commit", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.active.Load().client.Close()
	if err := os.WriteFile(cfg.Auth.TokensFile, []byte("not a valid token file"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.reloadTokens(now)
	s.reloadTokens(now.Add(59 * time.Second))
	if s.active.Load().tokens.Len() != 1 {
		t.Fatal("last-good tokens lost before grace")
	}
	s.reloadTokens(now.Add(time.Minute))
	if s.active.Load().tokens.Len() != 0 {
		t.Fatal("stale source did not fail closed")
	}
	s.draining.Store(true)
	before := s.active.Load()
	if err := os.WriteFile(cfg.Auth.TokensFile, []byte("new=secret-two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.reloadTokens(now.Add(2 * time.Minute))
	if s.active.Load() != before {
		t.Fatal("reload published during shutdown")
	}
}
