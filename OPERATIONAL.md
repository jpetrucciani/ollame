# Operating ollame

Ollame accepts Ollama requests and sends inference to LiteLLM. Your clients use
ollame's address and client token; ollame uses a separate LiteLLM URL and key.
Models are hosted upstream. No GGUFs, model downloads, or GPU are needed in the
ollame container.

## Baseline deployment with Docker

Use a LiteLLM URL ending in `/v1`, for example `https://litellm.example.com/v1`.
Its key must be allowed to list and invoke the models you want exposed. The
container must be able to reach this URL.

Choose a built/published image tag:

```sh
IMAGE=ghcr.io/jpetrucciani/ollame:0.1.0-dev
```

That tag was built locally during development; registry publication is separate.
If it has not been published, build/load it from the project shell:

```sh
publish-ollame --release-version 0.1.0-dev --action build
```

See [packaging and publishing](docs/packaging.md) for building the helper outside
the shell or pushing a release to GHCR.

### 1. Create the environment file

```sh
install -d -m 700 .secrets
docker run --rm --network none "$IMAGE" token new local
```

The token command prints a plaintext token and its `sha256:` digest. Use the
plaintext token as the client credential below. Create `.secrets/ollame.env` with
these contents, replacing both credential placeholders and the upstream URL:

```dotenv
OLLAME_CONFIG=/dev/null
OLLAME_UPSTREAM_BASE_URL=https://litellm.example.com/v1
OLLAME_UPSTREAM_API_KEY=REPLACE_WITH_LITELLM_KEY
OLLAME_AUTH_MODE=required
OLLAME_AUTH_TOKENS=local=REPLACE_WITH_GENERATED_CLIENT_TOKEN
OLLAME_SERVER_LISTEN=:11434
OLLAME_SERVER_ADMIN_LISTEN=:9434
OLLAME_LOG_FORMAT=json
OLLAME_LOG_LEVEL=info
```

```sh
chmod 600 .secrets/ollame.env
```

Docker env files use literal `NAME=value` lines: do not add `export` or surround
values with shell quotes. The file is read by Docker on the host, so container
UID 65532 does not need permission to open it. `.secrets/` is ignored by this
repository. Environment credentials can be inspected by users with Docker access.
Use mounted secret files instead when you need file-based rotation or your secret
manager provides files; see below.

`OLLAME_CONFIG=/dev/null` selects an empty configuration for the check commands.
The service command below also passes `serve --config /dev/null` explicitly,
replacing the image's default command, which selects `/etc/ollame/ollame.toml`.
This keeps the deployment env-only.

### 2. Validate and probe

```sh
docker run --rm --env-file .secrets/ollame.env "$IMAGE" check
docker run --rm --env-file .secrets/ollame.env "$IMAGE" check --probe
```

The first command validates and prints redacted effective configuration and
provenance without contacting LiteLLM. The probe fetches the catalog and reports
its model count. Exit codes: `0` success, `1` invalid configuration/credentials
source, `2` upstream discovery failure. Run these commands on the same Docker
network as the service if LiteLLM uses an internal container hostname.

### 3. Start the service

```sh
docker run -d --name ollame --restart unless-stopped \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --stop-timeout 70 \
  --env-file .secrets/ollame.env \
  -p 127.0.0.1:11434:11434 \
  -p 127.0.0.1:9434:9434 \
  "$IMAGE" serve --config /dev/null
```

The image runs as UID/GID `65532:65532` and contains a static binary and CA bundle.
The container listens on all interfaces internally, while the published ports
above are restricted to the host's loopback interface. To reach it remotely, put
it behind your existing TLS reverse proxy or deliberately change the API port's
published address. Keep the admin port private: it does not use client auth.

If LiteLLM is another container, attach both containers to the same Docker network
and use its service name, such as `http://litellm:4000/v1`. `localhost` inside
ollame is not the host or another container. For a host service on Linux Docker,
you can use `--add-host=host.docker.internal:host-gateway` with a URL that uses
`host.docker.internal`; the host service must listen on a reachable interface.

### 4. Verify and connect clients

```sh
curl --fail-with-body http://127.0.0.1:9434/readyz
curl --fail-with-body http://127.0.0.1:11434/version
docker logs --tail 30 ollame
```

Set `OLLAME_CLIENT_TOKEN` in your terminal to the generated plaintext client token,
then list exposed models:

```sh
curl --fail-with-body http://127.0.0.1:11434/api/tags \
  -H "Authorization: Bearer $OLLAME_CLIENT_TOKEN"
```

Use one of those names in a request:

```sh
curl --fail-with-body --no-buffer http://127.0.0.1:11434/api/chat \
  -H "Authorization: Bearer $OLLAME_CLIENT_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"model":"YOUR_LISTED_MODEL","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

Ollama clients use `http://127.0.0.1:11434` as the base URL. OpenAI clients use
`http://127.0.0.1:11434/v1`. Both send the ollame client token, not the LiteLLM key.
Clients unable to send auth headers can use `auth.mode=disabled` only on a trusted
local/private listener; required auth is the default. Browser clients may also
need their exact origin added to `server.cors_origins`.

## Running the binary with flags and environment

The same settings work without Docker. For example, read the upstream key from a
runtime file and keep the token source in another runtime file:

