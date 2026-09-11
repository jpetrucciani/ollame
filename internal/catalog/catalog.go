// Package catalog builds the exposed model set and its only legal resolution paths.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/glob"
)

var (
	ErrNotFound       = errors.New("model not found")
	ErrModelRequired  = errors.New("model is required")
	ErrInvalidCatalog = errors.New("invalid catalog")
)

type Knowledge uint8

const (
	Unknown Knowledge = iota
	No
	Yes
)

type Model struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Info    Info   `json:"-"`
}
type Info struct {
	Mode              string
	MaxInputTokens    int
	MaxTokens         int
	SupportsTools     *bool
	SupportsVision    *bool
	SupportsReasoning *bool
	OutputVectorSize  int
}

type Details struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
	ContextLength     int      `json:"context_length,omitempty"`
	EmbeddingLength   int      `json:"embedding_length,omitempty"`
}
type Entry struct {
	Name           string               `json:"name"`
	Model          string               `json:"model"`
	Target         string               `json:"-"`
	ModifiedAt     time.Time            `json:"modified_at"`
	Size           int64                `json:"size"`
	Digest         string               `json:"digest"`
	Details        Details              `json:"details"`
	Capabilities   []string             `json:"capabilities,omitempty"`
	Alias          bool                 `json:"-"`
	System         string               `json:"-"`
	Options        map[string]any       `json:"-"`
	Think          any                  `json:"-"`
	Behavior       config.Behavior      `json:"-"`
	Knowledge      map[string]Knowledge `json:"-"`
	Architecture   string               `json:"-"`
	Provenance     []string             `json:"-"`
	explicitInsert bool
}

type Catalog struct {
	byName     map[string]Entry
	raw        map[string]string
	targets    map[string]bool
	defaultTag string
	firstSeen  map[string]time.Time
}

