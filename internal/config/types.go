// Package config loads and validates the complete serving configuration.
package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Duration time.Duration

func (d Duration) String() string               { return time.Duration(d).String() }
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration")
	}
	*d = Duration(value)
	return nil
}
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

type Size int64

func (s Size) String() string               { return strconv.FormatInt(int64(s), 10) + "B" }
func (s Size) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
func (s Size) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }
func (s *Size) UnmarshalText(text []byte) error {
	value := string(text)
	for _, unit := range []struct {
		name  string
		scale int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1000}, {"B", 1}} {
		if !strings.HasSuffix(value, unit.name) {
			continue
		}
		count, err := strconv.ParseInt(strings.TrimSuffix(value, unit.name), 10, 64)
		if err != nil || count < 0 || count > math.MaxInt64/unit.scale {
			return fmt.Errorf("invalid size")
		}
		*s = Size(count * unit.scale)
		return nil
	}
	return fmt.Errorf("size requires B, KB, KiB, MB, MiB, GB, or GiB")
}

type Config struct {
	Server      Server      `toml:"server"`
	Upstream    Upstream    `toml:"upstream"`
	Auth        Auth        `toml:"auth"`
	Models      Models      `toml:"models"`
	Compat      Compat      `toml:"compat"`
	Generate    Generate    `toml:"generate"`
	Embed       Embed       `toml:"embed"`
	Management  Management  `toml:"management"`
	Passthrough Passthrough `toml:"passthrough"`
	Limits      Limits      `toml:"limits"`
	Admin       Admin       `toml:"admin"`
	Log         Log         `toml:"log"`
	Metrics     Metrics     `toml:"metrics"`
}

type Server struct {
	Listen            string   `toml:"listen" help:"API address or unix:/path.sock"`
	AdminListen       string   `toml:"admin_listen" help:"Admin listen address; empty disables it"`
	MaxBodyBytes      Size     `toml:"max_body_bytes" help:"Maximum inbound body size"`
	MaxHeaderBytes    Size     `toml:"max_header_bytes" help:"Maximum inbound header size"`
	WriteStallTimeout Duration `toml:"write_stall_timeout" help:"Maximum blocked downstream write time"`
	DrainDelay        Duration `toml:"drain_delay" help:"Accept traffic after readiness drops for this duration"`
	ReadHeaderTimeout Duration `toml:"read_header_timeout" help:"Maximum inbound header read time"`
	IdleTimeout       Duration `toml:"idle_timeout" help:"HTTP keep-alive idle timeout"`
	ShutdownGrace     Duration `toml:"shutdown_grace" help:"Time to drain after closing the listener"`
	MaxInflight       int      `toml:"max_inflight" help:"Concurrent inference limit; zero is unlimited"`
	CORSOrigins       []string `toml:"cors_origins" help:"Allowed browser origins"`
	TLSCert           string   `toml:"tls_cert" help:"API TLS certificate path"`
	TLSKey            string   `toml:"tls_key" help:"API TLS private key path"`
	RootBanner        string   `toml:"root_banner" help:"Root health banner"`
}

type Upstream struct {
	BaseURL             string            `toml:"base_url" help:"OpenAI-compatible base URL including /v1"`
	APIKey              string            `toml:"api_key" secret:"true" help:"Upstream API key; prefer api-key-file"`
	APIKeyFile          string            `toml:"api_key_file" help:"Runtime upstream API key file"`
	Flavor              string            `toml:"flavor" help:"Upstream flavor: litellm or openai"`
	ModelInfo           string            `toml:"model_info" help:"Metadata enrichment: auto, litellm, or off"`
	ConnectTimeout      Duration          `toml:"connect_timeout" help:"Upstream connection deadline"`
	HeaderTimeout       Duration          `toml:"header_timeout" help:"Upstream response header deadline"`
	IdleTimeout         Duration          `toml:"idle_timeout" help:"Maximum gap between SSE events"`
	RequestTimeout      Duration          `toml:"request_timeout" help:"Positive total request deadline including retries"`
	BodyTimeout         Duration          `toml:"body_timeout" help:"Non-SSE body read deadline"`
	Retries             int               `toml:"retries" help:"Additional attempts for eligible upstream failures"`
	StreamMode          string            `toml:"stream_mode" help:"Upstream streaming: always or match"`
	MaxTokensField      string            `toml:"max_tokens_field" help:"Token limit field: max_tokens or max_completion_tokens"`
	ForwardClientAsUser bool              `toml:"forward_client_as_user" help:"Attribute upstream requests to token names"`
	UserPrefix          string            `toml:"user_prefix" help:"Prefix for upstream user attribution"`
	Tags                []string          `toml:"tags" help:"LiteLLM attribution tags"`
	ExtraHeaders        map[string]string `toml:"extra_headers" secret:"true" help:"Static upstream headers as a JSON object"`
	CAFile              string            `toml:"ca_file" help:"Additional upstream CA bundle path"`
	InsecureSkipVerify  bool              `toml:"insecure_skip_verify" help:"Disable upstream TLS verification"`
	MaxIdleConns        int               `toml:"max_idle_conns" help:"Upstream idle connection pool size"`
}

