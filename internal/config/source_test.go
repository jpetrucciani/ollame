package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceReloadKeepsProcessOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[upstream]\nbase_url = 'http://first/v1'\n[server]\nlisten = ':1111'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := Input{Env: []string{"OLLAME_SERVER_LISTEN=:2222"}, Flags: map[string]string{"server.listen": ":3333"}}
	source := NewSource(path, input)
	input.Env[0] = "OLLAME_SERVER_LISTEN=:4444"
	input.Flags["server.listen"] = ":5555"
	first, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	if first.Config.Server.Listen != ":3333" || first.Config.Upstream.BaseURL != "http://first/v1" {
		t.Fatal("source lost original overrides")
	}
	if err := os.WriteFile(path, []byte("[upstream]\nbase_url = 'http://second/v1'\n[server]\nlisten = ':6666'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	if second.Config.Server.Listen != ":3333" || second.Config.Upstream.BaseURL != "http://second/v1" || first.Config.Upstream.BaseURL != "http://first/v1" {
		t.Fatal("reload changed static flags or prior config")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing selected config fell back silently")
	}
}
