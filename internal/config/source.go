package config

import (
	"fmt"
	"os"
)

// Source captures process-static overrides and the selected config path. Reloads
// re-read that path rather than searching again or sampling a changed environment.
type Source struct {
	path  string
	input Input
}

func NewSource(path string, input Input) Source {
	owned := Input{TOML: append([]byte(nil), input.TOML...), Env: append([]string(nil), input.Env...), Tokens: append([]string(nil), input.Tokens...), Aliases: append([]string(nil), input.Aliases...), Flags: make(map[string]string, len(input.Flags))}
	for key, value := range input.Flags {
		owned.Flags[key] = value
	}
	return Source{path: path, input: owned}
}

func (s Source) Load() (Loaded, error) {
	input := s.input
	if s.path != "" {
		var err error
		input.TOML, err = os.ReadFile(s.path)
		if err != nil {
			return Loaded{}, fmt.Errorf("%w: cannot read configuration file", ErrInvalid)
		}
	}
	return Load(input)
}
