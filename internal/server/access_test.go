package server

import (
	"strings"
	"testing"
	"time"
)

func TestGeneratedRequestID(t *testing.T) {
	before := time.Now().UnixMilli()
	id, err := newRequestID()
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()
	if len(id) != 26 || id[0] > '7' {
		t.Fatalf("invalid ULID: %q", id)
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var timestamp int64
	for i, c := range id {
		digit := strings.IndexRune(alphabet, c)
		if digit < 0 {
			t.Fatal("invalid Crockford base32 character")
		}
		if i < 10 {
			timestamp = timestamp*32 + int64(digit)
		}
	}
	if timestamp < before || timestamp > after {
		t.Fatal("request ID timestamp does not match creation")
	}
	second, err := newRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if id == second {
		t.Fatal("request ID entropy repeated")
	}
}
