package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jpetrucciani/ollame/internal/obs"
	"github.com/jpetrucciani/ollame/internal/upstream"
)

func TestAbortReasons(t *testing.T) {
	shutdown, cancelShutdown := context.WithCancelCause(context.Background())
	cancelShutdown(errShutdown)
	client, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	for _, tc := range []struct {
		ctx        context.Context
		err        error
		downstream bool
		want       obs.AbortReason
	}{
		{shutdown, context.Canceled, false, obs.AbortShutdown},
		{shutdown, errors.New("write failed"), true, obs.AbortShutdown},
		{client, context.Canceled, false, obs.AbortClient},
		{context.Background(), errors.New("write failed"), true, obs.AbortClient},
		{context.Background(), upstream.ErrIdleTimeout, false, obs.AbortIdle},
		{context.Background(), context.DeadlineExceeded, false, obs.AbortUpstream},
		{context.Background(), upstream.ErrUnavailable, false, obs.AbortUpstream},
	} {
		if got := abortReason(tc.ctx, tc.err, tc.downstream); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
	if !errors.Is(upstream.ErrIdleTimeout, context.DeadlineExceeded) {
		t.Fatal("idle timeout lost HTTP timeout mapping")
	}
}
