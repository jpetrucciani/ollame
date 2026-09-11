package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
)

var (
	ErrInvalidRequest = errors.New("invalid upstream request")
	ErrIdleTimeout    = fmt.Errorf("upstream idle timeout: %w", context.DeadlineExceeded)
)

// RequestInfo contains server-owned correlation values, never client headers.
type RequestInfo struct {
	RequestID          string
	Traceparent        string
	UserAgent          string
	PassthroughHeaders http.Header
	ContentType        string
}

// PostJSON returns headers without consuming response bytes. It must be called
// before writing downstream output. The caller owns and must close Body, even
// for non-2xx responses. The captured catalog and client stay fixed across retries.
func (c *Client) PostJSON(ctx context.Context, path string, body []byte, exposed *catalog.Catalog, info RequestInfo, maxJSONBytes int64) (*http.Response, error) {
	switch path {
	case "chat/completions", "completions", "embeddings", "responses", "responses/compact", "messages":
	default:
		return nil, ErrInvalidRequest
	}
	if maxJSONBytes <= 0 || c.config.RequestTimeout <= 0 || c.config.BodyTimeout <= 0 || c.config.IdleTimeout <= 0 {
		return nil, ErrInvalidRequest
	}
	// Own the bytes so another caller cannot change a validated target mid-retry.
	body = bytes.Clone(body)
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, ErrInvalidRequest
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil {
		return nil, ErrInvalidRequest
	}
	for key := range fields {
		if forbiddenRoutingKey(key) {
			return nil, ErrInvalidRequest
		}
	}
	return c.post(ctx, path, body, model, exposed, info, maxJSONBytes)
}

// PostMultipart independently validates the already-rewritten body before the
// shared sender checks its model against the captured catalog on every attempt.
func (c *Client) PostMultipart(ctx context.Context, body []byte, contentType string, exposed *catalog.Catalog, info RequestInfo, maxBodyBytes, maxJSONBytes int64) (*http.Response, error) {
	if maxBodyBytes <= 0 || int64(len(body)) > maxBodyBytes {
		return nil, ErrInvalidRequest
	}
	body = bytes.Clone(body)
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, ErrInvalidRequest
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	model := ""
	models := 0
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || part.FormName() == "" || forbiddenRoutingKey(part.FormName()) {
			return nil, ErrInvalidRequest
		}
		if part.FormName() == "model" {
			models++
			if models != 1 || part.FileName() != "" {
				return nil, ErrInvalidRequest
			}
			value, err := io.ReadAll(part)
			if err != nil {
				return nil, ErrInvalidRequest
			}
			model = string(value)
		} else if _, err := io.Copy(io.Discard, part); err != nil {
			return nil, ErrInvalidRequest
		}
		if err := part.Close(); err != nil {
			return nil, ErrInvalidRequest
		}
	}
	if models != 1 || model == "" {
		return nil, ErrInvalidRequest
	}
	info.ContentType = contentType
	return c.post(ctx, "audio/transcriptions", body, model, exposed, info, maxJSONBytes)
}

