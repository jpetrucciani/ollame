package upstream

import (
	"net/http"
	"strings"
)

// PassthroughHeaders returns an owned allowlisted copy. The transport appends
// server credentials and correlation headers after applying configured headers.
func PassthroughHeaders(source http.Header) http.Header {
	result := make(http.Header)
	hop := map[string]bool{}
	for _, value := range source.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			hop[strings.ToLower(strings.TrimSpace(key))] = true
		}
	}
	for key, values := range source {
		lower := strings.ToLower(key)
		allowed := strings.HasPrefix(lower, "x-stainless-")
		switch lower {
		case "content-type", "accept", "anthropic-version", "anthropic-beta", "openai-beta":
			allowed = true
		}
		if allowed && !hop[lower] {
			for _, value := range values {
				result.Add(key, value)
			}
		}
	}
	return result
}
