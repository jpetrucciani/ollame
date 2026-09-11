// Package auth owns token digests and source-level revocation state.
package auth

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
)

var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrInvalidSource  = errors.New("invalid token source")
	ErrAmbiguousToken = errors.New("token value has multiple names")
)

type Entry struct {
	Name   string
	Digest [32]byte
}
type Source struct {
	ID      string
	Entries []Entry
	Err     error
}
type Set struct{ byDigest map[[32]byte]string }

func NewSet(sources []Source) (Set, []string, error) {
	byName := map[string]Entry{}
	warnings := []string{}
	for _, source := range sources {
		if source.Err != nil {
			return Set{}, nil, source.Err
		}
		seen := map[string]bool{}
		for _, entry := range source.Entries {
			if !config.ValidTokenName(entry.Name) || seen[entry.Name] {
				return Set{}, nil, ErrInvalidSource
			}
			seen[entry.Name] = true
			if _, exists := byName[entry.Name]; exists {
				warnings = append(warnings, "token "+entry.Name+" overridden by "+source.ID)
			}
			byName[entry.Name] = entry
		}
	}
	result := Set{byDigest: map[[32]byte]string{}}
	for _, entry := range byName {
		if _, exists := result.byDigest[entry.Digest]; exists {
			return Set{}, nil, ErrAmbiguousToken
		}
		result.byDigest[entry.Digest] = entry.Name
	}
	return result, warnings, nil
}
func (s Set) Len() int { return len(s.byDigest) }
func (s Set) Authenticate(credential string) (string, error) {
	digest := sha256.Sum256([]byte(credential))
	name, ok := s.byDigest[digest]
	if !ok {
		return "", ErrUnauthorized
	}
	// The lookup never compares plaintext. Confirm digest equality in constant time.
	for stored, storedName := range s.byDigest {
		if storedName == name && subtle.ConstantTimeCompare(stored[:], digest[:]) == 1 {
			return name, nil
		}
	}
	return "", ErrUnauthorized
}
func Generate(name string) (string, string, error) {
	if !config.ValidTokenName(name) {
		return "", "", ErrInvalidSource
	}
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var chars [43]byte
	for i := 0; i < len(chars); {
		var sample [64]byte
		if _, err := rand.Read(sample[:]); err != nil {
			return "", "", fmt.Errorf("generate token: %w", err)
		}
		for _, b := range sample {
			if b >= 248 {
				continue
			}
			chars[i] = alphabet[int(b)%len(alphabet)]
			i++
			if i == len(chars) {
				break
			}
		}
	}
	token := "olm_" + string(chars[:])
	digest := sha256.Sum256([]byte(token))
	return token, "sha256:" + hex.EncodeToString(digest[:]), nil
}
func Hash(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil {
		return "", err
	}
	if len(body) > 4096 {
		return "", ErrInvalidSource
	}
	token := strings.TrimSuffix(string(body), "\n")
	if token == "" {
		return "", ErrInvalidSource
	}
	digest := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func ReadSources(cfg config.Auth, environment map[string]string, envTokens, flagTokens []config.Token) []Source {
	result := []Source{readDefinitions("inline", cfg.Tokens, environment)}
	if cfg.TokensFile != "" {
		result = append(result, readTokenFile(cfg.TokensFile))
	}
	if cfg.TokensDir != "" {
		result = append(result, readDirectory(cfg.TokensDir))
	}
	result = append(result, readDefinitions("env", envTokens, environment), readDefinitions("flags", flagTokens, environment))
	return result
}
func readDefinitions(id string, tokens []config.Token, environment map[string]string) Source {
	source := Source{ID: id}
	seen := map[string]bool{}
	for _, token := range tokens {
		if !config.ValidTokenName(token.Name) || seen[token.Name] {
			source.Err = ErrInvalidSource
			return source
		}
		seen[token.Name] = true
		var digest [32]byte
		if token.SHA256 != "" {
			raw, err := hex.DecodeString(token.SHA256)
			if err != nil || len(raw) != len(digest) {
				source.Err = ErrInvalidSource
				return source
			}
			copy(digest[:], raw)
		} else {
			value := token.Token
			if token.TokenEnv != "" {
				value = environment[token.TokenEnv]
			}
			if token.TokenFile != "" {
				raw, err := readSecret(token.TokenFile)
				if err != nil {
					source.Err = ErrInvalidSource
					return source
				}
				value = raw
			}
			if value == "" {
				source.Err = ErrInvalidSource
				return source
			}
			digest = sha256.Sum256([]byte(value))
		}
		source.Entries = append(source.Entries, Entry{Name: token.Name, Digest: digest})
	}
	return source
}
func readSecret(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrInvalidSource
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return "", ErrInvalidSource
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", ErrInvalidSource
	}
	return value, nil
}
func readTokenFile(path string) Source {
	source := Source{ID: "file"}
	file, err := os.Open(path)
	if err != nil {
		source.Err = ErrInvalidSource
		return source
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 65536)
	var lines []string
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if scanner.Err() != nil {
		source.Err = ErrInvalidSource
		return source
	}
	tokens, err := config.ParseTokens(lines)
	if err != nil {
		source.Err = ErrInvalidSource
		return source
	}
	return readDefinitions(source.ID, tokens, nil)
}
func readDirectory(path string) Source {
	source := Source{ID: "directory"}
	entries, err := os.ReadDir(path)
	if err != nil {
		source.Err = ErrInvalidSource
		return source
	}
	var tokens []config.Token
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.IsDir() {
			continue
		}
		full := filepath.Join(path, entry.Name())
		info, err := os.Stat(full)
		if err != nil {
			source.Err = ErrInvalidSource
			return source
		}
		if !info.Mode().IsRegular() {
			continue
		}
		value, err := readSecret(full)
		if err != nil {
			source.Err = ErrInvalidSource
			return source
		}
		tokens = append(tokens, config.Token{Name: entry.Name(), Token: value})
	}
	return readDefinitions(source.ID, tokens, nil)
}

