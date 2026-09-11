package server

import (
	"net/http"
	"strings"
)

// localErrorBody is used only for registered routes. Unknown paths retain the
// standard text/plain 404 handler instead of entering a provider protocol.
func localErrorBody(path string, status int, message string) any {
	if path == "/v1/messages" {
		kind := "api_error"
		switch status {
		case 400, 405, 422:
			kind = "invalid_request_error"
		case 401:
			kind = "authentication_error"
		case 403:
			kind = "permission_error"
		case 404:
			kind = "not_found_error"
		case 413:
			kind = "request_too_large"
		case 429:
			kind = "rate_limit_error"
		case 503:
			kind = "overloaded_error"
		}
		return map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}}
	}
	if strings.HasPrefix(path, "/v1/") {
		kind := "api_error"
		if status >= 400 && status < 500 {
			kind = "invalid_request_error"
		}
		if status == 401 {
			kind = "authentication_error"
		}
		if status == 429 {
			kind = "rate_limit_error"
		}
		code := strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_")
		return map[string]any{"error": map[string]any{"message": message, "type": kind, "param": nil, "code": code}}
	}
	return map[string]string{"error": message}
}

func localError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, status, localErrorBody(r.URL.Path, status, message))
}