type Token struct {
	Name      string `toml:"name"`
	Token     string `toml:"token,omitempty" secret:"true"`
	SHA256    string `toml:"sha256,omitempty" secret:"true"`
	TokenFile string `toml:"token_file,omitempty"`
	TokenEnv  string `toml:"token_env,omitempty"`
}

type Auth struct {
	Mode           string   `toml:"mode" help:"Inbound auth: required, optional, or disabled"`
	TokensFile     string   `toml:"tokens_file" help:"Runtime name=token file"`
	TokensDir      string   `toml:"tokens_dir" help:"Runtime directory containing named tokens"`
	ReloadInterval Duration `toml:"reload_interval" help:"Config and secret polling interval"`
	StaleGrace     Duration `toml:"stale_grace" help:"Retention after a token source starts failing"`
	PublicPaths    []string `toml:"public_paths" help:"Exact unauthenticated paths"`
	AcceptBasic    bool     `toml:"accept_basic" help:"Accept tokens through HTTP Basic auth"`
	AcceptXAPIKey  bool     `toml:"accept_x_api_key" help:"Accept x-api-key credentials"`
	Tokens         []Token  `toml:"token,omitempty"`
}

type Behavior struct {
	SortModelMessages    *bool          `toml:"sort_model_messages,omitempty" json:"sort_model_messages,omitempty"`
	Family               *string        `toml:"family,omitempty" json:"family,omitempty"`
	ContextLength        *int           `toml:"context_length,omitempty" json:"context_length,omitempty"`
	Capabilities         *[]string      `toml:"capabilities,omitempty" json:"capabilities,omitempty"`
	AddCapabilities      []string       `toml:"add_capabilities,omitempty" json:"add_capabilities,omitempty"`
	RemoveCapabilities   []string       `toml:"remove_capabilities,omitempty" json:"remove_capabilities,omitempty"`
	ParameterSize        *string        `toml:"parameter_size,omitempty" json:"parameter_size,omitempty"`
	QuantizationLevel    *string        `toml:"quantization_level,omitempty" json:"quantization_level,omitempty"`
	EmbeddingLength      *int           `toml:"embedding_length,omitempty" json:"embedding_length,omitempty"`
	ThinkStyle           *string        `toml:"think_style,omitempty" json:"think_style,omitempty"`
	ThinkOn              *string        `toml:"think_on,omitempty" json:"think_on,omitempty"`
	ThinkOff             *string        `toml:"think_off,omitempty" json:"think_off,omitempty"`
	ThinkMax             *string        `toml:"think_max,omitempty" json:"think_max,omitempty"`
	ThinkTags            *bool          `toml:"think_tags,omitempty" json:"think_tags,omitempty"`
	ThinkInitial         *bool          `toml:"think_initial,omitempty" json:"think_initial,omitempty"`
	ForwardSamplerExtras *bool          `toml:"forward_sampler_extras,omitempty" json:"forward_sampler_extras,omitempty"`
	DropParams           []string       `toml:"drop_params,omitempty" json:"drop_params,omitempty"`
	MaxTokensField       *string        `toml:"max_tokens_field,omitempty" json:"max_tokens_field,omitempty"`
	FIM                  *string        `toml:"fim,omitempty" json:"fim,omitempty"`
	FIMTemplate          *string        `toml:"fim_template,omitempty" json:"fim_template,omitempty"`
	ExtraBody            map[string]any `toml:"extra_body,omitempty" json:"extra_body,omitempty"`
}

type Alias struct {
	Name       string         `toml:"name" json:"name"`
	Target     string         `toml:"target" json:"target"`
	System     string         `toml:"system,omitempty" json:"system,omitempty"`
	Options    map[string]any `toml:"options,omitempty" json:"options,omitempty"`
	Think      any            `toml:"think,omitempty" json:"think,omitempty"`
	HideTarget bool           `toml:"hide_target,omitempty" json:"hide_target,omitempty"`
	Behavior
}

type Override struct {
	Match string `toml:"match" json:"match"`
	Behavior
}