```sh
export OLLAME_UPSTREAM_BASE_URL=https://litellm.example.com/v1
export OLLAME_UPSTREAM_API_KEY_FILE=/absolute/path/to/litellm-key
export OLLAME_AUTH_TOKENS_FILE=/absolute/path/to/ollame-tokens

./result-package/bin/ollame check --config /dev/null --probe
./result-package/bin/ollame serve --config /dev/null \
  --listen 127.0.0.1:11434 --server-admin-listen 127.0.0.1:9434
```

The key file contains only the LiteLLM key. The token file contains one
`name=token` entry per line, for example `local=olm_...`. Client names must match
`[a-z0-9][a-z0-9._-]{0,62}`. The `token hash` command can produce a digest for
storing a client credential as `name=sha256:<hex>` instead of plaintext.

Flags override the matching environment settings:

```sh
./result-package/bin/ollame models --config /dev/null \
  -u https://another-litellm.example.com/v1 --json
```

If an upstream key file is configured, that file supplies the key even when
`upstream.api_key` is also set. Clear `--upstream-api-key-file=` to switch back to
the inline/environment key. An empty or unreadable key file fails startup; on
reload the previous valid key is retained and an error is reported.

## Configuration rules

For ordinary settings, precedence is **defaults < TOML < environment < flags**.
Flags and environment are captured at process startup. Reloading does not reread
the invoking shell's environment.

Select TOML with `--config`/`-c`, then `OLLAME_CONFIG`. If neither is set, search:

1. `./ollame.toml`
2. `$XDG_CONFIG_HOME/ollame/ollame.toml`, or `$HOME/.config/ollame/ollame.toml`
3. `/etc/ollame/ollame.toml`

An explicitly selected missing or malformed file is an error. Relative file paths
are relative to the process working directory, not to the TOML file's directory.
Unknown TOML keys fail validation; unknown `OLLAME_*` variables produce warnings.

Value formats:

- Booleans: `true` or `false`; a bare boolean flag enables it. Disable with, for
  example, `--metrics-token-labels=false`.
- Durations: Go duration strings such as `500ms`, `30s`, `5m`, `1h`.
- Sizes: integer plus `B`, `KB`, `KiB`, `MB`, `MiB`, `GB`, or `GiB`, e.g. `64MiB`.
- Lists: comma-separated values. Repeat list flags to combine their entries within
  that flag source. An explicitly empty value clears a list. A higher source
  replaces the lower source's list.
- Maps: a JSON object of strings, for example
  `--upstream-extra-headers '{"x-team":"development"}'`. These values are secrets
  for config-output redaction. Prefer a TOML table for larger maps.
- Scalar flags: the last occurrence wins. Use `--flag=value` for values that begin
  with a dash or when passing an empty value.

Token sources have their own name-based precedence:
**TOML inline < tokens_file < tokens_dir < OLLAME_AUTH_TOKENS < --token**.
Higher sources replace lower entries with the same name. Duplicate names within
one source and identical credential values assigned to different names are errors.
An empty higher source does not erase unrelated names from lower sources.

### Special variables and flags

| Variable                  | Flag                     | Meaning                                                                                                                    |
| ------------------------- | ------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| `OLLAME_CONFIG`           | `-c`, `--config PATH`    | Select TOML; `/dev/null` gives an explicit empty config.                                                                   |
| `OLLAME_AUTH_TOKENS`      | `--token NAME=VALUE`     | Comma-separated environment entries; repeat the flag for multiple named tokens. Values may be plaintext or `sha256:<hex>`. |
| `OLLAME_MODELS_OVERRIDES` | None                     | JSON array replacing the TOML model override list; `[]` clears it.                                                         |
| `OLLAME_MODELS_ALIASES`   | `--alias NAME=TARGET`    | Simple aliases; comma-separated in the environment, repeatable as flags.                                                   |
| —                         | `-u`, `--upstream URL`   | Shorthand for `--upstream-base-url`; do not supply both URL forms.                                                         |
| —                         | `-l`, `--listen ADDRESS` | Shorthand for `--server-listen`; do not supply both address forms.                                                         |
| —                         | `--probe`                | `check` only: contact upstream after local validation.                                                                     |
| —                         | `--json`                 | `models` only: output catalog JSON instead of a table.                                                                     |
| —                         | `-h`, `--help`           | Show command help without loading config.                                                                                  |

`serve`, `check`, and `models` accept configuration flags. `models` validates the
upstream/model sections and does not require client tokens. `version`,
`token new NAME`, `token hash`, and `completion bash|zsh|fish` do not load config.
`token hash` reads at most 4KiB from stdin and trims one trailing newline.

For config-file aliases with system prompts, options, hiding or model behavior,
use `[[models.alias]]`. Use `[[models.override]]` or the JSON environment variable
`OLLAME_MODELS_OVERRIDES` for matching model overrides. Full alias records remain
TOML-only.

```toml
[models]
include = ["*"]
exclude = ["internal/*"]

[[models.alias]]
name = "coder"
target = "your-litellm-model-id"
hide_target = true

[[models.override]]
match = "your-litellm-model-id"
think_style = "chat_template_kwargs"
```

Filters enforce access on every inference route. `*` matches slashes. Aliases
cannot restore an excluded target. `hide_target` exposes the target through the
alias only. Use the upstream ID in the override matcher. Only add provider-specific
thinking overrides when the model needs them; the example style suits the tested
llama-server/Qwen integration, not every provider.

