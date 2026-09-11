package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

var ErrBodyTooLarge = errors.New("request body too large")

// Multipart rewrites a fully buffered audio upload without temporary files.
// A validation pass runs before encoding, so duplicate model parts cannot cause
// a partially accepted request. File payloads are copied without interpretation.
func Multipart(raw []byte, contentType string, maxBytes int64, cfg config.Upstream, exposed *catalog.Catalog, token string) ([]byte, string, catalog.Entry, error) {
	invalid := func() ([]byte, string, catalog.Entry, error) {
		return nil, "", catalog.Entry{}, fmt.Errorf("%w: invalid or ambiguous multipart body", ErrInvalidRequest)
	}
	if maxBytes <= 0 || int64(len(raw)) > maxBytes {
		return nil, "", catalog.Entry{}, ErrBodyTooLarge
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return invalid()
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	model := ""
	models := 0
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || part.FormName() == "" {
			return invalid()
		}
		if part.FormName() == "model" {
			models++
			if models != 1 || part.FileName() != "" {
				return invalid()
			}
			value, err := io.ReadAll(part)
			if err != nil {
				return invalid()
			}
			model = string(value)
		}
		if err := part.Close(); err != nil {
			return invalid()
		}
	}
	if models != 1 {
		return invalid()
	}
	modelJSON, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return invalid()
	}
	attributes, entry, err := Passthrough(modelJSON, "audio/transcriptions", cfg, exposed, token)
	if err != nil {
		return nil, "", catalog.Entry{}, err
	}
	var fields Fields
	if err := json.Unmarshal(attributes, &fields); err != nil {
		return invalid()
	}
	var output bytes.Buffer
	bounded := &boundedMultipartWriter{output: &output, remaining: maxBytes}
	writer := multipart.NewWriter(bounded)
	reader = multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return invalid()
		}
		name := part.FormName()
		if name == "model" || name == "metadata" || name == "user" || name == "extra_body" || routingKey(name) {
			_ = part.Close()
			continue
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", part.Header.Get("Content-Disposition"))
		if kind := part.Header.Get("Content-Type"); kind != "" {
			header.Set("Content-Type", kind)
		}
		// Preserve transfer encoding along with the unmodified payload bytes.
		if encoding := part.Header.Get("Content-Transfer-Encoding"); encoding != "" {
			header.Set("Content-Transfer-Encoding", encoding)
		}
		target, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", catalog.Entry{}, err
		}
		if _, err := io.Copy(target, part); err != nil {
			if errors.Is(err, ErrBodyTooLarge) {
				return nil, "", catalog.Entry{}, err
			}
			return invalid()
		}
		if err := part.Close(); err != nil {
			return invalid()
		}
	}
	if err := writer.WriteField("model", entry.Target); err != nil {
		return nil, "", catalog.Entry{}, err
	}
	if err := writer.WriteField("metadata", string(fields["metadata"])); err != nil {
		return nil, "", catalog.Entry{}, err
	}
	if user, ok := fields["user"]; ok {
		var text string
		if err := json.Unmarshal(user, &text); err != nil {
			return invalid()
		}
		if err := writer.WriteField("user", text); err != nil {
			return nil, "", catalog.Entry{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", catalog.Entry{}, err
	}
	return output.Bytes(), writer.FormDataContentType(), entry, nil
}

type boundedMultipartWriter struct {
	output    *bytes.Buffer
	remaining int64
}

func (w *boundedMultipartWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, ErrBodyTooLarge
	}
	n, err := w.output.Write(data)
	w.remaining -= int64(n)
	return n, err
}
