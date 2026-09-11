package auth

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
)

func TestTokenGenerationAndHash(t *testing.T) {
	token, digest, err := Generate("ci")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 47 || !strings.HasPrefix(token, "olm_") {
		t.Fatal("invalid generated format")
	}
	hashed, err := Hash(strings.NewReader(token + "\n"))
	if err != nil || hashed != digest {
		t.Fatal("hash mismatch")
	}
	for _, text := range []string{"", "\n", strings.Repeat("x", 4097)} {
		if _, err := Hash(strings.NewReader(text)); !errors.Is(err, ErrInvalidSource) {
			t.Fatal("accepted invalid hash input")
		}
	}
}
func TestCredentialPrecedence(t *testing.T) {
	set, _, err := NewSet([]Source{{ID: "inline", Entries: []Entry{{Name: "ci", Digest: sha256.Sum256([]byte("secret"))}}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults().Auth
	request := http.Request{Header: make(http.Header)}
	request.SetBasicAuth("unused", "secret")
	if name, err := set.Authorize(request.Header, cfg); err != nil || name != "ci" {
		t.Fatal("basic auth failed")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	request.Header.Set("X-Api-Key", "secret")
	if _, err := set.Authorize(request.Header, cfg); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("invalid bearer fell through")
	}
	request.Header.Set("Authorization", "bad")
	if _, err := set.Authorize(request.Header, cfg); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("malformed auth fell through")
	}
	request.Header.Del("Authorization")
	if _, err := set.Authorize(request.Header, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "optional"
	request.Header = make(http.Header)
	if name, err := set.Authorize(request.Header, cfg); err != nil || name != "anonymous" {
		t.Fatal("optional missing auth failed")
	}
	request.Header.Set("Authorization", "Bearer invalid")
	if _, err := set.Authorize(request.Header, cfg); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("optional accepted invalid token")
	}
	cfg.Mode = "disabled"
	if name, err := set.Authorize(request.Header, cfg); err != nil || name != "anonymous" {
		t.Fatal("disabled auth failed")
	}
}
func TestRealTokenSourcesAndKubernetesSymlinks(t *testing.T) {
	root := t.TempDir()
	version := filepath.Join(root, "..version")
	if err := os.Mkdir(version, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "ci"), []byte("directory-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..version", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/ci", filepath.Join(root, "ci")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults().Auth
	cfg.TokensDir = root
	cfg.Tokens = []config.Token{{Name: "ci", Token: "inline-secret"}}
	sources := ReadSources(cfg, nil, nil, nil)
	set, warnings, err := NewSet(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatal("override was not reported")
	}
	if name, err := set.Authenticate("directory-secret"); err != nil || name != "ci" {
		t.Fatal("did not follow projected symlink")
	}
	if _, err := set.Authenticate("inline-secret"); err == nil {
		t.Fatal("lower source remained effective")
	}
	if err := os.Remove(filepath.Join(root, "ci")); err != nil {
		t.Fatal(err)
	}
	set, _, err = NewSet(ReadSources(cfg, nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Authenticate("directory-secret"); err == nil {
		t.Fatal("deleted credential remained valid")
	}
	if _, err := set.Authenticate("inline-secret"); err != nil {
		t.Fatal("lower configured source not restored")
	}
}
func TestFailedSourceGraceDoesNotExtend(t *testing.T) {
	var state State
	now := time.Unix(1000, 0)
	grace := 5 * time.Minute
	good := Source{ID: "directory", Entries: []Entry{{Name: "ci", Digest: sha256.Sum256([]byte("secret"))}}}
	state, old, _, err := state.Update(now, grace, []Source{good})
	if err != nil {
		t.Fatal(err)
	}
	failed := Source{ID: "directory", Err: ErrInvalidSource}
	state, set, failures, err := state.Update(now.Add(time.Second), grace, []Source{failed})
	if err != nil || set.Len() != 1 || len(failures) != 1 || failures[0].Expired {
		t.Fatal("did not retain last good source")
	}
	state, set, _, err = state.Update(now.Add(4*time.Minute), grace, []Source{failed})
	if err != nil || set.Len() != 1 {
		t.Fatal("early expiry")
	}
	_, set, failures, err = state.Update(now.Add(5*time.Minute+time.Second), grace, []Source{failed})
	if err != nil || set.Len() != 0 || !failures[0].Expired {
		t.Fatal("source failure extended stale grace")
	}
	if old.Len() != 1 {
		t.Fatal("in-flight immutable set mutated")
	}
	_, set, _, err = state.Update(now.Add(6*time.Minute), grace, []Source{{ID: "directory"}})
	if err != nil || set.Len() != 0 {
		t.Fatal("successful removal did not revoke")
	}
}
func TestMalformedFileIsRejectedAsWhole(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(file, []byte("ci=good\nbad line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := readTokenFile(file)
	if !errors.Is(source.Err, ErrInvalidSource) || len(source.Entries) != 0 {
		t.Fatal("partial file accepted")
	}
}
func TestAmbiguousTokenRejected(t *testing.T) {
	digest := sha256.Sum256([]byte("same"))
	_, _, err := NewSet([]Source{{ID: "inline", Entries: []Entry{{Name: "one", Digest: digest}, {Name: "two", Digest: digest}}}})
	if !errors.Is(err, ErrAmbiguousToken) {
		t.Fatal("duplicate token value has ambiguous attribution")
	}
}