## Full configuration environment/flag reference

The tables below reflect `internal/config.Keys()` and `config.Defaults()`. Every
listed flag also works on `check` and `models`, although `models` only validates
its applicable sections. Defaults are built-in defaults; the example deployment
above intentionally overrides addresses and secret sources. Empty defaults may
need values before serving. `secret` marks fields whose effective values are
redacted by config inspection.

### admin

| TOML key      | Environment variable | Flag            | Default | Meaning / accepted values                    |
| ------------- | -------------------- | --------------- | ------- | -------------------------------------------- |
| `admin.debug` | `OLLAME_ADMIN_DEBUG` | `--admin-debug` | `false` | Enable debug endpoints on the admin listener |

### auth

| TOML key                | Environment variable           | Flag                      | Default                | Meaning / accepted values                                                           |
| ----------------------- | ------------------------------ | ------------------------- | ---------------------- | ----------------------------------------------------------------------------------- |
| `auth.accept_basic`     | `OLLAME_AUTH_ACCEPT_BASIC`     | `--auth-accept-basic`     | `true`                 | Accept tokens through HTTP Basic auth                                               |
| `auth.accept_x_api_key` | `OLLAME_AUTH_ACCEPT_X_API_KEY` | `--auth-accept-x-api-key` | `true`                 | Accept x-api-key credentials                                                        |
| `auth.mode`             | `OLLAME_AUTH_MODE`             | `--auth-mode`             | `"required"`           | Inbound auth: required, optional, or disabled; values: required, optional, disabled |
| `auth.public_paths`     | `OLLAME_AUTH_PUBLIC_PATHS`     | `--auth-public-paths`     | `["/","/api/version"]` | Exact unauthenticated paths                                                         |
| `auth.reload_interval`  | `OLLAME_AUTH_RELOAD_INTERVAL`  | `--auth-reload-interval`  | `"30s"`                | Config and secret polling interval                                                  |
| `auth.stale_grace`      | `OLLAME_AUTH_STALE_GRACE`      | `--auth-stale-grace`      | `"5m0s"`               | Retention after a token source starts failing                                       |
| `auth.tokens_dir`       | `OLLAME_AUTH_TOKENS_DIR`       | `--auth-tokens-dir`       | `(empty)`              | Runtime directory containing named tokens                                           |
| `auth.tokens_file`      | `OLLAME_AUTH_TOKENS_FILE`      | `--auth-tokens-file`      | `(empty)`              | Runtime name=token file                                                             |

### compat

| TOML key                        | Environment variable                   | Flag                              | Default              | Meaning / accepted values                                                                                               |
| ------------------------------- | -------------------------------------- | --------------------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `compat.sort_model_messages`    | `OLLAME_COMPAT_SORT_MODEL_MESSAGES`    | `--compat-sort-model-messages`    | `false`              | Combine system messages at the beginning of translated conversations                                                    |
| `compat.bad_tool_arguments`     | `OLLAME_COMPAT_BAD_TOOL_ARGUMENTS`     | `--compat-bad-tool-arguments`     | `"wrap"`             | Invalid tool argument policy: wrap or error; values: wrap, error                                                        |
| `compat.default_image_mime`     | `OLLAME_COMPAT_DEFAULT_IMAGE_MIME`     | `--compat-default-image-mime`     | `"image/png"`        | Image MIME fallback when magic bytes are unknown                                                                        |
| `compat.estimate_tokens`        | `OLLAME_COMPAT_ESTIMATE_TOKENS`        | `--compat-estimate-tokens`        | `false`              | Estimate token counts when upstream usage is absent                                                                     |
| `compat.forward_sampler_extras` | `OLLAME_COMPAT_FORWARD_SAMPLER_EXTRAS` | `--compat-forward-sampler-extras` | `false`              | Forward additional sampling parameters                                                                                  |
| `compat.history_thinking`       | `OLLAME_COMPAT_HISTORY_THINKING`       | `--compat-history-thinking`       | `"drop"`             | Historical reasoning policy: drop or reasoning_content; values: drop, reasoning_content                                 |
| `compat.json_schema_strict`     | `OLLAME_COMPAT_JSON_SCHEMA_STRICT`     | `--compat-json-schema-strict`     | `false`              | Request strict upstream JSON schemas                                                                                    |
| `compat.missing_tool_result`    | `OLLAME_COMPAT_MISSING_TOOL_RESULT`    | `--compat-missing-tool-result`    | `"synthesize"`       | Missing tool result policy: synthesize or passthrough; values: synthesize, passthrough                                  |
| `compat.ollama_version`         | `OLLAME_COMPAT_OLLAMA_VERSION`         | `--compat-ollama-version`         | `"0.34.0"`           | Pinned Ollama compatibility version                                                                                     |
| `compat.orphan_tool_result`     | `OLLAME_COMPAT_ORPHAN_TOOL_RESULT`     | `--compat-orphan-tool-result`     | `"user_message"`     | Orphan tool result policy: user_message or error; values: user_message, error                                           |
| `compat.strict_capabilities`    | `OLLAME_COMPAT_STRICT_CAPABILITIES`    | `--compat-strict-capabilities`    | `false`              | Reject known-unsupported requested capabilities                                                                         |
| `compat.strict_options`         | `OLLAME_COMPAT_STRICT_OPTIONS`         | `--compat-strict-options`         | `false`              | Reject unknown Ollama options                                                                                           |
| `compat.think_initial`          | `OLLAME_COMPAT_THINK_INITIAL`          | `--compat-think-initial`          | `false`              | Start inside a template-supplied thinking block                                                                         |
| `compat.think_max`              | `OLLAME_COMPAT_THINK_MAX`              | `--compat-think-max`              | `"high"`             | Reasoning effort used for think=max                                                                                     |
| `compat.think_off`              | `OLLAME_COMPAT_THINK_OFF`              | `--compat-think-off`              | `"omit"`             | Reasoning disable policy: omit, none, or disable; values: omit, none, disable                                           |
| `compat.think_on`               | `OLLAME_COMPAT_THINK_ON`               | `--compat-think-on`               | `"medium"`           | Reasoning effort used for think=true                                                                                    |
| `compat.think_style`            | `OLLAME_COMPAT_THINK_STYLE`            | `--compat-think-style`            | `"reasoning_effort"` | Thinking mapping: reasoning_effort, chat_template_kwargs, or none; values: reasoning_effort, chat_template_kwargs, none |
| `compat.think_tags`             | `OLLAME_COMPAT_THINK_TAGS`             | `--compat-think-tags`             | `false`              | Extract the first thinking tag block                                                                                    |

