package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jpetrucciani/ollame/internal/config"
)

func TestLoggerFormatsAndLevels(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := newLogger(&output, config.Log{Level: "warn", Format: format})
			if err != nil {
				t.Fatal(err)
			}
			logger.Info().Msg("hidden info")
			logger.Warn().Str("source", "inline").Bool("expired", true).Msg("token source read failed")
			if strings.Contains(output.String(), "hidden info") {
				t.Fatal("configured level not applied")
			}
			if format == "json" {
				var event struct {
					Level   string `json:"level"`
					Message string `json:"message"`
					Source  string `json:"source"`
					Expired bool   `json:"expired"`
					Time    string `json:"time"`
				}
				if err := json.Unmarshal(output.Bytes(), &event); err != nil {
					t.Fatal(err)
				}
				if event.Level != "warn" || event.Message != "token source read failed" || event.Source != "inline" || !event.Expired || event.Time == "" {
					t.Fatalf("incomplete event: %+v", event)
				}
			} else if !strings.Contains(output.String(), "token source read failed") || strings.Contains(output.String(), "\x1b") {
				t.Fatal("console output missing or contains color escapes")
			}
		})
	}
}

func TestHTTPDiagnosticsAreScrubbed(t *testing.T) {
	var output bytes.Buffer
	logger, err := newLogger(&output, config.Log{Level: "info", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	redactor, err := config.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{redactor: redactor}
	s.active.Store(&snapshot{logger: logger})
	credential := "olm_" + strings.Repeat("x", 43)
	raw := []byte("transport error https://user:private@example.test/ " + credential + " " + strings.Repeat("z", 4096))
	written, err := (httpErrorWriter{server: s}).Write(raw)
	if err != nil || written != len(raw) {
		t.Fatal("HTTP error writer rejected diagnostic")
	}
	var event struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Message != "http server error" || len(event.Detail) > 2048 || strings.Contains(event.Detail, credential) || strings.Contains(event.Detail, "user:private") {
		t.Fatal("HTTP diagnostic escaped redaction or bounds")
	}
}
