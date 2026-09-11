package server

import (
	"net/http"
	"slices"
	"strings"
)

// cors handles browser negotiation only. The actual request still passes route
// policy, authentication, admission, and upstream header filtering.
func cors(w http.ResponseWriter, r *http.Request, state *snapshot, mux *http.ServeMux) bool {
	if len(state.config.Server.CORSOrigins) == 0 {
		return false
	}
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" && origin != ""
	if preflight {
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		probe := r.Clone(r.Context())
		probe.Method = r.Header.Get("Access-Control-Request-Method")
		if _, pattern := mux.Handler(probe); pattern == "" {
			http.NotFound(w, r)
			return true
		}
	}
	if origin == "" || len(r.Header.Values("Origin")) != 1 || !slices.Contains(state.config.Server.CORSOrigins, origin) {
		if preflight {
			localError(w, r, http.StatusForbidden, "origin is not allowed")
		}
		return preflight
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	if !preflight {
		return false
	}
	method := r.Header.Get("Access-Control-Request-Method")
	headers := strings.Join(r.Header.Values("Access-Control-Request-Headers"), ",")
	for _, name := range strings.Split(headers, ",") {
		if headers != "" && !headerToken(strings.TrimSpace(name)) {
			localError(w, r, http.StatusBadRequest, "invalid preflight headers")
			return true
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", method)
	if headers != "" {
		w.Header().Set("Access-Control-Allow-Headers", headers)
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func headerToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
