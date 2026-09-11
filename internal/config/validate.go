package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/jpetrucciani/ollame/internal/glob"
)

type Profile uint8

const (
	ServeProfile Profile = iota
	ModelsProfile
)

func ValidTokenName(name string) bool {
	if len(name) < 1 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func NormalizeAlias(name, tag string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || strings.Count(name, ":") > 1 || strings.ContainsAny(name, "?#\\") || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("invalid alias name")
	}
	base, suffix, hasTag := strings.Cut(name, ":")
	if base == "" || strings.Trim(base, "/") == "" {
		return "", fmt.Errorf("invalid alias name")
	}
	if !hasTag {
		suffix = tag
	}
	if suffix == "" || strings.ContainsAny(suffix, ":/ \\?#") || strings.ContainsFunc(suffix, unicode.IsControl) {
		return "", fmt.Errorf("invalid model tag")
	}
	return base + ":" + strings.ToLower(suffix), nil
}

func ValidThink(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return true
	case string:
		return slices.Contains([]string{"low", "medium", "high", "max"}, v)
	default:
		return false
	}
}

func (cfg Config) Validate(profile Profile) (validationErr error) {
	defer func() {
		if validationErr != nil {
			validationErr = fmt.Errorf("%w: %s", ErrInvalid, validationErr)
		}
	}()
	parsed, err := url.Parse(cfg.Upstream.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("upstream.base_url must be an HTTP(S) base URL without query or fragment")
	}
	if !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1") {
		return fmt.Errorf("upstream.base_url must include /v1")
	}
	if _, err = NormalizeAlias("check", cfg.Models.DefaultTag); err != nil {
		return err
	}
	for _, patterns := range [][]string{cfg.Models.Include, cfg.Models.Exclude} {
		if _, err = glob.CompileAll(patterns); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{"upstream.flavor": cfg.Upstream.Flavor, "upstream.model_info": cfg.Upstream.ModelInfo, "upstream.stream_mode": cfg.Upstream.StreamMode, "upstream.max_tokens_field": cfg.Upstream.MaxTokensField} {
		if err = validateEnum(name, value); err != nil {
			return err
		}
	}
	for _, capability := range cfg.Models.DefaultCapabilities {
		if !validCapability(capability) {
			return fmt.Errorf("invalid default capability")
		}
	}
	if cfg.Models.DefaultContextLength <= 0 || cfg.Models.RefreshInterval <= 0 || cfg.Models.FetchTimeout <= 0 {
		return fmt.Errorf("model sizes and intervals must be positive")
	}
	if cfg.Upstream.ConnectTimeout <= 0 || cfg.Upstream.HeaderTimeout <= 0 || cfg.Upstream.IdleTimeout <= 0 || cfg.Upstream.RequestTimeout <= 0 || cfg.Upstream.BodyTimeout <= 0 || cfg.Upstream.Retries < 0 || cfg.Upstream.MaxIdleConns < 0 {
		return fmt.Errorf("invalid upstream bounds")
	}
	for name, value := range cfg.Upstream.ExtraHeaders {
		if !validHeader(name) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid extra header")
		}
	}
	seen := map[string]bool{}
	for _, alias := range cfg.Models.Aliases {
		name, err := NormalizeAlias(alias.Name, cfg.Models.DefaultTag)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("duplicate normalized alias name")
		}
		seen[name] = true
		if strings.TrimSpace(alias.Target) == "" || !ValidThink(alias.Think) {
			return fmt.Errorf("invalid alias target or thinking default")
		}
		if err = validateBehavior(alias.Behavior); err != nil {
			return err
		}
	}
	for _, override := range cfg.Models.Overrides {
		if _, err = glob.Compile(override.Match); err != nil {
			return err
		}
		if err = validateBehavior(override.Behavior); err != nil {
			return err
		}
	}
	if profile == ModelsProfile {
		return nil
	}
	for name, value := range map[string]string{"auth.mode": cfg.Auth.Mode, "compat.think_style": cfg.Compat.ThinkStyle, "compat.think_off": cfg.Compat.ThinkOff, "compat.history_thinking": cfg.Compat.HistoryThinking, "compat.orphan_tool_result": cfg.Compat.OrphanToolResult, "compat.bad_tool_arguments": cfg.Compat.BadToolArguments, "compat.missing_tool_result": cfg.Compat.MissingToolResult, "generate.raw": cfg.Generate.Raw, "generate.fim": cfg.Generate.FIM, "management.pull": cfg.Management.Pull, "management.push": cfg.Management.Push, "management.create": cfg.Management.Create, "management.copy": cfg.Management.Copy, "management.delete": cfg.Management.Delete, "management.ps": cfg.Management.PS, "log.level": cfg.Log.Level, "log.format": cfg.Log.Format} {
		if err = validateEnum(name, value); err != nil {
			return err
		}
	}
	if cfg.Compat.ThinkInitial && !cfg.Compat.ThinkTags {
		return fmt.Errorf("think_initial requires think_tags")
	}
	if cfg.Generate.FIM == "template" && cfg.Generate.FIMTemplate == "" {
		return fmt.Errorf("template FIM requires fim_template")
	}
	if cfg.Embed.BatchConcurrency != 1 || cfg.Embed.BatchSize < 0 {
		return fmt.Errorf("embedding batches require concurrency 1 and nonnegative size")
	}
	if cfg.Server.Listen == "" || cfg.Server.MaxBodyBytes <= 0 || cfg.Server.MaxHeaderBytes <= 0 || cfg.Server.WriteStallTimeout <= 0 || cfg.Server.ReadHeaderTimeout <= 0 || cfg.Server.IdleTimeout <= 0 || cfg.Server.ShutdownGrace < 0 || cfg.Server.DrainDelay < 0 || cfg.Server.MaxInflight < 0 {
		return fmt.Errorf("invalid server bounds")
	}
	if (cfg.Server.TLSCert == "") != (cfg.Server.TLSKey == "") {
		return fmt.Errorf("TLS requires both certificate and key")
	}
	if strings.HasPrefix(cfg.Server.Listen, "unix:") && strings.TrimPrefix(cfg.Server.Listen, "unix:") == "" {
		return fmt.Errorf("Unix listener requires a path")
	}
	if cfg.Auth.ReloadInterval <= 0 || cfg.Auth.StaleGrace < 0 {
		return fmt.Errorf("invalid authentication intervals")
	}
	limits := reflect.ValueOf(cfg.Limits)
	for i := 0; i < limits.NumField(); i++ {
		if limits.Field(i).Int() <= 0 {
			return fmt.Errorf("all resource limits must be positive")
		}
	}
	seen = map[string]bool{}
	for _, token := range cfg.Auth.Tokens {
		if err = validateToken(token); err != nil {
			return err
		}
		if seen[token.Name] {
			return fmt.Errorf("duplicate inline token name")
		}
		seen[token.Name] = true
	}
	for _, path := range cfg.Auth.PublicPaths {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
			return fmt.Errorf("public paths must be exact absolute paths")
		}
	}
	allowed := append(Defaults().Passthrough.Paths, "/v1/audio/transcriptions")
	for _, path := range cfg.Passthrough.Paths {
		if !slices.Contains(allowed, path) {
			return fmt.Errorf("unregistered passthrough path")
		}
	}
	if _, err = glob.CompileAll(cfg.Passthrough.ResponseHeaders); err != nil {
		return err
	}
	return nil
}