func (c *Client) post(ctx context.Context, path string, body []byte, model string, exposed *catalog.Catalog, info RequestInfo, maxJSONBytes int64) (*http.Response, error) {
	if maxJSONBytes <= 0 || c.config.RequestTimeout <= 0 || c.config.BodyTimeout <= 0 || c.config.IdleTimeout <= 0 {
		return nil, ErrInvalidRequest
	}
	endpoint, err := url.JoinPath(c.config.BaseURL, path)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(c.config.RequestTimeout))
	for attempt := 0; ; attempt++ {
		var received atomic.Bool
		attemptCtx := httptrace.WithClientTrace(requestCtx, &httptrace.ClientTrace{GotFirstResponseByte: func() { received.Store(true) }})
		request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, ErrInvalidRequest
		}
		// Retries belong here. Do not make POST replayable inside http.Transport.
		request.GetBody = nil
		if info.PassthroughHeaders != nil {
			request.Header = PassthroughHeaders(info.PassthroughHeaders)
		}
		for key, value := range c.config.ExtraHeaders {
			request.Header.Set(key, value)
		}
		request.Header.Del("Authorization")
		request.Header.Del("X-Api-Key")
		request.Header.Del("Cookie")
		request.Header.Del("Idempotency-Key")
		request.Header.Del("X-Idempotency-Key")
		request.Header.Del("Accept-Encoding")
		if c.key != "" {
			request.Header.Set("Authorization", "Bearer "+c.key)
		}
		if info.PassthroughHeaders == nil {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
		}
		if info.ContentType != "" {
			request.Header.Set("Content-Type", info.ContentType)
		}
		request.Header.Set("User-Agent", info.UserAgent)
		request.Header.Set("X-Request-Id", info.RequestID)
		request.Header.Set("Traceparent", info.Traceparent)
		// This check is adjacent to the only model-bearing send operation.
		if !exposed.AllowsTarget(model) {
			cancel()
			return nil, catalog.ErrNotFound
		}
		response, err := c.http.Do(request)
		c.recordAttempt(path, response)
		eligible := retryFailure(err, received.Load())
		if err == nil {
			eligible = retryStatus(response.StatusCode)
		}
		if eligible && attempt < c.config.Retries && requestCtx.Err() == nil {
			delay := retryDelay(attempt)
			if deadline, ok := requestCtx.Deadline(); !ok || time.Until(deadline) > delay {
				if response != nil {
					response.Body.Close()
				}
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
					continue
				case <-requestCtx.Done():
					timer.Stop()
					err := requestCtx.Err()
					cancel()
					return nil, err
				}
			}
		}
		if err != nil {
			cause := requestCtx.Err()
			cancel()
			if cause != nil {
				return nil, cause
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				return nil, context.DeadlineExceeded
			}
			// Transport errors can contain URLs and credentials. Keep them private.
			return nil, ErrUnavailable
		}
		mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		wrapped := &responseBody{ReadCloser: response.Body, cancel: cancel, ctx: requestCtx, remaining: maxJSONBytes}
		timeout := time.Duration(c.config.BodyTimeout)
		if mediaType == "text/event-stream" {
			wrapped.unbounded = true // SSE framing/aggregate limits belong to the decoder.
			wrapped.idleTimeout = time.Duration(c.config.IdleTimeout)
			timeout = wrapped.idleTimeout
		}
		wrapped.timer = time.AfterFunc(timeout, func() { wrapped.timedOut.Store(true); cancel() })
		response.Body = wrapped
		return response, nil
	}
}

func forbiddenRoutingKey(key string) bool {
	switch key {
	case "fallbacks", "context_window_fallback_dict", "api_base", "base_url", "api_key", "mock_response", "extra_body":
		return true
	default:
		return strings.HasPrefix(key, "litellm_")
	}
}

func retryStatus(status int) bool { return status == 502 || status == 503 || status == 504 }
func retryFailure(err error, received bool) bool {
	if err == nil || received || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial" || errors.Is(err, syscall.ECONNRESET)
}
func retryDelay(attempt int) time.Duration {
	base := 200 * time.Millisecond
	for i := 0; i < attempt && base < time.Second; i++ {
		base *= 2
	}
	if base > time.Second {
		base = time.Second
	}
	// Equal jitter avoids both synchronized retries and immediate retry loops.
	return base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
}

type responseBody struct {
	io.ReadCloser
	ctx         context.Context
	cancel      context.CancelFunc
	timer       *time.Timer
	timedOut    atomic.Bool
	once        sync.Once
	remaining   int64
	unbounded   bool
	idleTimeout time.Duration
	terminal    error
}

// EventReceived is called by the SSE parser for each complete data event.
// Bytes from a partial event cannot keep a stalled generation alive forever.
func (b *responseBody) EventReceived() {
	if b.idleTimeout > 0 && b.ctx.Err() == nil {
		b.timer.Reset(b.idleTimeout)
	}
}

func (b *responseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.terminal != nil {
		return 0, b.terminal
	}
	if !b.unbounded {
		if b.remaining == 0 {
			var extra [1]byte
			n, err := b.ReadCloser.Read(extra[:])
			if n > 0 {
				b.terminal = ErrResponseTooLarge
				b.Close()
				return 0, b.terminal
			}
			return 0, b.readError(err)
		}
		if int64(len(p)) > b.remaining {
			p = p[:b.remaining]
		}
	}
	n, err := b.ReadCloser.Read(p)
	if !b.unbounded {
		b.remaining -= int64(n)
	}
	return n, b.readError(err)
}

func (b *responseBody) readError(err error) error {
	if b.timedOut.Load() {
		err = context.DeadlineExceeded
		if b.unbounded {
			err = ErrIdleTimeout
		}
	} else if cause := b.ctx.Err(); cause != nil {
		err = cause
	}
	if err != nil {
		b.terminal = err
		b.Close()
	}
	return err
}
func (b *responseBody) Close() error {
	var err error
	b.once.Do(func() {
		if b.timer != nil {
			b.timer.Stop()
		}
		b.cancel()
		err = b.ReadCloser.Close()
	})
	return err
}