// Authorize resolves one credential using the configured precedence. A malformed
// present Authorization header never falls through to another credential.
func (s Set) Authorize(headers http.Header, cfg config.Auth) (string, error) {
	if cfg.Mode == "disabled" {
		return "anonymous", nil
	}
	values := headers.Values("Authorization")
	credential := ""
	present := len(values) > 0
	if len(values) > 1 {
		return "", ErrUnauthorized
	}
	if present {
		scheme, value, ok := strings.Cut(values[0], " ")
		if !ok {
			return "", ErrUnauthorized
		}
		switch {
		case strings.EqualFold(scheme, "Bearer"):
			credential = value
		case strings.EqualFold(scheme, "Basic") && cfg.AcceptBasic:
			request := http.Request{Header: headers}
			user, pass, ok := request.BasicAuth()
			if !ok {
				return "", ErrUnauthorized
			}
			credential = pass
			if pass == "" {
				credential = user
			}
		default:
			return "", ErrUnauthorized
		}
	} else if cfg.AcceptXAPIKey {
		keys := headers.Values("X-Api-Key")
		if len(keys) > 1 {
			return "", ErrUnauthorized
		}
		if len(keys) == 1 {
			credential = keys[0]
			present = true
		}
	}
	if !present && cfg.Mode == "optional" {
		return "anonymous", nil
	}
	if credential == "" {
		return "", ErrUnauthorized
	}
	return s.Authenticate(credential)
}

type sourceState struct {
	entries  []Entry
	failedAt time.Time
	failed   bool
}
type State struct{ sources map[string]sourceState }
type Failure struct {
	Source  string
	Expired bool
}

// Update is pure with respect to the prior state. A caller publishes the returned
// state and token set together, independently of any upstream reload failure.
func (s State) Update(now time.Time, grace time.Duration, results []Source) (State, Set, []Failure, error) {
	next := State{sources: map[string]sourceState{}}
	effective := make([]Source, 0, len(results))
	var failures []Failure
	for _, result := range results {
		prior := s.sources[result.ID]
		if result.Err == nil {
			prior = sourceState{entries: append([]Entry(nil), result.Entries...)}
		} else {
			if !prior.failed {
				prior.failed = true
				prior.failedAt = now
			}
			expired := now.Sub(prior.failedAt) >= grace
			if expired {
				prior.entries = nil
			}
			failures = append(failures, Failure{Source: result.ID, Expired: expired})
		}
		next.sources[result.ID] = prior
		effective = append(effective, Source{ID: result.ID, Entries: prior.entries})
	}
	set, _, err := NewSet(effective)
	if err != nil {
		return s, Set{}, failures, err
	}
	return next, set, failures, nil
}
func (s Set) Names() []string {
	names := make([]string, 0, len(s.byDigest))
	for _, name := range s.byDigest {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
