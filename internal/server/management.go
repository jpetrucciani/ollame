package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func (s *Server) managementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pull", s.pull)
	for _, route := range []struct {
		pattern string
		status  int
		message string
	}{
		{"POST /api/push", 501, "push is not supported by ollame; models are managed upstream"},
		{"POST /api/create", 501, "create is not supported by ollame; define aliases in config"},
		{"POST /api/copy", 501, "copy is not supported by ollame; define aliases in config"},
		{"DELETE /api/delete", 501, "delete is not supported by ollame; models are managed upstream"},
		{"POST /api/blobs/{digest}", 501, "blob upload is not supported by ollame; models are managed upstream"},
		{"POST /api/me", 503, "account unavailable"},
		{"POST /api/experimental/web_search", 501, "web search is not supported by ollame"},
		{"POST /api/experimental/web_fetch", 501, "web search is not supported by ollame"},
		{"GET /api/experimental/model-recommendations", 501, "model recommendations are not supported by ollame"},
		{"POST /api/signout", 200, ""},
		{"DELETE /api/user/keys/{key}", 200, ""},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			state := s.requestSnapshot(r)
			if !s.authorize(w, r, state) {
				return
			}
			body := map[string]string{}
			if route.message != "" {
				body["error"] = route.message
			}
			writeJSON(w, route.status, body)
		})
	}
	mux.HandleFunc("HEAD /api/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorize(w, r, s.requestSnapshot(r)) {
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	state := s.requestSnapshot(r)
	if !s.authorize(w, r, state) {
		return
	}
	if state.config.Management.Pull == "error" {
		writeJSON(w, 501, map[string]string{"error": "pull is not supported by ollame; models are managed upstream"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(state.config.Server.MaxBodyBytes))
	defer r.Body.Close()
	var request struct {
		Model  string `json:"model"`
		Name   string `json:"name"`
		Stream *bool  `json:"stream"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		bodyError(w, err)
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		bodyError(w, err)
		return
	}
	model := request.Model
	if model == "" {
		model = request.Name
	}
	entry, err := state.catalog.Resolve(model)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "pull model manifest: file does not exist"})
		return
	}
	s.recordUse(entry, 5*time.Minute)
	if request.Stream != nil && !*request.Stream {
		writeJSON(w, 200, map[string]string{"status": "success"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	controller := http.NewResponseController(w)
	for _, status := range []string{"pulling manifest", "verifying sha256 digest", "writing manifest", "success"} {
		if err := controller.SetWriteDeadline(time.Now().Add(time.Duration(state.config.Server.WriteStallTimeout))); err != nil {
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"status": status}); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}