### embed

| TOML key                  | Environment variable             | Flag                        | Default | Meaning / accepted values                                  |
| ------------------------- | -------------------------------- | --------------------------- | ------- | ---------------------------------------------------------- |
| `embed.batch_concurrency` | `OLLAME_EMBED_BATCH_CONCURRENCY` | `--embed-batch-concurrency` | `1`     | Embedding concurrency; only 1 is supported                 |
| `embed.batch_size`        | `OLLAME_EMBED_BATCH_SIZE`        | `--embed-batch-size`        | `0`     | Maximum inputs per embedding call; zero disables splitting |
| `embed.legacy_normalize`  | `OLLAME_EMBED_LEGACY_NORMALIZE`  | `--embed-legacy-normalize`  | `false` | Normalize legacy embedding vectors                         |
| `embed.normalize`         | `OLLAME_EMBED_NORMALIZE`         | `--embed-normalize`         | `true`  | Normalize modern embedding vectors                         |

### generate

| TOML key                | Environment variable           | Flag                      | Default         | Meaning / accepted values                                                        |
| ----------------------- | ------------------------------ | ------------------------- | --------------- | -------------------------------------------------------------------------------- |
| `generate.fim`          | `OLLAME_GENERATE_FIM`          | `--generate-fim`          | `"off"`         | Fill-in-middle mode: off, suffix, or template; values: off, suffix, template     |
| `generate.fim_template` | `OLLAME_GENERATE_FIM_TEMPLATE` | `--generate-fim-template` | `(empty)`       | Fill-in-middle template with prefix and suffix placeholders                      |
| `generate.raw`          | `OLLAME_GENERATE_RAW`          | `--generate-raw`          | `"completions"` | Raw generate mode: completions, chat, or error; values: completions, chat, error |

### limits

| TOML key                         | Environment variable                    | Flag                               | Default       | Meaning / accepted values                   |
| -------------------------------- | --------------------------------------- | ---------------------------------- | ------------- | ------------------------------------------- |
| `limits.max_aggregate_bytes`     | `OLLAME_LIMITS_MAX_AGGREGATE_BYTES`     | `--limits-max-aggregate-bytes`     | `"33554432B"` | Maximum retained non-streaming output size  |
| `limits.max_catalog_models`      | `OLLAME_LIMITS_MAX_CATALOG_MODELS`      | `--limits-max-catalog-models`      | `10000`       | Maximum discovery and exposed catalog count |
| `limits.max_embed_inputs`        | `OLLAME_LIMITS_MAX_EMBED_INPUTS`        | `--limits-max-embed-inputs`        | `2048`        | Maximum embedding inputs per request        |
| `limits.max_images`              | `OLLAME_LIMITS_MAX_IMAGES`              | `--limits-max-images`              | `32`          | Maximum images per request                  |
| `limits.max_tool_call_bytes`     | `OLLAME_LIMITS_MAX_TOOL_CALL_BYTES`     | `--limits-max-tool-call-bytes`     | `"1048576B"`  | Maximum argument bytes per tool call        |
| `limits.max_tool_calls`          | `OLLAME_LIMITS_MAX_TOOL_CALLS`          | `--limits-max-tool-calls`          | `128`         | Maximum tool calls per response             |
| `limits.max_upstream_json_bytes` | `OLLAME_LIMITS_MAX_UPSTREAM_JSON_BYTES` | `--limits-max-upstream-json-bytes` | `"67108864B"` | Maximum non-SSE upstream response size      |

### log

| TOML key     | Environment variable | Flag           | Default  | Meaning / accepted values                                                |
| ------------ | -------------------- | -------------- | -------- | ------------------------------------------------------------------------ |
| `log.access` | `OLLAME_LOG_ACCESS`  | `--log-access` | `true`   | Log completed requests                                                   |
| `log.bodies` | `OLLAME_LOG_BODIES`  | `--log-bodies` | `false`  | Log bodies at debug level; avoid in production                           |
| `log.format` | `OLLAME_LOG_FORMAT`  | `--log-format` | `"json"` | Log format: json or text; values: json, text                             |
| `log.level`  | `OLLAME_LOG_LEVEL`   | `--log-level`  | `"info"` | Log level: debug, info, warn, or error; values: debug, info, warn, error |

