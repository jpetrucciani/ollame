package server

import "net/http"

type snapshotContextKey struct{}

// The API entry point captures one generation before routing. Every stage uses
// that same snapshot even if a reload publishes while the request is running.
func (s *Server) requestSnapshot(r *http.Request) *snapshot {
	if state, ok := r.Context().Value(snapshotContextKey{}).(*snapshot); ok && state != nil {
		return state
	}
	// Direct handler calls outside the API router capture their own generation.
	return s.active.Load()
}
