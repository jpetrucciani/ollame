// Package cli owns command parsing without process-global flag or logger state.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jpetrucciani/ollame/internal/auth"
	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/server"
	"github.com/jpetrucciani/ollame/internal/upstream"
	"github.com/spf13/pflag"
)

type App struct {
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Env     []string
	Version string
	Commit  string
	Reload  <-chan struct{}
}

func (app App) Run(ctx context.Context, args []string) int {
	if err := app.run(ctx, args); err != nil {
		fmt.Fprintln(app.Err, err)
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		return 1
	}
	return 0
}

type exitError struct {
	code  int
	cause error
}

func (e *exitError) Error() string { return e.cause.Error() }
func (e *exitError) Unwrap() error { return e.cause }

func (app App) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if args[0] == "--help" || args[0] == "-h" {
		return app.help()
	}
	if strings.HasPrefix(args[0], "-") {
		args = append([]string{"serve"}, args...)
	}
	command := args[0]
	args = args[1:]
	switch command {
	case "version":
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			_, err := fmt.Fprintln(app.Out, "Usage: ollame version\nPrint the binary version, commit, and emulated Ollama API version. No config or network access.\nExample: ollame version")
			return err
		}
		if len(args) > 0 {
			return fmt.Errorf("version takes no arguments")
		}
		_, err := fmt.Fprintf(app.Out, "ollame %s (%s), Ollama API %s\n", app.Version, app.Commit, config.Defaults().Compat.OllamaVersion)
		return err
	case "token":
		return app.token(args)
	case "completion":
		return app.completion(args)
	case "check", "models", "serve":
	default:
		return fmt.Errorf("unknown command %q; run ollame --help", command)
	}
	input, path, probe, asJSON, help, err := parseFlags(command, args, app.Env, app.Out)
	if err != nil || help {
		return err
	}
	source := config.NewSource(path, input)
	loaded, err := source.Load()
	if err != nil {
		return err
	}
	profile := config.ServeProfile
	if command == "models" {
		profile = config.ModelsProfile
	}
	if err = loaded.Config.Validate(profile); err != nil {
		return err
	}
	for _, warning := range loaded.Warnings {
		fmt.Fprintln(app.Err, warning)
	}
	if command == "serve" {
		daemon, err := server.New(loaded, source, envMap(app.Env), app.Version, app.Commit, app.Err)
		if err != nil {
			return err
		}
		return daemon.Run(ctx, app.Reload)
	}
	if command == "check" {
		set, warnings, err := auth.NewSet(auth.ReadSources(loaded.Config.Auth, envMap(app.Env), loaded.EnvTokens, loaded.FlagTokens))
		if err != nil {
			return err
		}
		for _, warning := range warnings {
			fmt.Fprintln(app.Err, warning)
		}
		if loaded.Config.Auth.Mode == "required" && set.Len() == 0 {
			return fmt.Errorf("auth.mode=required needs at least one valid token")
		}
		key, err := upstream.ReadKey(loaded.Config.Upstream)
		if err != nil {
			return err
		}
		if key == "" {
			fmt.Fprintln(app.Err, "upstream key is empty")
		}
		if loaded.Config.Upstream.APIKeyFile != "" {
			loaded.Config.Upstream.APIKey = key
			loaded.Provenance["upstream.api_key"] = "file"
		}
		if err = app.printConfig(loaded, set.Names()); err != nil {
			return err
		}
		if !probe {
			return nil
		}
	}
	key, err := upstream.ReadKey(loaded.Config.Upstream)
	if err != nil {
		return err
	}
	client, err := upstream.New(loaded.Config.Upstream, key, nil, nil)
	if err != nil {
		return err
	}
	defer client.Close()
	models, warnings, err := client.Discover(ctx, loaded.Config)
	if err != nil {
		return &exitError{2, err}
	}
	for _, warning := range warnings {
		fmt.Fprintln(app.Err, warning)
	}
	cat, warnings, err := catalog.Build(loaded.Config, models, nil, time.Now())
	if err != nil {
		return &exitError{2, err}
	}
	for _, warning := range warnings {
		fmt.Fprintln(app.Err, warning)
	}
	if command == "check" {
		_, err = fmt.Fprintf(app.Out, "catalog: %d models\n", cat.Len())
		return err
	}
	if asJSON {
		type row struct {
			Name          string   `json:"name"`
			Upstream      string   `json:"upstream"`
			Capabilities  []string `json:"capabilities"`
			ContextLength int      `json:"context_length"`
			Source        []string `json:"source"`
		}
		rows := []row{}
		for _, entry := range cat.Entries() {
			rows = append(rows, row{entry.Name, entry.Target, entry.Capabilities, entry.Details.ContextLength, entry.Provenance})
		}
		return json.NewEncoder(app.Out).Encode(rows)
	}
	writer := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tUPSTREAM\tCAPS\tCTX\tSOURCE")
	for _, entry := range cat.Entries() {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\n", entry.Name, entry.Target, strings.Join(entry.Capabilities, ","), entry.Details.ContextLength, strings.Join(entry.Provenance, "+"))
	}
	return writer.Flush()
}
func (app App) help() error {
	_, err := fmt.Fprintln(app.Out, `Usage: ollame <command> [flags]

Commands:
  serve              Run the API and admin listeners (default)
  check [--probe]      Validate configuration and show redacted provenance
  models [--json]      Fetch and display the exposed model catalog
  token new <name>    Generate a token and its SHA-256 digest
  token hash         Hash a token read from standard input (at most 4KiB)
  version            Show binary and Ollama compatibility versions
  completion <shell> Print bash, zsh, or fish command/flag completions

Examples:
  ollame check -c ollame.toml
  ollame check -u http://localhost:4000/v1 --probe
  ollame models --auth-mode disabled --upstream http://localhost:4000/v1
  ollame token new ci

Run 'ollame check --help' or 'ollame models --help' for configuration flags.`)
	return err
}
func (app App) token(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ollame token new <name> | hash")
	}
	switch args[0] {
	case "--help", "-h":
		_, err := fmt.Fprintln(app.Out, "Usage: ollame token new <name> | hash\nGenerate a named token, or hash up to 4KiB from stdin. Neither command loads config.\nExample: ollame token new ci")
		return err
	case "new":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := fmt.Fprintln(app.Out, "Usage: ollame token new <name>\nNames: [a-z0-9][a-z0-9._-]{0,62}. Prints plaintext and SHA-256 once.\nExample: ollame token new ci")
			return err
		}
		if len(args) != 2 {
			return fmt.Errorf("usage: ollame token new <name>")
		}
		token, digest, err := auth.Generate(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(app.Out, "%s\n%s\n", token, digest)
		return err
	case "hash":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := fmt.Fprintln(app.Out, "Usage: ollame token hash\nReads at most 4KiB from stdin, trims one trailing newline, and prints SHA-256.\nExample: ollame token hash < token.txt")
			return err
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: ollame token hash")
		}
		digest, err := auth.Hash(app.In)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(app.Out, digest)
		return err
	default:
		return fmt.Errorf("unknown token command")
	}
}
func parseFlags(command string, args, environment []string, out io.Writer) (config.Input, string, bool, bool, bool, error) {
	input := config.Input{Env: environment, Flags: map[string]string{}}
	flags := pflag.NewFlagSet(command, pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := ""
	probe := false
	asJSON := false
	help := false
	flags.StringVarP(&path, "config", "c", "", "Configuration file (otherwise search standard paths)")
	flags.BoolVarP(&help, "help", "h", false, "Show command help and configuration flags")
	if command == "check" {
		flags.BoolVar(&probe, "probe", false, "Also probe the upstream catalog")
	}
	if command == "models" {
		flags.BoolVar(&asJSON, "json", false, "Print catalog JSON")
	}
	flags.StringArrayVar(&input.Tokens, "token", nil, "Named token as name=value; repeatable (prefer files)")
	flags.StringArrayVar(&input.Aliases, "alias", nil, "Simple name=target alias; repeatable")
	values := map[string]*[]string{}
	for _, key := range config.Keys() {
		var entries []string
		name := key.Flag
		flags.StringArrayVar(&entries, name, nil, key.Help)
		if _, ok := key.Value.(bool); ok {
			flags.Lookup(name).NoOptDefVal = "true"
		}
		values[key.Path] = &entries
	}
	var shortUpstream, shortListen string
	flags.StringVarP(&shortUpstream, "upstream", "u", "", "Shorthand for --upstream-base-url")
	flags.StringVarP(&shortListen, "listen", "l", "", "Shorthand for --server-listen")
	if err := flags.Parse(args); err != nil {
		return input, "", false, false, false, fmt.Errorf("invalid command flags; use --help")
	}
	if help {
		example := map[string]string{"serve": "ollame serve --config ollame.toml", "check": "ollame check --config ollame.toml --probe", "models": "ollame models -u http://localhost:4000/v1 --json"}[command]
		_, err := fmt.Fprintf(out, "Usage: ollame %s [flags]\n\nExample: %s\n\n%s", command, example, flags.FlagUsages())
		return input, "", false, false, true, err
	}
	if flags.NArg() > 0 {
		return input, "", false, false, false, fmt.Errorf("unexpected positional argument")
	}
	for _, key := range config.Keys() {
		entries := *values[key.Path]
		if len(entries) == 0 {
			continue
		}
		if _, ok := key.Value.([]string); ok {
			input.Flags[key.Path] = strings.Join(entries, ",")
		} else {
			input.Flags[key.Path] = entries[len(entries)-1]
		}
	}
	if flags.Changed("upstream") {
		if flags.Changed("upstream-base-url") {
			return input, "", false, false, false, fmt.Errorf("use only one upstream URL flag")
		}
		input.Flags["upstream.base_url"] = shortUpstream
	}
	if flags.Changed("listen") {
		if flags.Changed("server-listen") {
			return input, "", false, false, false, fmt.Errorf("use only one listen flag")
		}
		input.Flags["server.listen"] = shortListen
	}
	if path == "" {
		path = envMap(environment)["OLLAME_CONFIG"]
	}
	if path == "" {
		env := envMap(environment)
		xdg := env["XDG_CONFIG_HOME"]
		if xdg == "" && env["HOME"] != "" {
			xdg = filepath.Join(env["HOME"], ".config")
		}
		candidates := []string{"ollame.toml"}
		if xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "ollame", "ollame.toml"))
		}
		candidates = append(candidates, "/etc/ollame/ollame.toml")
		for _, candidate := range candidates {
			_, err := os.Stat(candidate)
			if err == nil {
				path = candidate
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return input, "", false, false, false, fmt.Errorf("cannot inspect configuration path")
			}
		}
	}
	return input, path, probe, asJSON, false, nil
}
func envMap(environment []string) map[string]string {
	result := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
func (app App) printConfig(loaded config.Loaded, tokens []string) error {
	redacted := loaded.Config.Redacted()
	writer := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "KEY\tVALUE\tSOURCE")
	for _, key := range config.Keys() {
		section, field, _ := strings.Cut(key.Path, ".")
		group, ok := redacted[section].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid config display section")
		}
		value, err := json.Marshal(group[field])
		if err != nil {
			return err
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\n", key.Path, value, loaded.Provenance[key.Path])
	}
	for _, field := range []string{"alias", "override"} {
		group, ok := redacted["models"].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid models display")
		}
		value, err := json.Marshal(group[field])
		if err != nil {
			return err
		}
		fmt.Fprintf(writer, "models.%s\t%s\t%s\n", field, value, loaded.Provenance["models."+field])
	}
	fmt.Fprintf(writer, "auth.tokens\t%d (%s)\tmerged\n", len(tokens), strings.Join(tokens, ","))
	return writer.Flush()
}
func (app App) completion(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(app.Out, "Usage: ollame completion bash|zsh|fish\nPrint command and flag completions for the selected shell. No config or network access.\nExample: ollame completion bash > ollame.bash")
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: ollame completion bash|zsh|fish")
	}
	commands := []string{"serve", "check", "models", "token", "version", "completion"}
	options := []string{"--config", "--help", "--probe", "--json", "--token", "--alias", "--upstream", "--listen"}
	for _, key := range config.Keys() {
		options = append(options, "--"+key.Flag)
	}
	sort.Strings(options)
	switch args[0] {
	case "bash":
		_, err := fmt.Fprintf(app.Out, "complete -W '%s %s' ollame\n", strings.Join(commands, " "), strings.Join(options, " "))
		return err
	case "zsh":
		_, err := fmt.Fprintf(app.Out, "#compdef ollame\n_arguments '*:argument:(%s %s)'\n", strings.Join(commands, " "), strings.Join(options, " "))
		return err
	case "fish":
		for _, command := range commands {
			if _, err := fmt.Fprintf(app.Out, "complete -c ollame -f -a '%s'\n", command); err != nil {
				return err
			}
		}
		for _, option := range options {
			if _, err := fmt.Fprintf(app.Out, "complete -c ollame -l '%s'\n", strings.TrimPrefix(option, "--")); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("supported shells: bash, zsh, fish")
	}
}
