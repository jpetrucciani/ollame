package config

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
	"github.com/pelletier/go-toml/v2"
)

// Input contains process-static values and the current TOML bytes. Reading files
// belongs to the caller so reloads can distinguish source failures from removal.
type Input struct {
	TOML    []byte
	Env     []string
	Flags   map[string]string
	Tokens  []string
	Aliases []string
}

type Loaded struct {
	Config     Config
	Provenance map[string]string
	Warnings   []string
	EnvTokens  []Token
	FlagTokens []Token
}

type Key struct {
	Path   string
	Flag   string
	Env    string
	Help   string
	Secret bool
	Value  any
}

func Keys() []Key {
	defaults := reflect.ValueOf(Defaults())
	var keys []Key
	for i := 0; i < defaults.NumField(); i++ {
		section := defaults.Type().Field(i).Tag.Get("toml")
		group := defaults.Field(i)
		for j := 0; j < group.NumField(); j++ {
			field := group.Type().Field(j)
			help := field.Tag.Get("help")
			if help == "" {
				continue
			}
			path := section + "." + field.Tag.Get("toml")
			keys = append(keys, Key{Path: path, Flag: strings.NewReplacer(".", "-", "_", "-").Replace(path), Env: "OLLAME_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_")), Help: help, Secret: field.Tag.Get("secret") == "true", Value: group.Field(j).Interface()})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Path < keys[j].Path })
	return keys
}

var ErrInvalid = errors.New("invalid configuration")

func Load(input Input) (result Loaded, loadErr error) {
	defer func() {
		if loadErr != nil {
			loadErr = fmt.Errorf("%w: %s", ErrInvalid, loadErr)
		}
	}()
	result = Loaded{Config: Defaults(), Provenance: map[string]string{}}
	defaults, err := toml.Marshal(Defaults())
	if err != nil {
		return result, fmt.Errorf("encode defaults: %w", err)
	}
	values := map[string]any{}
	if err = toml.Unmarshal(defaults, &values); err != nil {
		return result, fmt.Errorf("decode defaults: %w", err)
	}
	k := koanf.New(".")
	if err = k.Load(confmap.Provider(values, ""), nil); err != nil {
		return result, err
	}
	keys := Keys()
	byEnv := map[string]Key{}
	byPath := map[string]Key{}
	for _, key := range keys {
		result.Provenance[key.Path] = "default"
		byEnv[key.Env] = key
		byPath[key.Path] = key
	}
	if len(input.TOML) > 0 {
		var check Config
		if err = toml.NewDecoder(bytes.NewReader(input.TOML)).DisallowUnknownFields().Decode(&check); err != nil {
			// Decoder diagnostics may embed secret-bearing source lines.
			return result, fmt.Errorf("invalid TOML configuration (syntax, type, or unknown key)")
		}
		file := map[string]any{}
		if err = toml.Unmarshal(input.TOML, &file); err != nil {
			return result, fmt.Errorf("invalid TOML configuration")
		}
		source := koanf.New(".")
		if err = source.Load(confmap.Provider(file, ""), nil); err != nil {
			return result, err
		}
		for _, key := range keys {
			if source.Exists(key.Path) {
				result.Provenance[key.Path] = "toml"
			}
		}
		for _, key := range []string{"auth.token", "models.alias", "models.override"} {
			if source.Exists(key) {
				result.Provenance[key] = "toml"
			}
		}
		if err = k.Merge(source); err != nil {
			return result, fmt.Errorf("merge TOML configuration: %w", err)
		}
	}
	var envAliases []string
	for _, variable := range input.Env {
		name, value, ok := strings.Cut(variable, "=")
		if !ok || !strings.HasPrefix(name, "OLLAME_") {
			continue
		}
		switch name {
		case "OLLAME_CONFIG":
			continue
		case "OLLAME_AUTH_TOKENS":
			result.EnvTokens, err = ParseTokens(splitList(value))
			if err != nil {
				return result, err
			}
			continue
		case "OLLAME_MODELS_OVERRIDES":
			var overrides []Override
			decoder := json.NewDecoder(strings.NewReader(value))
			decoder.DisallowUnknownFields()
			if !json.Valid([]byte(value)) || decoder.Decode(&overrides) != nil || overrides == nil {
				return result, fmt.Errorf("%s: expected a JSON array with valid model override fields and types", name)
			}
			for _, override := range overrides {
				if override.Match == "" {
					return result, fmt.Errorf("%s: each override requires a nonempty match", name)
				}
			}
			if err = k.Set("models.override", overrides); err != nil {
				return result, err
			}
			result.Provenance["models.override"] = "env"
			continue
		case "OLLAME_MODELS_ALIASES":
			envAliases = splitList(value)
			continue
		}
		key, ok := byEnv[name]
		if !ok {
			result.Warnings = append(result.Warnings, "unknown environment variable "+name)
			continue
		}
		decoded, err := parseValue(key, value)
		if err != nil {
			return result, fmt.Errorf("%s: invalid value", name)
		}
		if err = k.Set(key.Path, decoded); err != nil {
			return result, err
		}
		result.Provenance[key.Path] = "env"
	}
	paths := make([]string, 0, len(input.Flags))
	for path := range input.Flags {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		key, ok := byPath[path]
		if !ok {
			return result, fmt.Errorf("unknown configuration flag %s", path)
		}
		decoded, err := parseValue(key, input.Flags[path])
		if err != nil {
			return result, fmt.Errorf("%s: invalid value", key.Flag)
		}
		if err = k.Set(path, decoded); err != nil {
			return result, err
		}
		result.Provenance[path] = "flag"
	}
	result.FlagTokens, err = ParseTokens(input.Tokens)
	if err != nil {
		return result, err
	}
	encoded, err := toml.Marshal(k.Raw())
	if err != nil {
		return result, fmt.Errorf("encode merged configuration: %w", err)
	}
	if err = toml.NewDecoder(bytes.NewReader(encoded)).DisallowUnknownFields().Decode(&result.Config); err != nil {
		return result, fmt.Errorf("invalid merged configuration types")
	}
	for _, source := range []struct {
		name    string
		aliases []string
	}{{"env", envAliases}, {"flag", input.Aliases}} {
		if len(source.aliases) == 0 {
			continue
		}
		if err = mergeAliases(&result.Config, source.aliases); err != nil {
			return result, err
		}
		result.Provenance["models.alias"] = source.name
	}
	return result, nil
}

