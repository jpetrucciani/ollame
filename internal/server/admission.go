package server

import "net/http"

// Count unlimited requests too: a reload may introduce a limit while they are
// still running. Existing requests finish; new requests never wait for a slot.
func (s *Server) admit(w http.ResponseWriter, r *http.Request, state *snapshot) bool {
	limit := int64(state.config.Server.MaxInflight)
	for {
		current := s.inflight.Load()
		if limit > 0 && current >= limit {
			localError(w, r, http.StatusServiceUnavailable, "server busy")
			return false
		}
		if s.inflight.CompareAndSwap(current, current+1) {
			return true
		}
	}
}
