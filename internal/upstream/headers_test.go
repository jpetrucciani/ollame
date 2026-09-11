package upstream

import (
	"net/http"
	"testing"
)

func TestPassthroughHeaderAllowlist(t *testing.T) {
	source := http.Header{"Content-Type": {"application/custom+json"}, "Anthropic-Version": {"2023-06-01"}, "X-Stainless-Lang": {"python"}, "Cookie": {"private"}, "Authorization": {"Bearer private"}, "X-Litellm-Api-Key": {"private"}, "Accept-Encoding": {"gzip"}, "Connection": {"Openai-Beta"}, "Openai-Beta": {"drop"}}
	result := PassthroughHeaders(source)
	if len(result) != 3 || result.Get("Content-Type") != "application/custom+json" || result.Get("X-Stainless-Lang") != "python" {
		t.Fatalf("incorrect forwarded headers: %v", result)
	}
	source["Content-Type"][0] = "changed"
	if result.Get("Content-Type") == "changed" {
		t.Fatal("headers alias caller storage")
	}
}