func validateToken(token Token) error {
	if !ValidTokenName(token.Name) {
		return fmt.Errorf("invalid token name")
	}
	sources := 0
	for _, v := range []string{token.Token, token.SHA256, token.TokenFile, token.TokenEnv} {
		if v != "" {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("token needs exactly one credential source")
	}
	if token.SHA256 != "" {
		digest, err := hex.DecodeString(token.SHA256)
		if err != nil || len(digest) != 32 {
			return fmt.Errorf("invalid token SHA-256 digest")
		}
	}
	return nil
}

func validateEnum(name, value string) error {
	var allowed []string
	switch name {
	case "upstream.flavor":
		allowed = []string{"litellm", "openai"}
	case "upstream.model_info":
		allowed = []string{"auto", "litellm", "off"}
	case "upstream.stream_mode":
		allowed = []string{"always", "match"}
	case "upstream.max_tokens_field":
		allowed = []string{"max_tokens", "max_completion_tokens"}
	case "auth.mode":
		allowed = []string{"required", "optional", "disabled"}
	case "compat.think_style":
		allowed = []string{"reasoning_effort", "chat_template_kwargs", "none"}
	case "compat.think_off":
		allowed = []string{"omit", "none", "disable"}
	case "compat.history_thinking":
		allowed = []string{"drop", "reasoning_content"}
	case "compat.orphan_tool_result":
		allowed = []string{"user_message", "error"}
	case "compat.bad_tool_arguments":
		allowed = []string{"wrap", "error"}
	case "compat.missing_tool_result":
		allowed = []string{"synthesize", "passthrough"}
	case "generate.raw":
		allowed = []string{"completions", "chat", "error"}
	case "generate.fim":
		allowed = []string{"off", "suffix", "template"}
	case "management.pull":
		allowed = []string{"emulate", "error"}
	case "management.push", "management.create", "management.copy", "management.delete":
		allowed = []string{"error"}
	case "management.ps":
		allowed = []string{"recent", "empty", "catalog"}
	case "log.level":
		allowed = []string{"debug", "info", "warn", "error"}
	case "log.format":
		allowed = []string{"json", "text"}
	default:
		return fmt.Errorf("unknown enum %s", name)
	}
	if !slices.Contains(allowed, value) {
		return fmt.Errorf("%s: invalid enum", name)
	}
	return nil
}

func validCapability(value string) bool {
	return slices.Contains([]string{"completion", "tools", "insert", "vision", "embedding", "thinking", "image", "audio"}, value)
}
func validateBehavior(b Behavior) error {
	for _, caps := range [][]string{b.AddCapabilities, b.RemoveCapabilities} {
		for _, c := range caps {
			if !validCapability(c) {
				return fmt.Errorf("invalid override capability")
			}
		}
	}
	if b.Capabilities != nil {
		for _, c := range *b.Capabilities {
			if !validCapability(c) {
				return fmt.Errorf("invalid override capability")
			}
		}
	}
	for name, pointer := range map[string]*string{"compat.think_style": b.ThinkStyle, "compat.think_off": b.ThinkOff, "upstream.max_tokens_field": b.MaxTokensField, "generate.fim": b.FIM} {
		if pointer != nil {
			if err := validateEnum(name, *pointer); err != nil {
				return err
			}
		}
	}
	if b.ContextLength != nil && *b.ContextLength <= 0 || b.EmbeddingLength != nil && *b.EmbeddingLength < 0 {
		return fmt.Errorf("invalid override dimensions")
	}
	for _, key := range []string{"model", "messages", "prompt", "input", "stream", "stream_options", "n", "user"} {
		if _, ok := b.ExtraBody[key]; ok {
			return fmt.Errorf("extra_body touches protected field %s", key)
		}
	}
	if b.ParameterSize != nil && *b.ParameterSize != "" {
		valid, err := regexp.MatchString(`^[0-9]+(?:\.[0-9]+)?[KMBT]$`, *b.ParameterSize)
		if err != nil || !valid {
			return fmt.Errorf("invalid parameter_size")
		}
	}
	return nil
}

func validHeader(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
