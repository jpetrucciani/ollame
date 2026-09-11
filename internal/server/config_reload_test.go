package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func TestRealConfigReloadPublication(t *testing.T) {
	endpoint := os.Getenv("OLLAME_TEST_UPSTREAM")
	if endpoint == "" {
		t.Skip("set OLLAME_TEST_UPSTREAM to run against the real LiteLLM integration stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = endpoint
	cfg.Upstream.APIKey = "local-test-only"
	cfg.Auth.Tokens = []config.Token{{Name: "old", Token: "old-credential"}}
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func() {
		t.Helper()
		raw, err := toml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	source := config.NewSource(path, config.Input{})
	loaded, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(loaded, source, nil, "test", "test-commit", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.active.Load().client.Close() }()
	s.refresh(ctx, false)
	before := s.active.Load()
	if len(before.catalog.Entries()) != 2 {
		t.Fatal("real initial catalog not loaded")
	}
	cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
	cfg.Auth.Tokens = nil
	cfg.Models.Include = []string{"test-qwen3"}
	write()
	s.reloadTokens(time.Now())
	s.reloadConfig(ctx)
	failed := s.active.Load()
	if failed.tokens.Len() != 0 || failed.client != before.client || failed.catalog != before.catalog || failed.config.Upstream.BaseURL != endpoint {
		t.Fatal("failed candidate changed upstream or prevented revocation")
	}
	if before.tokens.Len() != 1 {
		t.Fatal("in-flight snapshot changed")
	}
	cfg.Upstream.BaseURL = endpoint
	cfg.Server.Listen = ":12345"
	write()
	s.reloadConfig(ctx)
	after := s.active.Load()
	if after.generation != before.generation+1 || after.tokens.Len() != 0 || len(after.catalog.Entries()) != 1 || after.catalog.Entries()[0].Name != "test-qwen3:latest" {
		t.Fatal("candidate catalog/config not published atomically")
	}
	if after.config.Server.Listen != before.config.Server.Listen {
		t.Fatal("restart-only listener changed")
	}
	if len(before.catalog.Entries()) != 2 {
		t.Fatal("prior catalog mutated")
	}
	cfg.Upstream.APIKeyFile = filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(cfg.Upstream.APIKeyFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	write()
	s.reloadConfig(ctx)
	if s.active.Load().key != after.key {
		t.Fatal("empty key file replaced active key")
	}
	s.draining.Store(true)
	final := s.active.Load()
	cfg.Models.Include = []string{"*"}
	write()
	s.reloadConfig(ctx)
	if s.active.Load() != final {
		t.Fatal("reload published during shutdown")
	}
}
