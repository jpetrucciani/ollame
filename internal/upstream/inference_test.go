package upstream

import (
	"context"
	"errors"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestRetryEligibility(t *testing.T) {
	for status := 100; status < 600; status++ {
		if retryStatus(status) != (status == 502 || status == 503 || status == 504) {
			t.Fatalf("unexpected retry status %d", status)
		}
	}
	dial := &url.Error{Op: "Post", URL: "http://upstream/v1", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}
	reset := &net.OpError{Op: "read", Err: syscall.ECONNRESET}
	for _, err := range []error{dial, reset} {
		if !retryFailure(err, false) || retryFailure(err, true) {
			t.Fatalf("wrong response-byte boundary: %v", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("unclassified"), &net.OpError{Op: "dial", Err: context.DeadlineExceeded}} {
		if retryFailure(err, false) {
			t.Fatalf("unsafe retry: %v", err)
		}
	}
	for attempt := 0; attempt < 100; attempt++ {
		if delay := retryDelay(attempt); delay < 100*time.Millisecond || delay > time.Second {
			t.Fatalf("unbounded delay %s", delay)
		}
	}
}