### management

| TOML key            | Environment variable       | Flag                  | Default     | Meaning / accepted values                                                        |
| ------------------- | -------------------------- | --------------------- | ----------- | -------------------------------------------------------------------------------- |
| `management.copy`   | `OLLAME_MANAGEMENT_COPY`   | `--management-copy`   | `"error"`   | Copy behavior; only error is supported; values: error                            |
| `management.create` | `OLLAME_MANAGEMENT_CREATE` | `--management-create` | `"error"`   | Create behavior; only error is supported; values: error                          |
| `management.delete` | `OLLAME_MANAGEMENT_DELETE` | `--management-delete` | `"error"`   | Delete behavior; only error is supported; values: error                          |
| `management.ps`     | `OLLAME_MANAGEMENT_PS`     | `--management-ps`     | `"recent"`  | Running model listing: recent, empty, or catalog; values: recent, empty, catalog |
| `management.pull`   | `OLLAME_MANAGEMENT_PULL`   | `--management-pull`   | `"emulate"` | Pull behavior: emulate or error; values: emulate, error                          |
| `management.push`   | `OLLAME_MANAGEMENT_PUSH`   | `--management-push`   | `"error"`   | Push behavior; only error is supported; values: error                            |

### metrics

| TOML key               | Environment variable          | Flag                     | Default | Meaning / accepted values                 |
| ---------------------- | ----------------------------- | ------------------------ | ------- | ----------------------------------------- |
| `metrics.enabled`      | `OLLAME_METRICS_ENABLED`      | `--metrics-enabled`      | `true`  | Enable Prometheus metrics                 |
| `metrics.token_labels` | `OLLAME_METRICS_TOKEN_LABELS` | `--metrics-token-labels` | `true`  | Include configured token names in metrics |

### models

| TOML key                        | Environment variable                   | Flag                              | Default                             | Meaning / accepted values                            |
| ------------------------------- | -------------------------------------- | --------------------------------- | ----------------------------------- | ---------------------------------------------------- |
| `models.advertise_remote`       | `OLLAME_MODELS_ADVERTISE_REMOTE`       | `--models-advertise-remote`       | `false`                             | Advertise upstream host and model metadata           |
| `models.architecture`           | `OLLAME_MODELS_ARCHITECTURE`           | `--models-architecture`           | `(empty)`                           | Override general.architecture for synthetic metadata |
| `models.default_capabilities`   | `OLLAME_MODELS_DEFAULT_CAPABILITIES`   | `--models-default-capabilities`   | `["completion","tools"]`            | Advertised defaults for unknown capabilities         |
| `models.default_context_length` | `OLLAME_MODELS_DEFAULT_CONTEXT_LENGTH` | `--models-default-context-length` | `131072`                            | Context length when no metadata is available         |
| `models.default_tag`            | `OLLAME_MODELS_DEFAULT_TAG`            | `--models-default-tag`            | `"latest"`                          | Tag appended to untagged exposed names               |
| `models.exclude`                | `OLLAME_MODELS_EXCLUDE`                | `--models-exclude`                | `[]`                                | Full upstream ID exclusion globs                     |
| `models.fetch_timeout`          | `OLLAME_MODELS_FETCH_TIMEOUT`          | `--models-fetch-timeout`          | `"15s"`                             | Deadline per catalog discovery or metadata call      |
| `models.format`                 | `OLLAME_MODELS_FORMAT`                 | `--models-format`                 | `"gguf"`                            | Synthetic model format                               |
| `models.include`                | `OLLAME_MODELS_INCLUDE`                | `--models-include`                | `["*"]`                             | Full upstream ID inclusion globs                     |
| `models.modes`                  | `OLLAME_MODELS_MODES`                  | `--models-modes`                  | `["chat","completion","embedding"]` | Allowed known upstream modes                         |
| `models.refresh_interval`       | `OLLAME_MODELS_REFRESH_INTERVAL`       | `--models-refresh-interval`       | `"1m0s"`                            | Periodic model discovery interval                    |
| `models.refresh_on_miss`        | `OLLAME_MODELS_REFRESH_ON_MISS`        | `--models-refresh-on-miss`        | `true`                              | Refresh the catalog once for an unknown model        |
| `models.require_on_start`       | `OLLAME_MODELS_REQUIRE_ON_START`       | `--models-require-on-start`       | `true`                              | Gate readiness on initial catalog discovery          |

### passthrough

| TOML key                       | Environment variable                  | Flag                             | Default                                                                                                                                                | Meaning / accepted values                     |
| ------------------------------ | ------------------------------------- | -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------- |
| `passthrough.enabled`          | `OLLAME_PASSTHROUGH_ENABLED`          | `--passthrough-enabled`          | `true`                                                                                                                                                 | Enable registered OpenAI and Anthropic routes |
| `passthrough.paths`            | `OLLAME_PASSTHROUGH_PATHS`            | `--passthrough-paths`            | `["/v1/chat/completions","/v1/completions","/v1/embeddings","/v1/models","/v1/models/{model}","/v1/responses","/v1/responses/compact","/v1/messages"]` | Enabled registered passthrough paths          |
| `passthrough.response_headers` | `OLLAME_PASSTHROUGH_RESPONSE_HEADERS` | `--passthrough-response-headers` | `["x-litellm-*","retry-after","x-request-id","openai-*","anthropic-*"]`                                                                                | Upstream response header allowlist globs      |

