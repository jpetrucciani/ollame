package server

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/translate"
)

func TestKeepAliveParsing(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": 5 * time.Minute, " null ": 5 * time.Minute, `"2m"`: 2 * time.Minute,
		"1.5": 1500 * time.Millisecond, "0": 0, "-3": -1, `"-1s"`: -time.Second,
	} {
		got, err := parseKeepAlive(json.RawMessage(raw))
		if err != nil || got != want {
			t.Fatalf("%q: got %s, %v; want %s", raw, got, err, want)
		}
	}
	for _, raw := range []string{`true`, `{}`, `[]`, `"tomorrow"`, `1e100`} {
		if _, err := parseKeepAlive(json.RawMessage(raw)); !errors.Is(err, translate.ErrInvalidRequest) {
			t.Fatalf("accepted %s", raw)
		}
	}
}
