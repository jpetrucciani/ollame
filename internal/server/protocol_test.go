package server

import (
	"encoding/json"
	"testing"
)

func TestProtocolAuthenticationErrors(t *testing.T) {
	for path, want := range map[string]string{
		"/api/chat":            `{"error":"unauthorized"}`,
		"/v1/chat/completions": `{"error":{"code":"unauthorized","message":"unauthorized","param":null,"type":"authentication_error"}}`,
		"/v1/messages":         `{"error":{"message":"unauthorized","type":"authentication_error"},"type":"error"}`,
	} {
		raw, err := json.Marshal(localErrorBody(path, 401, "unauthorized"))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Fatalf("%s: %s", path, raw)
		}
	}
}
