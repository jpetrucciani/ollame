# ollame

[![uses nix](https://img.shields.io/badge/uses-nix-%237EBAE4)](https://nixos.org/)
[![uses go](https://img.shields.io/badge/uses-go-%2301ADD8)](https://go.dev/)

Use Ollama clients with the models you already serve through LiteLLM.

Some apps expect Ollama's API, while your models live behind LiteLLM. Ollame
bridges that gap: point the app at ollame, and it translates chat, generation,
tools, thinking, and embeddings into upstream requests. You keep your existing
model infrastructure and give clients a separate key without exposing your
LiteLLM key.

```text
Ollama client → ollame → LiteLLM → model providers
```

Ollame runs no models itself. It provides model discovery, aliases, access
filters, and streaming translation. Local model installation and management
remain upstream responsibilities.

## Quick start

Build with Go 1.26.7 or newer, or use `build-ollame` in the project's Nix shell:

```sh
CGO_ENABLED=0 go build -trimpath -o result-bin/ollame ./cmd/ollame
```

Set your LiteLLM URL/key and choose a separate client key:

```sh
export OLLAME_CONFIG=/dev/null
export OLLAME_UPSTREAM_BASE_URL=https://litellm.example.com/v1
export OLLAME_UPSTREAM_API_KEY=your-litellm-key
export OLLAME_SERVER_LISTEN=0.0.0.0:8000
export OLLAME_SERVER_ADMIN_LISTEN=127.0.0.1:9434
export OLLAME_AUTH_MODE=disabled

./result-bin/ollame check --probe
./result-bin/ollame serve
```

Replace the placeholders with real credentials. The LiteLLM key must be allowed
to list and invoke your models. `OLLAME_CONFIG=/dev/null` makes this setup
independent of any local TOML configuration.

Point your Ollama client at `http://localhost:8000` and configure it to send
`Authorization: Bearer your-client-key`. List available models with:

```sh
curl --fail-with-body http://localhost:8000/api/tags \
  -H 'Authorization: Bearer your-client-key'
```

The API listens on all interfaces; use your TLS reverse proxy for remote access.
The admin listener stays on loopback.

## Deployment and reference

- [Operations](OPERATIONAL.md): Docker deployment, all environment variables and
  flags, credential rotation, health checks, and observability.
- [Packaging](docs/packaging.md): static binaries, scratch-based images, and GHCR
  publishing.
- [Integration tests](test/integration/README.md): the real LiteLLM and
  llama-server stack used for local validation.

v1 is in development. Local LiteLLM/llama-server integration has been exercised;
broader provider and client compatibility is still being validated.

Licensed under [MIT](LICENSE).
