# Real integration stack

`compose.yaml` runs real LiteLLM and a CPU llama-server with the pinned Qwen3
GGUF. Image digests, model revision, size, and SHA-256 are in `pins.json`.
This stack currently validates catalog discovery, binary list/show/chat/generate/ps, and
the inference transport directly against real services. Remaining HTTP routes,
toxiproxy faults, virtual-key scopes, embeddings, and the full client matrix
are still implementation work.

Set `OLLAME_TEST_MODEL` and `OLLAME_TEST_EMBED_MODEL` to existing GGUFs whose
SHA-256 values match the chat and embedding model entries in `pins.json`.
The development run cached the verified file at
`/home/jacobi/.cache/ollame-test/Qwen3-0.6B-Q8_0.gguf`; this path is not required
on other machines. Model data must stay outside the repository and Nix store.

The embedding cache used locally is
`/home/jacobi/.cache/ollame-test/nomic-embed-text-v1.5.Q4_K_M.gguf`.
The dedicated embedding server uses mean pooling and the same pinned llama-server
image. `ollame.toml` drops the unsupported upstream `dimensions` parameter for
this custom LiteLLM model name; local dimension truncation is exercised by the
real-binary test, with batch size one. Both environment variables are required
by Compose commands, including shutdown.

```sh
docker compose -p ollame-integration -f test/integration/compose.yaml up -d
docker compose -p ollame-integration -f test/integration/compose.yaml port litellm 4000
```

The published port is dynamic and loopback-only. Set `OLLAME_TEST_UPSTREAM` to
`http://<reported address>/v1`, then run:

```sh
CGO_ENABLED=0 go test ./test -run 'TestRealCatalogListAndShow|TestProcessBootAuthenticationAndShutdown' -v
CGO_ENABLED=0 go test ./test -run '^TestRealInferenceTransport$' -v
CGO_ENABLED=0 go test ./test -run '^TestRealChatHTTP$' -v
```

Stop only this test project when finished:

```sh
docker compose -p ollame-integration -f test/integration/compose.yaml down
```

`TestRealCatalogListAndShow` explicitly skips locally without its upstream
variable, and fails when CI is set but the variable is absent. CI orchestration
still needs to provision and validate the stack. The separate process smoke uses
an unavailable upstream and verifies only startup/auth/listener/shutdown behavior.
The upstream model-info recording is sanitized and cannot prove virtual-key
permissions in a different LiteLLM deployment.

Wait for `/v1/models` to return successfully before running the tests; Compose
container startup does not establish application readiness. The inference
transport test consumes real SSE content and usage, bounds a nonstreaming body,
and checks cancellation, rejected targets, and routing controls. It does not yet
prove network-fault retries. The separate real-binary chat test covers downstream
streaming and aggregation in both upstream modes, requested model echo, reasoning
suppression, usage, degraded match timing, real tool output, and ps/unload behavior.
It also exercises chat-mode and raw-completions generate in both framing modes,
checking requested model echo and generate-specific response fields. FIM provider
behavior and completion logprobs still require dedicated acceptance coverage.
Embedding cases exercise both routes, sequential batches, eight-dimensional
normalized output, stable repeated inputs, summed usage, and empty-input behavior.