### server

| TOML key                     | Environment variable                | Flag                           | Default               | Meaning / accepted values                              |
| ---------------------------- | ----------------------------------- | ------------------------------ | --------------------- | ------------------------------------------------------ |
| `server.admin_listen`        | `OLLAME_SERVER_ADMIN_LISTEN`        | `--server-admin-listen`        | `"127.0.0.1:9434"`             | Admin listen address; empty disables it                |
| `server.cors_origins`        | `OLLAME_SERVER_CORS_ORIGINS`        | `--server-cors-origins`        | `[]`                  | Allowed browser origins                                |
| `server.drain_delay`         | `OLLAME_SERVER_DRAIN_DELAY`         | `--server-drain-delay`         | `"5s"`                | Accept traffic after readiness drops for this duration |
| `server.idle_timeout`        | `OLLAME_SERVER_IDLE_TIMEOUT`        | `--server-idle-timeout`        | `"2m0s"`              | HTTP keep-alive idle timeout                           |
| `server.listen`              | `OLLAME_SERVER_LISTEN`              | `--server-listen`              | `":11434"`            | API address or unix:/path.sock                         |
| `server.max_body_bytes`      | `OLLAME_SERVER_MAX_BODY_BYTES`      | `--server-max-body-bytes`      | `"67108864B"`         | Maximum inbound body size                              |
| `server.max_header_bytes`    | `OLLAME_SERVER_MAX_HEADER_BYTES`    | `--server-max-header-bytes`    | `"65536B"`            | Maximum inbound header size                            |
| `server.max_inflight`        | `OLLAME_SERVER_MAX_INFLIGHT`        | `--server-max-inflight`        | `0`                   | Concurrent inference limit; zero is unlimited          |
| `server.read_header_timeout` | `OLLAME_SERVER_READ_HEADER_TIMEOUT` | `--server-read-header-timeout` | `"10s"`               | Maximum inbound header read time                       |
| `server.root_banner`         | `OLLAME_SERVER_ROOT_BANNER`         | `--server-root-banner`         | `"Ollama is running"` | Root health banner                                     |
| `server.shutdown_grace`      | `OLLAME_SERVER_SHUTDOWN_GRACE`      | `--server-shutdown-grace`      | `"1m0s"`              | Time to drain after closing the listener               |
| `server.tls_cert`            | `OLLAME_SERVER_TLS_CERT`            | `--server-tls-cert`            | `(empty)`             | API TLS certificate path                               |
| `server.tls_key`             | `OLLAME_SERVER_TLS_KEY`             | `--server-tls-key`             | `(empty)`             | API TLS private key path                               |
| `server.write_stall_timeout` | `OLLAME_SERVER_WRITE_STALL_TIMEOUT` | `--server-write-stall-timeout` | `"30s"`               | Maximum blocked downstream write time                  |

### upstream

| TOML key                          | Environment variable                     | Flag                                | Default        | Meaning / accepted values                                                                         |
| --------------------------------- | ---------------------------------------- | ----------------------------------- | -------------- | ------------------------------------------------------------------------------------------------- |
| `upstream.api_key`                | `OLLAME_UPSTREAM_API_KEY`                | `--upstream-api-key`                | `(empty)`      | Upstream API key; prefer api-key-file; secret                                                     |
| `upstream.api_key_file`           | `OLLAME_UPSTREAM_API_KEY_FILE`           | `--upstream-api-key-file`           | `(empty)`      | Runtime upstream API key file                                                                     |
| `upstream.base_url`               | `OLLAME_UPSTREAM_BASE_URL`               | `--upstream-base-url`               | `(empty)`      | OpenAI-compatible base URL including /v1                                                          |
| `upstream.body_timeout`           | `OLLAME_UPSTREAM_BODY_TIMEOUT`           | `--upstream-body-timeout`           | `"2m0s"`       | Non-SSE body read deadline                                                                        |
| `upstream.ca_file`                | `OLLAME_UPSTREAM_CA_FILE`                | `--upstream-ca-file`                | `(empty)`      | Additional upstream CA bundle path                                                                |
| `upstream.connect_timeout`        | `OLLAME_UPSTREAM_CONNECT_TIMEOUT`        | `--upstream-connect-timeout`        | `"5s"`         | Upstream connection deadline                                                                      |
| `upstream.extra_headers`          | `OLLAME_UPSTREAM_EXTRA_HEADERS`          | `--upstream-extra-headers`          | `{}`           | Static upstream headers as a JSON object; secret                                                  |
| `upstream.flavor`                 | `OLLAME_UPSTREAM_FLAVOR`                 | `--upstream-flavor`                 | `"litellm"`    | Upstream flavor: litellm or openai; values: litellm, openai                                       |
| `upstream.forward_client_as_user` | `OLLAME_UPSTREAM_FORWARD_CLIENT_AS_USER` | `--upstream-forward-client-as-user` | `true`         | Attribute upstream requests to token names                                                        |
| `upstream.header_timeout`         | `OLLAME_UPSTREAM_HEADER_TIMEOUT`         | `--upstream-header-timeout`         | `"5m0s"`       | Upstream response header deadline                                                                 |
| `upstream.idle_timeout`           | `OLLAME_UPSTREAM_IDLE_TIMEOUT`           | `--upstream-idle-timeout`           | `"2m0s"`       | Maximum gap between SSE events                                                                    |
| `upstream.insecure_skip_verify`   | `OLLAME_UPSTREAM_INSECURE_SKIP_VERIFY`   | `--upstream-insecure-skip-verify`   | `false`        | Disable upstream TLS verification                                                                 |
| `upstream.max_idle_conns`         | `OLLAME_UPSTREAM_MAX_IDLE_CONNS`         | `--upstream-max-idle-conns`         | `256`          | Upstream idle connection pool size                                                                |
| `upstream.max_tokens_field`       | `OLLAME_UPSTREAM_MAX_TOKENS_FIELD`       | `--upstream-max-tokens-field`       | `"max_tokens"` | Token limit field: max_tokens or max_completion_tokens; values: max_tokens, max_completion_tokens |
| `upstream.model_info`             | `OLLAME_UPSTREAM_MODEL_INFO`             | `--upstream-model-info`             | `"auto"`       | Metadata enrichment: auto, litellm, or off; values: auto, litellm, off                            |
| `upstream.request_timeout`        | `OLLAME_UPSTREAM_REQUEST_TIMEOUT`        | `--upstream-request-timeout`        | `"1h0m0s"`     | Positive total request deadline including retries                                                 |
| `upstream.retries`                | `OLLAME_UPSTREAM_RETRIES`                | `--upstream-retries`                | `1`            | Additional attempts for eligible upstream failures                                                |
| `upstream.stream_mode`            | `OLLAME_UPSTREAM_STREAM_MODE`            | `--upstream-stream-mode`            | `"always"`     | Upstream streaming: always or match; values: always, match                                        |
| `upstream.tags`                   | `OLLAME_UPSTREAM_TAGS`                   | `--upstream-tags`                   | `["ollame"]`   | LiteLLM attribution tags                                                                          |
| `upstream.user_prefix`            | `OLLAME_UPSTREAM_USER_PREFIX`            | `--upstream-user-prefix`            | `"ollame:"`    | Prefix for upstream user attribution                                                              |

