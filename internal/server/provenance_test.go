package server

import "testing"

func TestAuthProvenancePublication(t *testing.T) {
	active := map[string]string{"auth.mode": "flag", "auth.tokens_file": "toml", "upstream.base_url": "env"}
	candidate := map[string]string{"auth.mode": "toml", "upstream.base_url": "toml"}
	next := mergeAuthProvenance(active, candidate)
	if next["auth.mode"] != "toml" || next["upstream.base_url"] != "env" {
		t.Fatal("auth publication mixed non-auth provenance")
	}
	if _, ok := next["auth.tokens_file"]; ok {
		t.Fatal("removed auth source retained provenance")
	}
	next["auth.mode"] = "changed"
	if active["auth.mode"] != "flag" || candidate["auth.mode"] != "toml" {
		t.Fatal("provenance maps share mutable state")
	}
}
