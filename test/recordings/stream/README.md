# Real chat stream

`chat.sse` is the unmodified response from a real LiteLLM proxy routing to the
pinned CPU llama-server/Qwen model in `test/integration`. `request.json` contains
the nonsensitive capture request. `provenance.json` records the capture date,
source, pin reference, and content SHA-256. No provider credential was required
by this isolated local stack.

The parser tests derive protocol framing and injected fault variants in memory
from this recording. Thinking splitter tests exercise a pure text transformation,
not a mocked upstream service. The SSE framing implementation follows the
[HTML event-stream interpretation rules](https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation).

`tools.sse` is a second unmodified capture from the same pinned stack, using
`tools.request.json` to request `get_weather` for Indianapolis. Its separate
`tools.provenance.json` records the content hash. Tool assembly tests use the
recorded fragments directly, interleave copies with distinct indexes to exercise
parallel assembly, and truncate or enlarge fragments to exercise fault handling.
These derived cases test pure byte transformations, not provider fault behavior.

`completion.json` records a non-streaming raw completion with logprobs from the
same stack, requested with `completion.request.json`; its hash is recorded in
`completion.provenance.json`. This upstream returns chat-shaped logprobs even on
the completions endpoint. The converter removes its provider-only token IDs.
The pinned LiteLLM stack omits logprobs from streaming completion responses,
confirmed by a direct request and surfaced explicitly in the integration test.
Streaming completion probability acceptance remains open for other providers.