## Models that require system messages first

Enable normalization globally with `--compat-sort-model-messages` or:

```sh
export OLLAME_COMPAT_SORT_MODEL_MESSAGES=true
```

For selected upstream model IDs, set a JSON array in the environment:

```sh
export OLLAME_MODELS_OVERRIDES='[{"match":"qwen*","sort_model_messages":true}]'
```

Matchers are case-sensitive globs against upstream IDs, not exposed aliases.
Use `"match":"qwen3.8"` for that exact upstream ID. Entries apply in array order,
with later matching entries overriding earlier fields. The environment array
replaces the entire TOML override list; `[]` clears it. It accepts the same fields
as `[[models.override]]`, including explicit false values. Invalid JSON, unknown
fields, or wrong types reject configuration. Environment changes require restart.

For Docker `--env-file`, omit the surrounding shell quotes:

```dotenv
OLLAME_MODELS_OVERRIDES=[{"match":"qwen*","sort_model_messages":true}]
```

Alternatively, use TOML:

```toml
[[models.override]]
match = "qwen*"
sort_model_messages = true
```

`sort_model_messages` also works on model aliases. Model/alias overrides take
precedence over the global `compat.sort_model_messages` default (`false`),
including explicit `false` overrides.

When enabled, translated Ollama conversations contain one leading system message,
combining all client system messages in their original order with blank lines
between them. Other messages keep their relative order, and tool-call/result
matching happens after system messages are collected. This changes the scope of
mid-conversation system instructions. OpenAI/Anthropic passthrough bodies are not
normalized by this option.

## Secret files and reloads

For file-based credentials, replace `OLLAME_UPSTREAM_API_KEY` and
`OLLAME_AUTH_TOKENS` in the environment file with:

```dotenv
OLLAME_UPSTREAM_API_KEY_FILE=/run/ollame-secrets/litellm-key
OLLAME_AUTH_TOKENS_FILE=/run/ollame-secrets/tokens
```

Mount the containing directory when starting or checking the container:

```sh
--mount type=bind,src=/absolute/path/to/runtime-secrets,dst=/run/ollame-secrets,readonly
```

`litellm-key` contains the LiteLLM key alone. `tokens` contains one
`name=value` entry per line, for example `local=olm_...`. Ensure UID/GID 65532 can
traverse the directory and read both files. Mounting the directory lets atomic
file replacements become visible inside the container.

Configuration and token sources are polled every `auth.reload_interval` (default
30s); `SIGHUP` also requests a reload:

```sh
docker kill --signal HUP ollame
```

Environment variables are captured at startup. Editing the Docker env file
requires recreating the container; sending HUP does not reread that file. CLI
values continue to override reloaded TOML values. Listener addresses and server
TLS certificate/key settings require a process restart.

Successful token-source reads apply removals as revocations. An unreadable or
invalid token source retains its last-good tokens for `auth.stale_grace` (5m),
then drops them. An unreadable or empty upstream key file keeps the last-good
key and logs an error. A new upstream URL/key must successfully fetch a catalog
before replacing the live upstream snapshot. In-flight requests retain their
starting snapshot and may finish after a token is revoked.

Catalog refreshes run every `models.refresh_interval` (1m). Failed refreshes keep
the last-good catalog. After initial readiness, upstream refresh failures do not
make `/readyz` fail; monitor catalog freshness and request errors as well.

