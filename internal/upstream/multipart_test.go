package upstream

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func TestMultipartSendBoundary(t *testing.T) {
	cfg := config.Defaults()
	cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
	client, err := New(cfg.Upstream, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		models  []string
		routing bool
		want    error
	}{
		{[]string{"allowed"}, false, context.Canceled},
		{[]string{"outside"}, false, catalog.ErrNotFound},
		{[]string{"allowed", "allowed"}, false, ErrInvalidRequest},
		{[]string{"allowed"}, true, ErrInvalidRequest},
		{nil, false, ErrInvalidRequest},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, model := range test.models {
			if err := writer.WriteField("model", model); err != nil {
				t.Fatal(err)
			}
		}
		if test.routing {
			if err := writer.WriteField("fallbacks", "outside"); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.PostMultipart(ctx, body.Bytes(), writer.FormDataContentType(), exposed, RequestInfo{}, 4096, 4096)
		if !errors.Is(err, test.want) {
			t.Fatalf("models=%v routing=%v got %v want %v", test.models, test.routing, err, test.want)
		}
	}
}
