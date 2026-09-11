# Recorded catalog responses

Captured on 2026-09-10 from the real LiteLLM/llama-server Compose stack defined in
`test/integration`, using its immutable image and Qwen GGUF pins. Requests were
GET /v1/models and GET /v1/model/info. No upstream mock or replay server was used.
Credential-valued fields are replaced with `<redacted>`; other values retain the
upstream response. These recordings prove the tested local metadata shape, not
key-scoped visibility of a deployed LiteLLM virtual key.