type Models struct {
	RefreshInterval      Duration   `toml:"refresh_interval" help:"Periodic model discovery interval"`
	FetchTimeout         Duration   `toml:"fetch_timeout" help:"Deadline per catalog discovery or metadata call"`
	RequireOnStart       bool       `toml:"require_on_start" help:"Gate readiness on initial catalog discovery"`
	RefreshOnMiss        bool       `toml:"refresh_on_miss" help:"Refresh the catalog once for an unknown model"`
	Include              []string   `toml:"include" help:"Full upstream ID inclusion globs"`
	Exclude              []string   `toml:"exclude" help:"Full upstream ID exclusion globs"`
	Modes                []string   `toml:"modes" help:"Allowed known upstream modes"`
	DefaultTag           string     `toml:"default_tag" help:"Tag appended to untagged exposed names"`
	DefaultContextLength int        `toml:"default_context_length" help:"Context length when no metadata is available"`
	DefaultCapabilities  []string   `toml:"default_capabilities" help:"Advertised defaults for unknown capabilities"`
	Architecture         string     `toml:"architecture" help:"Override general.architecture for synthetic metadata"`
	Format               string     `toml:"format" help:"Synthetic model format"`
	AdvertiseRemote      bool       `toml:"advertise_remote" help:"Advertise upstream host and model metadata"`
	Aliases              []Alias    `toml:"alias,omitempty"`
	Overrides            []Override `toml:"override,omitempty"`
}

type Compat struct {
	SortModelMessages    bool   `toml:"sort_model_messages" help:"Combine system messages at the beginning of translated conversations"`
	OllamaVersion        string `toml:"ollama_version" help:"Pinned Ollama compatibility version"`
	StrictOptions        bool   `toml:"strict_options" help:"Reject unknown Ollama options"`
	StrictCapabilities   bool   `toml:"strict_capabilities" help:"Reject known-unsupported requested capabilities"`
	ThinkStyle           string `toml:"think_style" help:"Thinking mapping: reasoning_effort, chat_template_kwargs, or none"`
	ThinkOn              string `toml:"think_on" help:"Reasoning effort used for think=true"`
	ThinkOff             string `toml:"think_off" help:"Reasoning disable policy: omit, none, or disable"`
	ThinkMax             string `toml:"think_max" help:"Reasoning effort used for think=max"`
	ThinkTags            bool   `toml:"think_tags" help:"Extract the first thinking tag block"`
	ThinkInitial         bool   `toml:"think_initial" help:"Start inside a template-supplied thinking block"`
	HistoryThinking      string `toml:"history_thinking" help:"Historical reasoning policy: drop or reasoning_content"`
	OrphanToolResult     string `toml:"orphan_tool_result" help:"Orphan tool result policy: user_message or error"`
	BadToolArguments     string `toml:"bad_tool_arguments" help:"Invalid tool argument policy: wrap or error"`
	MissingToolResult    string `toml:"missing_tool_result" help:"Missing tool result policy: synthesize or passthrough"`
	JSONSchemaStrict     bool   `toml:"json_schema_strict" help:"Request strict upstream JSON schemas"`
	ForwardSamplerExtras bool   `toml:"forward_sampler_extras" help:"Forward additional sampling parameters"`
	DefaultImageMIME     string `toml:"default_image_mime" help:"Image MIME fallback when magic bytes are unknown"`
	EstimateTokens       bool   `toml:"estimate_tokens" help:"Estimate token counts when upstream usage is absent"`
}

