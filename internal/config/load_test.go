package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultRoundTrip(t *testing.T) {
	got, err := Load(Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Config, Defaults()) {
		t.Fatalf("default config changed in round trip:\n%+v", got.Config)
	}
	for _, key := range Keys() {
		if key.Help == "" || got.Provenance[key.Path] != "default" {
			t.Fatalf("missing metadata for %s", key.Path)
		}
	}
}
func TestLayeredConfiguration(t *testing.T) {
	loaded, err := Load(Input{TOML: []byte(`[upstream]
base_url = "http://localhost:4000/v1"
request_timeout = "2h"
[upstream.extra_headers]
x-team = "infra"
[auth]
mode = "disabled"
[models]
include = ["provider/*"]
[[models.alias]]
name = "Coder"
target = "old"
system = "retained"
options = { temperature = 0.2 }
think = false
context_length = 8192
`), Env: []string{"OLLAME_UPSTREAM_REQUEST_TIMEOUT=3h", "OLLAME_MODELS_ALIASES=coder=new", "OLLAME_SERVER_MAX_BODY_BYTES=1MiB", "OLLAME_NOT_REAL=secret-value"}, Flags: map[string]string{"upstream.request_timeout": "4h", "models.include": "*", "auth.mode": "optional"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = loaded.Config.Validate(ServeProfile); err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Upstream.RequestTimeout != Duration(4*time.Hour) || loaded.Provenance["upstream.request_timeout"] != "flag" {
		t.Fatal("flag precedence failed")
	}
	if loaded.Config.Server.MaxBodyBytes != 1<<20 || loaded.Provenance["server.max_body_bytes"] != "env" {
		t.Fatal("environment decode failed")
	}
	alias := loaded.Config.Models.Aliases[0]
	if alias.Target != "new" || alias.System != "retained" || alias.Think != false || alias.ContextLength == nil || *alias.ContextLength != 8192 {
		t.Fatalf("structured merge lost values: %+v", alias)
	}
	if loaded.Config.Upstream.ExtraHeaders["x-team"] != "infra" {
		t.Fatal("map was lost")
	}
	if len(loaded.Warnings) != 1 || strings.Contains(loaded.Warnings[0], "secret-value") {
		t.Fatal("unknown-env warning exposed value")
	}
}
func TestMalformedConfigDoesNotExposeSecrets(t *testing.T) {
	for _, input := range []Input{
		{TOML: []byte("[upstream]\napi_key = 123 # private-secret")},
		{TOML: []byte("[upstream]\napi_key_typo = 'private-secret'")},
		{Env: []string{"OLLAME_UPSTREAM_REQUEST_TIMEOUT=private-secret"}},
		{Flags: map[string]string{"upstream.request_timeout": "private-secret"}},
	} {
		_, err := Load(input)
		if err == nil {
			t.Fatal("expected validation error")
		}
		if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("secret appeared in error")
		}
	}
}
func TestEveryConfigKeyCanBeOverridden(t *testing.T) {
	for _, key := range Keys() {
		var value string
		switch v := key.Value.(type) {
		case string:
			value = v
		case bool:
			value = "false"
		case int:
			value = "5"
		case Duration:
			value = "7s"
		case Size:
			value = "9KiB"
		case []string:
			value = "x,y"
		case map[string]string:
			value = `{"x-value":"yes"}`
		default:
			t.Fatalf("unhandled type for %s", key.Path)
		}
		for _, input := range []Input{{Env: []string{key.Env + "=" + value}}, {Flags: map[string]string{key.Path: value}}} {
			if _, err := Load(input); err != nil {
				t.Errorf("%s: %v", key.Path, err)
			}
		}
	}
}
func TestValidationRejectsBoundaryBypasses(t *testing.T) {
	cases := []string{
		"[[models.alias]]\nname='a'\ntarget='one'\n[[models.alias]]\nname='A:latest'\ntarget='two'",
		"[[models.override]]\nmatch='*'\nextra_body={model='other'}",
		"[passthrough]\nresolve_models=false",
		"[passthrough]\npaths=['/arbitrary']",
		"[compat]\nthink_initial=true",
		"[upstream]\nrequest_timeout='0s'",
		"[embed]\nbatch_concurrency=2",
		"[models]\ninclude=['[']",
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			loaded, err := Load(Input{TOML: []byte(source), Env: []string{"OLLAME_UPSTREAM_BASE_URL=http://localhost/v1"}})
			if err == nil {
				err = loaded.Config.Validate(ServeProfile)
			}
			if err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
func TestSizeBounds(t *testing.T) {
	for _, text := range []string{"-1B", "9223372036854775807GiB", "1", "1XB", "NaNMiB"} {
		var size Size
		if err := size.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}
func TestTokenForms(t *testing.T) {
	for _, entries := range [][]string{{"bad name=secret"}, {"ok="}, {"ok=secret", "ok=other"}, {"ok=sha256:ff"}} {
		if _, err := ParseTokens(entries); err == nil {
			t.Errorf("accepted invalid token entries")
		}
	}
	tokens, err := ParseTokens([]string{"ci=with=equals", "other=sha256:" + strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].Token != "with=equals" || tokens[1].SHA256 == "" {
		t.Fatal("token parsing lost data")
	}
}

func TestEnvironmentModelOverrides(t *testing.T) {
	file := []byte("[[models.override]]\nmatch='old'\nsort_model_messages=false\n")
	loaded, err := Load(Input{TOML: file, Env: []string{`OLLAME_MODELS_OVERRIDES=[{"match":"qwen*","sort_model_messages":true},{"match":"qwen-special","sort_model_messages":false}]`}})
	if err != nil {
		t.Fatal(err)
	}
	overrides := loaded.Config.Models.Overrides
	if len(overrides) != 2 || overrides[0].Match != "qwen*" || overrides[0].SortModelMessages == nil || !*overrides[0].SortModelMessages || overrides[1].SortModelMessages == nil || *overrides[1].SortModelMessages {
		t.Fatalf("unexpected overrides: %+v", overrides)
	}
	if loaded.Provenance["models.override"] != "env" {
		t.Fatal("missing environment provenance")
	}
	loaded, err = Load(Input{TOML: file, Env: []string{"OLLAME_MODELS_OVERRIDES=[]"}})
	if err != nil || len(loaded.Config.Models.Overrides) != 0 {
		t.Fatalf("clear overrides: %+v %v", loaded.Config.Models.Overrides, err)
	}
	for _, value := range []string{``, `null`, `{}`, `[null]`, `[{"match":"qwen*","sort_model_messages":"true"}]`, `[{"match":"qwen*","unknown_field":"secret-value"}]`, `[] trailing`} {
		_, err = Load(Input{Env: []string{"OLLAME_MODELS_OVERRIDES=" + value}})
		if err == nil {
			t.Fatalf("accepted %q", value)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("error leaked value")
		}
	}
}
