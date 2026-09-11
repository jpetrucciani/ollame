package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpetrucciani/ollame/internal/config"
)

func run(t *testing.T, args, env []string, input string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := App{In: strings.NewReader(input), Out: &out, Err: &stderr, Env: env, Version: "test", Commit: "abc"}
	code := app.Run(context.Background(), args)
	return code, out.String(), stderr.String()
}
func TestUtilityCommandsIgnoreBrokenConfiguration(t *testing.T) {
	env := []string{"OLLAME_CONFIG=/does/not/exist", "OLLAME_UPSTREAM_BASE_URL=broken"}
	for _, args := range [][]string{{"version"}, {"token", "new", "ci"}, {"token", "hash"}, {"token", "new", "--help"}, {"token", "hash", "--help"}} {
		code, _, err := run(t, args, env, "sample\n")
		if code != 0 {
			t.Fatalf("%v failed: %s", args, err)
		}
	}
}
func TestCheckLoadsAndRedactsWithoutNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	source := `[upstream]
base_url="http://127.0.0.1:1/v1"
api_key="secret-upstream"
[upstream.extra_headers]
x-private="secret-header"
[[auth.token]]
name="ci"
token="secret-client"
[[models.alias]]
name="alias"
target="model"
system="my system"
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run(t, []string{"check", "--config", path}, nil, "")
	if code != 0 {
		t.Fatalf("check failed: %s", stderr)
	}
	for _, secret := range []string{"secret-upstream", "secret-header", "secret-client"} {
		if strings.Contains(out+stderr, secret) {
			t.Fatal("check exposed a credential")
		}
	}
	if !strings.Contains(out, "models.alias") || !strings.Contains(out, "my system") || !strings.Contains(out, "1 (ci)") {
		t.Fatal("effective config missing structured values")
	}
}
func TestEveryFlagHasHelpAndCompletion(t *testing.T) {
	code, out, stderr := run(t, []string{"check", "--help"}, nil, "")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, key := range config.Keys() {
		if !strings.Contains(out, "--"+key.Flag) || !strings.Contains(out, key.Help) {
			t.Errorf("missing help for %s", key.Path)
		}
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		code, out, stderr := run(t, []string{"completion", shell}, nil, "")
		if code != 0 {
			t.Fatal(stderr)
		}
		for _, key := range config.Keys() {
			if !strings.Contains(out, key.Flag) {
				t.Errorf("missing %s completion for %s", shell, key.Path)
			}
		}
	}
}
func TestCheckRequiresTokens(t *testing.T) {
	code, _, _ := run(t, []string{"check", "--upstream", "http://localhost/v1"}, nil, "")
	if code != 1 {
		t.Fatal("required auth accepted empty token set")
	}
	code, _, stderr := run(t, []string{"check", "--upstream", "http://localhost/v1", "--auth-mode", "disabled"}, nil, "")
	if code != 0 {
		t.Fatal(stderr)
	}
}
func TestRepeatedListFlagsAndProbeShorthand(t *testing.T) {
	input, _, probe, _, _, err := parseFlags("check", []string{"-u", "http://localhost/v1", "--probe", "--models-include", "one", "--models-include", "two,three"}, nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !probe || input.Flags["upstream.base_url"] != "http://localhost/v1" || input.Flags["models.include"] != "one,two,three" {
		t.Fatal("flag mapping failed")
	}
}

func TestEveryCommandHasStandaloneHelp(t *testing.T) {
	for _, command := range [][]string{{"serve"}, {"check"}, {"models"}, {"version"}, {"completion"}, {"token"}, {"token", "new"}, {"token", "hash"}} {
		for _, flag := range []string{"--help", "-h"} {
			args := append(append([]string{}, command...), flag)
			code, out, stderr := run(t, args, []string{"OLLAME_CONFIG=/missing/config.toml"}, "")
			if code != 0 || !strings.Contains(out, "Usage:") || !strings.Contains(out, "Example:") {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, stderr)
			}
		}
	}
}

func TestDiscoveryExitProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[upstream]\nbase_url=\"http://127.0.0.1:1/v1\"\n[auth]\nmode=\"invalid\"\ntokens_file=\"/missing/tokens\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"models", "--config", path, "--json"}, 2},
		{[]string{"check", "--config", path}, 1},
		{[]string{"check", "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled"}, 0},
		{[]string{"check", "--upstream", "http://127.0.0.1:1/v1", "--auth-mode", "disabled", "--probe"}, 2},
		{[]string{"models", "--upstream", "bad-url"}, 1},
		{[]string{"models", "--probe"}, 1},
		{[]string{"check", "--json"}, 1},
	} {
		code, _, stderr := run(t, tc.args, nil, "")
		if code != tc.code {
			t.Errorf("%v: code %d want %d: %s", tc.args, code, tc.code, stderr)
		}
	}
}

func TestBashCompletionLoads(t *testing.T) {
	code, script, stderr := run(t, []string{"completion", "bash"}, nil, "")
	if code != 0 {
		t.Fatal(stderr)
	}
	shell := os.Getenv("OLLAME_TEST_BASH")
	if shell == "" {
		shell = "bash"
	}
	if output, err := exec.Command(shell, "--noprofile", "--norc", "-c", "type complete").CombinedOutput(); err != nil {
		if os.Getenv("OLLAME_TEST_BASH") != "" {
			t.Fatalf("configured Bash lacks programmable completion: %v %s", err, output)
		}
		t.Skip("Bash lacks programmable completion; set OLLAME_TEST_BASH to an interactive Bash build")
	}
	command := exec.Command(shell, "--noprofile", "--norc")
	command.Stdin = strings.NewReader(script + "\ncomplete -p ollame\n")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--upstream") {
		t.Fatalf("bash completion registration failed: %v %s", err, output)
	}
}

func TestCompletionOutputFailure(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var stderr bytes.Buffer
		app := App{In: strings.NewReader(""), Out: output, Err: &stderr}
		if code := app.Run(context.Background(), []string{"completion", shell}); code != 1 {
			t.Errorf("%s completion reported success after output failed: %d", shell, code)
		}
	}
}
