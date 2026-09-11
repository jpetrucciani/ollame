package server

import (
	"net/http"
	"net/http/pprof"
)

func (s *Server) profilingRoutes(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /debug/pprof", func(w http.ResponseWriter, r *http.Request) {
			target := "/debug/pprof/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}},
		{"GET /debug/pprof/", pprof.Index},
		{"GET /debug/pprof/cmdline", pprof.Cmdline},
		{"GET /debug/pprof/profile", pprof.Profile},
		{"GET /debug/pprof/symbol", pprof.Symbol},
		{"POST /debug/pprof/symbol", pprof.Symbol},
		{"GET /debug/pprof/trace", pprof.Trace},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			state := s.active.Load()
			if !state.config.Admin.Debug || state.config.Log.Level != "debug" {
				http.NotFound(w, r)
				return
			}
			route.handler(w, r)
		})
	}
}
