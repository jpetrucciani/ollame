package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/jpetrucciani/ollame/internal/obs"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

var errShutdown = errors.New("server shutting down")

func abortReason(ctx context.Context, err error, downstream bool) obs.AbortReason {
	if errors.Is(context.Cause(ctx), errShutdown) {
		return obs.AbortShutdown
	}
	if downstream || errors.Is(ctx.Err(), context.Canceled) {
		return obs.AbortClient
	}
	if errors.Is(err, upstream.ErrIdleTimeout) {
		return obs.AbortIdle
	}
	return obs.AbortUpstream
}
func (s *Server) recordAbort(r *http.Request, err error, downstream bool) {
	details := requestDetails(r)
	if details.aborted {
		return
	}
	details.aborted = true
	details.abortReason = abortReason(r.Context(), err, downstream)
	s.metrics.Abort(details.abortReason)
}