func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byName)
}
func (c *Catalog) AllowsTarget(target string) bool { return c != nil && c.targets[target] }
func (c *Catalog) Entries() []Entry {
	if c == nil {
		return []Entry{}
	}
	entries := make([]Entry, 0, len(c.byName))
	for _, entry := range c.byName {
		entries = append(entries, cloneEntry(entry))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
func (c *Catalog) Resolve(requested string) (Entry, error) {
	input := strings.TrimSpace(requested)
	if input == "" {
		return Entry{}, ErrModelRequired
	}
	if c == nil {
		return Entry{}, ErrNotFound
	}
	if entry, ok := c.byName[strings.ToLower(input)]; ok {
		return cloneEntry(entry), nil
	}
	if !strings.Contains(input, ":") {
		if entry, ok := c.byName[strings.ToLower(input+":"+c.defaultTag)]; ok {
			return cloneEntry(entry), nil
		}
	}
	if raw, ok := strings.CutSuffix(input, ":"+c.defaultTag); ok {
		if name, ok := c.raw[raw]; ok {
			return cloneEntry(c.byName[name]), nil
		}
	}
	if name, ok := c.raw[input]; ok {
		return cloneEntry(c.byName[name]), nil
	}
	return Entry{}, ErrNotFound
}

type matchingOverride struct {
	pattern  glob.Pattern
	behavior config.Behavior
	index    int
}

func Build(cfg config.Config, models []Model, previous *Catalog, now time.Time) (*Catalog, []string, error) {
	if len(models) > cfg.Limits.MaxCatalogModels {
		return nil, nil, fmt.Errorf("%w: discovery limit exceeded", ErrInvalidCatalog)
	}
	include, err := glob.CompileAll(cfg.Models.Include)
	if err != nil {
		return nil, nil, err
	}
	exclude, err := glob.CompileAll(cfg.Models.Exclude)
	if err != nil {
		return nil, nil, err
	}
	overrides := make([]matchingOverride, 0, len(cfg.Models.Overrides))
	for i, override := range cfg.Models.Overrides {
		pattern, err := glob.Compile(override.Match)
		if err != nil {
			return nil, nil, err
		}
		overrides = append(overrides, matchingOverride{pattern, override.Behavior, i})
	}
	result := &Catalog{byName: map[string]Entry{}, raw: map[string]string{}, targets: map[string]bool{}, defaultTag: strings.ToLower(cfg.Models.DefaultTag), firstSeen: map[string]time.Time{}}
	if previous != nil {
		for id, first := range previous.firstSeen {
			result.firstSeen[id] = first
		}
	}
	models = append([]Model(nil), models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	eligible := map[string]Entry{}
	warnings := []string{}
	seenIDs := map[string]bool{}
	for _, model := range models {
		if model.ID == "" || seenIDs[model.ID] {
			return nil, nil, fmt.Errorf("%w: empty or duplicate upstream ID", ErrInvalidCatalog)
		}
		seenIDs[model.ID] = true
		if !glob.Any(include, model.ID) || glob.Any(exclude, model.ID) || model.Info.Mode != "" && !slices.Contains(cfg.Models.Modes, model.Info.Mode) {
			continue
		}
		name := strings.ToLower(strings.ReplaceAll(model.ID, ":", "-")) + ":" + result.defaultTag
		entry := baseEntry(cfg, model, name)
		if model.Created != 0 {
			entry.ModifiedAt = time.Unix(model.Created, 0).UTC()
		} else {
			first, ok := result.firstSeen[model.ID]
			if !ok {
				first = now.UTC()
			}
			entry.ModifiedAt = first
			result.firstSeen[model.ID] = first
		}
		for _, override := range overrides {
			if override.pattern.Match(model.ID) {
				apply(&entry, override.behavior)
				entry.Provenance = append(entry.Provenance, "override:"+strconv.Itoa(override.index))
			}
		}
		if err := finish(&entry, cfg); err != nil {
			return nil, nil, err
		}
		eligible[model.ID] = entry
		if _, exists := result.byName[name]; exists {
			warnings = append(warnings, "derived name collision: "+name)
			continue
		}
		result.byName[name] = entry
	}
	aliases := map[string]bool{}
	hidden := map[string]bool{}
	for _, alias := range cfg.Models.Aliases {
		name, err := config.NormalizeAlias(alias.Name, result.defaultTag)
		if err != nil {
			return nil, nil, err
		}
		if aliases[name] {
			return nil, nil, fmt.Errorf("%w: duplicate alias", ErrInvalidCatalog)
		}
		aliases[name] = true
		target, ok := eligible[alias.Target]
		if !ok {
			warnings = append(warnings, "alias target absent or filtered: "+name)
			continue
		}
		entry := cloneEntry(target)
		entry.Name = name
		entry.Model = name
		entry.Alias = true
		entry.System = alias.System
		entry.Options = cloneMap(alias.Options)
		entry.Think = alias.Think
		apply(&entry, alias.Behavior)
		entry.Provenance = append(entry.Provenance, "alias:"+name)
		if err := finish(&entry, cfg); err != nil {
			return nil, nil, err
		}
		if _, exists := result.byName[name]; exists {
			warnings = append(warnings, "alias replaces derived name: "+name)
		}
		result.byName[name] = entry
		if alias.HideTarget {
			hidden[alias.Target] = true
		}
	}
	for name, entry := range result.byName {
		if !entry.Alias && hidden[entry.Target] {
			delete(result.byName, name)
			continue
		}
		result.targets[entry.Target] = true
		if !entry.Alias {
			result.raw[entry.Target] = name
		}
	}
	// Retain first-seen dates only for currently discovered IDs, bounding churn.
	for id := range result.firstSeen {
		if !seenIDs[id] {
			delete(result.firstSeen, id)
		}
	}
	if len(result.byName) > cfg.Limits.MaxCatalogModels {
		return nil, nil, fmt.Errorf("%w: exposed limit exceeded", ErrInvalidCatalog)
	}
	return result, warnings, nil
}

func baseEntry(cfg config.Config, model Model, name string) Entry {
	family := "ollame"
	lower := strings.ToLower(model.ID)
	for _, candidate := range []string{"claude", "gpt", "gemini", "gemma", "llama", "qwen", "mistral", "mixtral", "deepseek", "glm", "kimi", "minimax", "phi", "command", "nomic-bert"} {
		if strings.Contains(lower, candidate) {
			family = candidate
			break
		}
	}
	leaf := lower[strings.LastIndex(lower, "/")+1:]
	if len(leaf) >= 2 && leaf[0] == 'o' && leaf[1] >= '0' && leaf[1] <= '9' {
		family = "o"
	}
	context := cfg.Models.DefaultContextLength
	if model.Info.MaxInputTokens > 0 {
		context = model.Info.MaxInputTokens
	} else if model.Info.MaxTokens > 0 {
		context = model.Info.MaxTokens
	}
	entry := Entry{Name: name, Model: name, Target: model.ID, Details: Details{Format: cfg.Models.Format, Family: family, Families: []string{family}, ContextLength: context, EmbeddingLength: model.Info.OutputVectorSize}, Knowledge: map[string]Knowledge{}, Provenance: []string{"upstream"}}
	// Defaults only affect advertisement, not evidence used by strict checks.
	switch model.Info.Mode {
	case "embedding":
		entry.Knowledge["embedding"] = Yes
		entry.Knowledge["completion"] = No
	case "chat", "completion":
		entry.Knowledge["completion"] = Yes
		entry.Knowledge["embedding"] = No
	}
	for capability, value := range map[string]*bool{"tools": model.Info.SupportsTools, "vision": model.Info.SupportsVision, "thinking": model.Info.SupportsReasoning} {
		if value != nil {
			entry.Knowledge[capability] = No
			if *value {
				entry.Knowledge[capability] = Yes
			}
		}
	}
	for _, hint := range []string{"embed", "embedding", "bge-", "e5-", "nomic", "gte-"} {
		if strings.Contains(lower, hint) && entry.Knowledge["embedding"] == Unknown {
			entry.Knowledge["embedding"] = Yes
			break
		}
	}
	entry.Behavior = config.Behavior{SortModelMessages: ptr(cfg.Compat.SortModelMessages), ThinkStyle: ptr(cfg.Compat.ThinkStyle), ThinkOn: ptr(cfg.Compat.ThinkOn), ThinkOff: ptr(cfg.Compat.ThinkOff), ThinkMax: ptr(cfg.Compat.ThinkMax), ThinkTags: ptr(cfg.Compat.ThinkTags), ThinkInitial: ptr(cfg.Compat.ThinkInitial), ForwardSamplerExtras: ptr(cfg.Compat.ForwardSamplerExtras), MaxTokensField: ptr(cfg.Upstream.MaxTokensField), FIM: ptr(cfg.Generate.FIM), FIMTemplate: ptr(cfg.Generate.FIMTemplate)}
	return entry
}
func ptr[T any](value T) *T { return &value }
func apply(entry *Entry, override config.Behavior) {
	if override.Capabilities != nil || slices.Contains(override.AddCapabilities, "insert") || slices.Contains(override.RemoveCapabilities, "insert") {
		entry.explicitInsert = true
	}
	if override.Capabilities != nil {
		for _, capability := range allCapabilities() {
			entry.Knowledge[capability] = No
		}
		for _, capability := range *override.Capabilities {
			entry.Knowledge[capability] = Yes
		}
	}
	for _, capability := range override.AddCapabilities {
		entry.Knowledge[capability] = Yes
	}
	for _, capability := range override.RemoveCapabilities {
		entry.Knowledge[capability] = No
	}
	source := reflect.ValueOf(override)
	target := reflect.ValueOf(&entry.Behavior).Elem()
	for i := 0; i < source.NumField(); i++ {
		v := source.Field(i)
		if v.IsNil() {
			continue
		}
		switch v.Kind() {
		case reflect.Pointer:
			copy := reflect.New(v.Elem().Type())
			copy.Elem().Set(v.Elem())
			if v.Elem().Kind() == reflect.Slice {
				copy.Elem().Set(reflect.ValueOf(append([]string(nil), v.Elem().Interface().([]string)...)))
			}
			target.Field(i).Set(copy)
		case reflect.Slice:
			target.Field(i).Set(reflect.ValueOf(append([]string(nil), v.Interface().([]string)...)))
		case reflect.Map:
			entry.Behavior.ExtraBody = mergeMap(entry.Behavior.ExtraBody, override.ExtraBody)
		}
	}
	if override.Family != nil {
		entry.Details.Family = *override.Family
		entry.Details.Families = []string{*override.Family}
	}
	if override.ContextLength != nil {
		entry.Details.ContextLength = *override.ContextLength
	}
	if override.EmbeddingLength != nil {
		entry.Details.EmbeddingLength = *override.EmbeddingLength
	}
	if override.ParameterSize != nil {
		entry.Details.ParameterSize = *override.ParameterSize
	}
	if override.QuantizationLevel != nil {
		entry.Details.QuantizationLevel = *override.QuantizationLevel
	}
}
func finish(entry *Entry, cfg config.Config) error {
	b := entry.Behavior
	if b.ThinkInitial != nil && *b.ThinkInitial && (b.ThinkTags == nil || !*b.ThinkTags) {
		return fmt.Errorf("%w: think_initial requires think_tags", ErrInvalidCatalog)
	}
	if b.FIM != nil && *b.FIM == "template" && (b.FIMTemplate == nil || *b.FIMTemplate == "") {
		return fmt.Errorf("%w: FIM template missing", ErrInvalidCatalog)
	}
	if !entry.explicitInsert {
		entry.Knowledge["insert"] = Unknown
		if b.FIM != nil && *b.FIM != "off" {
			entry.Knowledge["insert"] = Yes
		}
	}
	entry.Architecture = cfg.Models.Architecture
	if entry.Architecture == "" {
		entry.Architecture = entry.Details.Family
	}
	entry.Capabilities = nil
	for _, capability := range allCapabilities() {
		state := entry.Knowledge[capability]
		if state == Yes || state == Unknown && slices.Contains(cfg.Models.DefaultCapabilities, capability) {
			entry.Capabilities = append(entry.Capabilities, capability)
		}
	}
	canonical, err := json.Marshal(struct {
		System       string
		Options      map[string]any
		Think        any
		Behavior     config.Behavior
		Details      Details
		Capabilities []string
		Architecture string
	}{entry.System, entry.Options, entry.Think, entry.Behavior, entry.Details, entry.Capabilities, entry.Architecture})
	if err != nil {
		return fmt.Errorf("%w: metadata serialization", ErrInvalidCatalog)
	}
	digest := sha256.Sum256(append([]byte("ollame\x00"+entry.Name+"\x00"+entry.Target+"\x00"), canonical...))
	entry.Digest = hex.EncodeToString(digest[:])
	return nil
}
func allCapabilities() []string {
	return []string{"completion", "tools", "insert", "vision", "embedding", "thinking", "image", "audio"}
}
func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	copy := make(map[string]any, len(value))
	for key, v := range value {
		copy[key] = cloneValue(v)
	}
	return copy
}
func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		copy := make([]any, len(v))
		for i := range v {
			copy[i] = cloneValue(v[i])
		}
		return copy
	case []string:
		return append([]string(nil), v...)
	case json.RawMessage:
		return append(json.RawMessage(nil), v...)
	default:
		return v
	}
}
func mergeMap(base, update map[string]any) map[string]any {
	result := cloneMap(base)
	if result == nil {
		result = map[string]any{}
	}
	for key, value := range update {
		nested, ok := value.(map[string]any)
		previous, wasMap := result[key].(map[string]any)
		if ok && wasMap {
			result[key] = mergeMap(previous, nested)
		} else {
			result[key] = cloneValue(value)
		}
	}
	return result
}
func cloneEntry(entry Entry) Entry {
	entry.Details.Families = append([]string(nil), entry.Details.Families...)
	entry.Capabilities = append([]string(nil), entry.Capabilities...)
	entry.Provenance = append([]string(nil), entry.Provenance...)
	entry.Options = cloneMap(entry.Options)
	knowledge := map[string]Knowledge{}
	for key, value := range entry.Knowledge {
		knowledge[key] = value
	}
	entry.Knowledge = knowledge
	b := entry.Behavior
	entry.Behavior = config.Behavior{}
	holder := Entry{Knowledge: map[string]Knowledge{}}
	apply(&holder, b)
	entry.Behavior = holder.Behavior
	return entry
}
