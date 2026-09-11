package server

import (
	"net/http"
	"testing"
)

func TestPassthroughEventFraming(t *testing.T) {
	for _, raw := range []string{"data: one\n\n", "data: one\r\n\r\n", "data\r\r"} {
		for split := 0; split <= len(raw); split++ {
			var progress eventProgress
			first := progress.Feed([]byte(raw[:split]))
			second := progress.Feed([]byte(raw[split:]))
			if !first && !second {
				t.Fatalf("lost completed event at split %d in %q", split, raw)
			}
		}
	}
	for _, raw := range []string{":keepalive\n\n", "data: incomplete", "event: ping\n\n"} {
		var progress eventProgress
		if progress.Feed([]byte(raw)) {
			t.Fatalf("non-data event reset deadline: %q", raw)
		}
	}
}
func TestPassthroughResponseHeaders(t *testing.T) {
	source := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Connection": {"X-Litellm-Hop"}, "X-Litellm-Hop": {"drop"}, "X-Litellm-Cost": {"0.1"}, "Content-Length": {"123"}, "Set-Cookie": {"private"}}
	target := make(http.Header)
	copyProviderHeaders(target, source, []string{"x-litellm-*"})
	if len(target) != 2 || target.Get("Content-Type") != source.Get("Content-Type") || target.Get("X-Litellm-Cost") != "0.1" {
		t.Fatalf("unexpected copied headers: %v", target)
	}
}