## Health, logs, and build identity

| Endpoint                          | Listener            | Purpose                                             |
| --------------------------------- | ------------------- | --------------------------------------------------- |
| `/healthz`                        | Admin, default 9434 | Process liveness                                    |
| `/readyz`                         | Admin               | Initial readiness and shutdown state                |
| `/metrics`                        | Admin               | Prometheus metrics                                  |
| `/version`                        | API and admin       | Binary version, commit, and emulated Ollama version |
| `/api/version`                    | API                 | Ollama compatibility version only                   |
| `/debug/config`, `/debug/catalog` | Admin               | Diagnostics, disabled unless `admin.debug=true`     |

The admin listener defaults to `127.0.0.1:9434` and has no client authentication.
Container deployments that need published admin ports must explicitly set
`OLLAME_SERVER_ADMIN_LISTEN=:9434`, as in the Docker example. Keep it private. API `/version`
is public; API responses also carry `X-Ollame-Version` and `X-Ollame-Commit`.

Logs go to stderr through zerolog. Production defaults are JSON and `info`;
use `OLLAME_LOG_FORMAT=text` for console output in development. Access logging
is controlled by `OLLAME_LOG_ACCESS`. Build fields identify the running binary;
request logs include status, duration, model attribution, and trace correlation.
Lifecycle events report listener addresses, initial catalog discovery and readiness,
and shutdown drain/grace periods. Successful periodic catalog refreshes are logged
at debug level; discovery failures include elapsed time and the refresh interval.
Keep credentials out of request URLs and operator-supplied telemetry attributes.
See [monitoring](docs/monitoring.md) for metrics and alert rules.

SIGINT and SIGTERM begin graceful shutdown. With defaults, allow at least 70s:
5s drain delay + 60s shutdown grace + 5s margin. A second termination signal
exits immediately. For Kubernetes, set `terminationGracePeriodSeconds` to at
least this total and restrict access to the admin port.

## OpenTelemetry environment variables

These are startup-only SDK/exporter settings, separate from the `OLLAME_*`
configuration table. They have no matching ollame flags or TOML keys.

| Variable                                         | Purpose                                                              |
| ------------------------------------------------ | -------------------------------------------------------------------- |
| `OTEL_SDK_DISABLED`                              | `true` disables HTTP tracing                                         |
| `OTEL_TRACES_EXPORTER`                           | `otlp` enables export; `none` keeps local correlation without export |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                    | Collector base endpoint; HTTP appends `/v1/traces`                   |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`             | Full traces endpoint, overrides the general endpoint                 |
| `OTEL_EXPORTER_OTLP_PROTOCOL`                    | `http/protobuf` (default), `http/json`, or `grpc`                    |
| `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`             | Traces-specific protocol override                                    |
| `OTEL_SERVICE_NAME`                              | Telemetry service name                                               |
| `OTEL_RESOURCE_ATTRIBUTES`                       | Comma-separated resource attributes                                  |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | SDK sampling policy and argument                                     |
| `OTEL_BSP_*`                                     | SDK batch queue, scheduling, and export limits                       |

Setting either endpoint also enables OTLP export unless the exporter is explicitly
`none`. The official exporters support additional standard OTLP variables for
headers, TLS, compression, and timeouts; see [tracing configuration](docs/tracing.md)
for exporter references and protocol details. Restart the process after changes.

For a collector on the same Docker network, add to the env file:

```dotenv
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
OTEL_SERVICE_NAME=ollame
```

## Building and publishing an image

From the project shell, after authenticating Docker to GHCR:

```sh
publish-ollame --release-version 0.1.0 --action build
publish-ollame --release-version 0.1.0 --action push
```

`build` builds, loads, and checks the image locally. `push` does those steps and
pushes `ghcr.io/jpetrucciani/ollame:0.1.0`. The default action is **push**. No
`latest` tag is added automatically.

| Helper flag         | Environment variable     | Default                                                 |
| ------------------- | ------------------------ | ------------------------------------------------------- |
| `--release-version` | `OLLAME_RELEASE_VERSION` | `dev`                                                   |
| `--revision`        | `OLLAME_BUILD_COMMIT`    | Git HEAD, or `unknown`; dirty checkouts append `-dirty` |
| `--action`          | `POG_ACTION`             | `push`                                                  |

These variables belong to the build helper, not the running daemon. The version
and revision are compiled into the binary and recorded in image labels. See
[packaging](docs/packaging.md) for direct Nix commands and platform limitations.

## Common deployment problems

- **401 from ollame:** send an ollame client token. The LiteLLM key belongs in
  ollame's upstream configuration, not in clients.
- **`check --probe` exits 2:** verify the URL, key scope, TLS trust, and connectivity
  from the container network. A host-side successful request does not prove
  container connectivity.
- **Model not found:** select a name returned by `/api/tags`. Include/exclude
  filters and aliases define the reachable set; a hidden target is reachable
  through its alias only.
- **Browser request blocked:** configure the browser's origin in
  `server.cors_origins`, and ensure the client can send its auth header.
- **Streaming arrives all at once:** disable response buffering in the reverse
  proxy and check the client's streaming support and upstream stream mode.
- **Local model management fails:** models are managed in LiteLLM and its
  providers. Ollame does not install or run local Ollama models.
