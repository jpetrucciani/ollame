package translate

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func multipartInput(t *testing.T, models []string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, model := range models {
		if err := writer.WriteField("model", model); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"api_key", "fallbacks", "litellm_params", "metadata", "user", "extra_body"} {
		if err := writer.WriteField(name, "client-controlled"); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile("file", "clip.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0, 1, 2, 255, 13, 10}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}
func TestMultipartBoundaryAndFileFidelity(t *testing.T) {
	cfg := config.Defaults()
	exposed, _, err := catalog.Build(cfg, []catalog.Model{{ID: "allowed"}}, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw, kind := multipartInput(t, []string{"allowed:latest"})
	encoded, newType, entry, err := Multipart(raw, kind, 1<<20, cfg.Upstream, exposed, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if kind == newType || entry.Target != "allowed" {
		t.Fatal("boundary or target not rewritten")
	}
	_, params, err := mime.ParseMediaType(newType)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(encoded), params["boundary"])
	fields := map[string][]byte{}
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		fields[part.FormName()] = data
	}
	if len(fields) != 4 || string(fields["model"]) != "allowed" || string(fields["user"]) != "ollame:ci" || string(fields["metadata"]) != `{"tags":["ollame","token:ci"]}` {
		t.Fatal("routing fields or attribution escaped")
	}
	if !bytes.Equal(fields["file"], []byte{0, 1, 2, 255, 13, 10}) {
		t.Fatal("file payload changed")
	}
	if _, _, _, err := Multipart(raw, kind, 10, cfg.Upstream, exposed, "ci"); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatal("body limit bypassed")
	}
	for _, models := range [][]string{nil, {"allowed", "allowed"}, {"outside"}} {
		raw, kind := multipartInput(t, models)
		if _, _, _, err := Multipart(raw, kind, 1<<20, cfg.Upstream, exposed, "ci"); err == nil {
			t.Fatalf("accepted invalid models %v", models)
		}
	}
	for _, test := range []struct {
		raw  []byte
		kind string
	}{
		{raw, "application/json"},
		{raw, "multipart/form-data"},
		{raw[:len(raw)-8], kind},
	} {
		if _, _, _, err := Multipart(test.raw, test.kind, 1<<20, cfg.Upstream, exposed, "ci"); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("malformed multipart accepted: %v", err)
		}
	}
}