type Generate struct {
	Raw         string `toml:"raw" help:"Raw generate mode: completions, chat, or error"`
	FIM         string `toml:"fim" help:"Fill-in-middle mode: off, suffix, or template"`
	FIMTemplate string `toml:"fim_template" help:"Fill-in-middle template with prefix and suffix placeholders"`
}
type Embed struct {
	Normalize        bool `toml:"normalize" help:"Normalize modern embedding vectors"`
	LegacyNormalize  bool `toml:"legacy_normalize" help:"Normalize legacy embedding vectors"`
	BatchSize        int  `toml:"batch_size" help:"Maximum inputs per embedding call; zero disables splitting"`
	BatchConcurrency int  `toml:"batch_concurrency" help:"Embedding concurrency; only 1 is supported"`
}
type Management struct {
	Pull   string `toml:"pull" help:"Pull behavior: emulate or error"`
	Push   string `toml:"push" help:"Push behavior; only error is supported"`
	Create string `toml:"create" help:"Create behavior; only error is supported"`
	Copy   string `toml:"copy" help:"Copy behavior; only error is supported"`
	Delete string `toml:"delete" help:"Delete behavior; only error is supported"`
	PS     string `toml:"ps" help:"Running model listing: recent, empty, or catalog"`
}
type Passthrough struct {
	Enabled         bool     `toml:"enabled" help:"Enable registered OpenAI and Anthropic routes"`
	Paths           []string `toml:"paths" help:"Enabled registered passthrough paths"`
	ResponseHeaders []string `toml:"response_headers" help:"Upstream response header allowlist globs"`
}
type Limits struct {
	MaxAggregateBytes    Size `toml:"max_aggregate_bytes" help:"Maximum retained non-streaming output size"`
	MaxToolCalls         int  `toml:"max_tool_calls" help:"Maximum tool calls per response"`
	MaxToolCallBytes     Size `toml:"max_tool_call_bytes" help:"Maximum argument bytes per tool call"`
	MaxUpstreamJSONBytes Size `toml:"max_upstream_json_bytes" help:"Maximum non-SSE upstream response size"`
	MaxEmbedInputs       int  `toml:"max_embed_inputs" help:"Maximum embedding inputs per request"`
	MaxImages            int  `toml:"max_images" help:"Maximum images per request"`
	MaxCatalogModels     int  `toml:"max_catalog_models" help:"Maximum discovery and exposed catalog count"`
}
type Admin struct {
	Debug bool `toml:"debug" help:"Enable debug endpoints on the admin listener"`
}
type Log struct {
	Level  string `toml:"level" help:"Log level: debug, info, warn, or error"`
	Format string `toml:"format" help:"Log format: json or text"`
	Access bool   `toml:"access" help:"Log completed requests"`
	Bodies bool   `toml:"bodies" help:"Log bodies at debug level; avoid in production"`
}
type Metrics struct {
	Enabled     bool `toml:"enabled" help:"Enable Prometheus metrics"`
	TokenLabels bool `toml:"token_labels" help:"Include configured token names in metrics"`
}

func Defaults() Config {
	return Config{
		Server:   Server{Listen: ":11434", AdminListen: "127.0.0.1:9434", MaxBodyBytes: 64 << 20, MaxHeaderBytes: 64 << 10, WriteStallTimeout: Duration(30 * time.Second), DrainDelay: Duration(5 * time.Second), ReadHeaderTimeout: Duration(10 * time.Second), IdleTimeout: Duration(120 * time.Second), ShutdownGrace: Duration(time.Minute), RootBanner: "Ollama is running", CORSOrigins: []string{}},
		Upstream: Upstream{Flavor: "litellm", ModelInfo: "auto", ConnectTimeout: Duration(5 * time.Second), HeaderTimeout: Duration(300 * time.Second), IdleTimeout: Duration(120 * time.Second), RequestTimeout: Duration(time.Hour), BodyTimeout: Duration(120 * time.Second), Retries: 1, StreamMode: "always", MaxTokensField: "max_tokens", ForwardClientAsUser: true, UserPrefix: "ollame:", Tags: []string{"ollame"}, ExtraHeaders: map[string]string{}, MaxIdleConns: 256},
		Auth:     Auth{Mode: "required", ReloadInterval: Duration(30 * time.Second), StaleGrace: Duration(5 * time.Minute), PublicPaths: []string{"/", "/api/version"}, AcceptBasic: true, AcceptXAPIKey: true},
		Models:   Models{RefreshInterval: Duration(time.Minute), FetchTimeout: Duration(15 * time.Second), RequireOnStart: true, RefreshOnMiss: true, Include: []string{"*"}, Exclude: []string{}, Modes: []string{"chat", "completion", "embedding"}, DefaultTag: "latest", DefaultContextLength: 131072, DefaultCapabilities: []string{"completion", "tools"}, Format: "gguf"},
		Compat:   Compat{OllamaVersion: "0.34.0", ThinkStyle: "reasoning_effort", ThinkOn: "medium", ThinkOff: "omit", ThinkMax: "high", HistoryThinking: "drop", OrphanToolResult: "user_message", BadToolArguments: "wrap", MissingToolResult: "synthesize", DefaultImageMIME: "image/png"},
		Generate: Generate{Raw: "completions", FIM: "off"}, Embed: Embed{Normalize: true, BatchConcurrency: 1},
		Management:  Management{Pull: "emulate", Push: "error", Create: "error", Copy: "error", Delete: "error", PS: "recent"},
		Passthrough: Passthrough{Enabled: true, Paths: []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/models", "/v1/models/{model}", "/v1/responses", "/v1/responses/compact", "/v1/messages"}, ResponseHeaders: []string{"x-litellm-*", "retry-after", "x-request-id", "openai-*", "anthropic-*"}},
		Limits:      Limits{MaxAggregateBytes: 32 << 20, MaxToolCalls: 128, MaxToolCallBytes: 1 << 20, MaxUpstreamJSONBytes: 64 << 20, MaxEmbedInputs: 2048, MaxImages: 32, MaxCatalogModels: 10000},
		Log:         Log{Level: "info", Format: "json", Access: true}, Metrics: Metrics{Enabled: true, TokenLabels: true},
	}
}
