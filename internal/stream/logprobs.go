package stream

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
)

type tokenProbability struct {
	Token   string          `json:"token"`
	Logprob json.RawMessage `json:"logprob"`
	Bytes   []int           `json:"bytes,omitempty"`
}
type tokenLogprob struct {
	tokenProbability
	TopLogprobs []tokenProbability `json:"top_logprobs,omitempty"`
}

// Logprobs normalizes chat objects and legacy completion arrays to the pinned
// Ollama shape. Missing probabilities are not invented as zero probabilities.
func Logprobs(raw json.RawMessage) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrMalformed
	}
	var values []tokenLogprob
	if content, ok := fields["content"]; ok && !bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
		if json.Unmarshal(content, &values) != nil {
			return nil, ErrMalformed
		}
	} else if _, ok := fields["tokens"]; ok {
		var legacy struct {
			Tokens []string                     `json:"tokens"`
			Scores []json.RawMessage            `json:"token_logprobs"`
			Top    []map[string]json.RawMessage `json:"top_logprobs"`
		}
		if json.Unmarshal(raw, &legacy) != nil || len(legacy.Tokens) != len(legacy.Scores) || (len(legacy.Top) > 0 && len(legacy.Top) != len(legacy.Tokens)) {
			return nil, ErrMalformed
		}
		for i, token := range legacy.Tokens {
			value := tokenLogprob{tokenProbability: tokenProbability{Token: token, Logprob: legacy.Scores[i], Bytes: tokenBytes(token)}}
			if len(legacy.Top) > 0 {
				for token, score := range legacy.Top[i] {
					if _, err := probability(score); err != nil {
						return nil, err
					}
					value.TopLogprobs = append(value.TopLogprobs, tokenProbability{Token: token, Logprob: score, Bytes: tokenBytes(token)})
				}
				sort.Slice(value.TopLogprobs, func(i, j int) bool {
					a, b := value.TopLogprobs[i], value.TopLogprobs[j]
					x, _ := probability(a.Logprob)
					y, _ := probability(b.Logprob)
					if x == y {
						return a.Token < b.Token
					}
					return x > y
				})
			}
			values = append(values, value)
		}
	} else if _, ok := fields["content"]; ok {
		return nil, nil
	} else {
		return nil, ErrMalformed
	}
	output := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		if err := validateProbability(value.tokenProbability); err != nil {
			return nil, err
		}
		for _, top := range value.TopLogprobs {
			if err := validateProbability(top); err != nil {
				return nil, err
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrMalformed
		}
		output = append(output, encoded)
	}
	return output, nil
}
func tokenBytes(token string) []int {
	result := make([]int, len(token))
	for i := range len(token) {
		result[i] = int(token[i])
	}
	return result
}
func validateProbability(value tokenProbability) error {
	if _, err := probability(value.Logprob); err != nil {
		return err
	}
	for _, b := range value.Bytes {
		if b < 0 || b > 255 {
			return ErrMalformed
		}
	}
	return nil
}
func probability(raw json.RawMessage) (float64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || !(raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9') {
		return 0, ErrMalformed
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, ErrMalformed
	}
	value, err := number.Float64()
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, ErrMalformed
	}
	return value, nil
}
