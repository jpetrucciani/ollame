# Real embedding response

`response.json` is an unmodified real LiteLLM response from the pinned
llama-server/Nomic backend in `test/integration`. `request.json` supplies two
public, synthetic search-document strings. `provenance.json` records the content
hash and pin reference. No private text or provider credentials were used.

The dedicated GGUF originates from the
[Nomic publisher repository](https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF).
The integration override drops `dimensions` for this custom model name because
LiteLLM rejects that parameter; ollame truncates and normalizes the returned full
vectors. These tests establish protocol behavior, not embedding retrieval quality.