func splitList(value string) []string {
	if value == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseValue(key Key, text string) (any, error) {
	value := reflect.New(reflect.TypeOf(key.Value))
	if decoder, ok := value.Interface().(encoding.TextUnmarshaler); ok {
		if err := decoder.UnmarshalText([]byte(text)); err != nil {
			return nil, err
		}
		encoder, ok := value.Interface().(encoding.TextMarshaler)
		if !ok {
			return nil, fmt.Errorf("missing text encoder")
		}
		encoded, err := encoder.MarshalText()
		return string(encoded), err
	}
	switch value.Elem().Kind() {
	case reflect.String:
		return text, nil
	case reflect.Bool:
		return strconv.ParseBool(text)
	case reflect.Int:
		return strconv.Atoi(text)
	case reflect.Slice:
		return splitList(text), nil
	case reflect.Map:
		var headers map[string]string
		if err := json.Unmarshal([]byte(text), &headers); err != nil || headers == nil {
			return nil, fmt.Errorf("expected JSON string map")
		}
		return headers, nil
	default:
		return nil, fmt.Errorf("unsupported config type")
	}
}

func ParseTokens(entries []string) ([]Token, error) {
	tokens := make([]Token, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !ValidTokenName(name) || value == "" || seen[name] {
			return nil, fmt.Errorf("invalid or duplicate token entry")
		}
		token := Token{Name: name}
		if hash, ok := strings.CutPrefix(value, "sha256:"); ok {
			token.SHA256 = hash
		} else {
			token.Token = value
		}
		if err := validateToken(token); err != nil {
			return nil, err
		}
		seen[name] = true
		tokens = append(tokens, token)
	}
	return tokens, nil
}

func mergeAliases(cfg *Config, entries []string) error {
	seen := map[string]bool{}
	for _, entry := range entries {
		name, target, ok := strings.Cut(entry, "=")
		if !ok || target == "" {
			return fmt.Errorf("invalid alias entry")
		}
		normalized, err := NormalizeAlias(name, cfg.Models.DefaultTag)
		if err != nil {
			return err
		}
		if seen[normalized] {
			return fmt.Errorf("duplicate alias in one source")
		}
		seen[normalized] = true
		replaced := false
		for i := range cfg.Models.Aliases {
			prior, err := NormalizeAlias(cfg.Models.Aliases[i].Name, cfg.Models.DefaultTag)
			if err != nil {
				return err
			}
			if prior == normalized {
				cfg.Models.Aliases[i].Target = target
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Models.Aliases = append(cfg.Models.Aliases, Alias{Name: normalized, Target: target})
		}
	}
	return nil
}
