package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func pointer[T any](value T) *T { return &value }
func TestOptionPrecedenceAndPrecision(t *testing.T) {
	request := Fields{"temperature": json.RawMessage("0.2"), "seed": json.RawMessage("9007199254740993"), "stop": json.RawMessage(`"end"`), "num_predict": json.RawMessage("42")}
	entry := catalog.Entry{Behavior: config.Behavior{MaxTokensField: pointer("max_completion_tokens")}}
	body, _, err := Options(map[string]any{"temperature": 0.9, "top_p": 0.7}, request, entry, false)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"temperature": "0.2", "top_p": "0.7", "seed": "9007199254740993", "stop": `["end"]`, "max_completion_tokens": "42"} {
		if string(body[key]) != want {
			t.Errorf("%s: %s", key, body[key])
		}
	}
	request["seed"][0] = '1'
	if string(body["seed"]) != "9007199254740993" {
		t.Fatal("outgoing body aliases caller memory")
	}
}
func TestDropsAndStrictOptions(t *testing.T) {
	name := strings.Repeat("unknown", 40)
	request := Fields{"top_k": json.RawMessage("8"), "num_ctx": json.RawMessage("8192"), name: json.RawMessage("0"), "num_predict": json.RawMessage("-1")}
	body, dropped, err := Options(nil, request, catalog.Entry{}, false)
	if err != nil || len(body) != 0 || len(dropped) != 3 {
		t.Fatalf("drop rules: %v %v %v", body, dropped, err)
	}
	for _, entry := range dropped {
		if len(entry.Name) > 64 {
			t.Fatal("unbounded unknown name")
		}
		if strings.HasPrefix(entry.Name, "unknown") && entry.Field != "other" {
			t.Fatal("unbounded metric label")
		}
	}
	if _, _, err = Options(nil, request, catalog.Entry{}, true); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("strict options accepted unknown option")
	}
	entry := catalog.Entry{Behavior: config.Behavior{ForwardSamplerExtras: pointer(true)}}
	body, _, err = Options(nil, Fields{"repeat_penalty": json.RawMessage("1.1")}, entry, false)
	if err != nil || string(body["repetition_penalty"]) != "1.1" {
		t.Fatal("sampler mapping failed")
	}
}
func TestInvalidOptionTypes(t *testing.T) {
	for key, raw := range map[string]string{"seed": "1.5", "num_predict": "null", "temperature": "true", "stop": "[1]"} {
		if _, _, err := Options(nil, Fields{key: json.RawMessage(raw)}, catalog.Entry{}, false); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("accepted %s=%s", key, raw)
		}
	}
}
func TestFormatPreservesSchemaBytes(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"z":{"const":9007199254740993},"a":{"type":"string"}},"unknown":true}`)
	raw, err := Format(schema, true)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Schema struct {
			Schema json.RawMessage `json:"schema"`
			Strict bool            `json:"strict"`
		} `json:"json_schema"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(schema, response.Schema.Schema) || !response.Schema.Strict {
		t.Fatal("schema changed during mapping")
	}
	for _, invalid := range []string{`"yaml"`, `[]`, `true`, `{`} {
		if _, err = Format(json.RawMessage(invalid), false); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("accepted %s", invalid)
		}
	}
}
func TestThinkingMatrix(t *testing.T) {
	cfg := config.Defaults().Compat
	for _, style := range []string{"reasoning_effort", "chat_template_kwargs", "none"} {
		for _, value := range []any{nil, false, true, "low", "medium", "high", "max"} {
			entry := catalog.Entry{Behavior: config.Behavior{ThinkStyle: pointer(style)}}
			fields, visible, warn, err := Thinking(value, entry, cfg)
			if err != nil || warn {
				t.Fatal(err)
			}
			if visible != (value != false) {
				t.Fatal("incorrect reasoning visibility")
			}
			if value == nil || style == "none" {
				if len(fields) != 0 {
					t.Fatal("unexpected thinking parameter")
				}
				continue
			}
			if style == "chat_template_kwargs" {
				var kwargs struct {
					Enabled bool `json:"enable_thinking"`
				}
				if err = json.Unmarshal(fields["chat_template_kwargs"], &kwargs); err != nil || kwargs.Enabled != visible {
					t.Fatal("template thinking mapping")
				}
			}
		}
	}
	entry := catalog.Entry{Think: false}
	_, visible, _, err := Thinking(nil, entry, cfg)
	if err != nil || visible {
		t.Fatal("alias thinking default ignored")
	}
	entry.Knowledge = map[string]catalog.Knowledge{"thinking": catalog.No}
	fields, _, warn, err := Thinking(true, entry, cfg)
	if err != nil || !warn || len(fields) != 0 {
		t.Fatal("known-no not relaxed")
	}
	cfg.StrictCapabilities = true
	if _, _, _, err = Thinking(true, entry, cfg); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("strict known-no not rejected")
	}
}

func TestQuotedNumbersAreNotNumericOptions(t *testing.T) {
	entry := catalog.Entry{Behavior: config.Behavior{ForwardSamplerExtras: pointer(true)}}
	for _, key := range []string{"temperature", "top_p", "min_p", "typical_p", "repeat_penalty"} {
		if _, _, err := Options(nil, Fields{key: json.RawMessage(`"0.5"`)}, entry, false); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("accepted quoted number for %s", key)
		}
	}
}

func TestMetricFieldVocabulary(t *testing.T) {
	for _, name := range []string{"num_ctx", "temperature", "num_gpu", "top_k"} {
		if MetricField(name) != name {
			t.Fatalf("known field lost: %s", name)
		}
	}
	for _, name := range []string{"", "client-secret-option", strings.Repeat("x", 4096), "tool_type", "context"} {
		if MetricField(name) != "other" {
			t.Fatal("non-option field escaped metric vocabulary")
		}
	}
}
