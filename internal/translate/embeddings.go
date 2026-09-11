package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

var ErrInvalidEmbeddings = errors.New("upstream returned invalid embeddings")

// EmbeddingInputs accepts exactly a string or an array of strings. In particular,
// JSON null elements must not silently decode into empty input strings.
func EmbeddingInputs(raw json.RawMessage, maxInputs int) ([]string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if maxInputs <= 0 || len(raw) == 0 {
		return nil, false, fmt.Errorf("%w: input is required", ErrInvalidRequest)
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, false, fmt.Errorf("%w: invalid input", ErrInvalidRequest)
		}
		return []string{text}, true, nil
	}
	var values []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &values) != nil {
		return nil, false, fmt.Errorf("%w: input must be a string or array of strings", ErrInvalidRequest)
	}
	if len(values) > maxInputs {
		return nil, false, fmt.Errorf("%w: too many embedding inputs", ErrInvalidRequest)
	}
	inputs := make([]string, len(values))
	for i, value := range values {
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &inputs[i]) != nil {
			return nil, false, fmt.Errorf("%w: input must contain only strings", ErrInvalidRequest)
		}
	}
	return inputs, false, nil
}

type EmbeddingBatch struct {
	Vectors [][]float64
	// Width is the original dimension, used to compare separate upstream batches.
	Width         int
	PromptTokens  *int
	Estimated     bool
	RetainedBytes int64
}

// DecodeEmbeddings validates an entire batch before exposing any vectors.
// maxBytes bounds both the encoded response and retained numeric storage.
func DecodeEmbeddings(raw []byte, count, dimensions int, normalize bool, maxBytes int64) (EmbeddingBatch, error) {
	invalid := func(reason string) (EmbeddingBatch, error) {
		return EmbeddingBatch{}, fmt.Errorf("%w: %s", ErrInvalidEmbeddings, reason)
	}
	if count < 0 || dimensions < 0 || maxBytes <= 0 || int64(len(raw)) > maxBytes {
		return invalid("response size limit exceeded")
	}
	var response struct {
		Data []struct {
			Index     json.RawMessage   `json:"index"`
			Embedding []json.RawMessage `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens *int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Data == nil {
		return invalid("invalid response object")
	}
	if len(response.Data) != count {
		return invalid("data count does not match input count")
	}
	batch := EmbeddingBatch{Vectors: make([][]float64, count), PromptTokens: response.Usage.PromptTokens}
	indexed := count > 0 && len(response.Data[0].Index) > 0
	seen := make([]bool, count)
	for position, entry := range response.Data {
		if (len(entry.Index) > 0) != indexed {
			return invalid("mixed indexed and unindexed entries")
		}
		index := position
		if indexed {
			if !numericLiteral(entry.Index) || json.Unmarshal(entry.Index, &index) != nil || index < 0 || index >= count {
				return invalid("index out of range or invalid")
			}
		}
		if seen[index] {
			return invalid("duplicate index")
		}
		seen[index] = true
		if entry.Embedding == nil {
			return invalid("vector is missing or invalid")
		}
		width := len(entry.Embedding)
		if position == 0 {
			batch.Width = width
		} else if width != batch.Width {
			return invalid("inconsistent vector dimensions")
		}
		if width < dimensions {
			return invalid("vector shorter than requested dimensions")
		}
		retainedWidth := width
		if dimensions > 0 && width > dimensions {
			retainedWidth = dimensions
		}
		if int64(retainedWidth) > (maxBytes-batch.RetainedBytes)/8 {
			return invalid("retained output size limit exceeded")
		}
		vector := make([]float64, retainedWidth)
		for i, rawValue := range entry.Embedding {
			var value float64
			if !numericLiteral(rawValue) || json.Unmarshal(rawValue, &value) != nil || math.IsInf(value, 0) || math.IsNaN(value) {
				return invalid("non-numeric or non-finite vector element")
			}
			if i < retainedWidth {
				vector[i] = value
			}
		}
		if normalize || retainedWidth < width {
			normalizeVector(vector)
		}
		batch.Vectors[index] = vector
		batch.RetainedBytes += int64(retainedWidth) * 8
	}
	return batch, nil
}

// Scaling before squaring handles very large and subnormal finite inputs.
func normalizeVector(vector []float64) {
	scale := 0.0
	for _, value := range vector {
		scale = math.Max(scale, math.Abs(value))
	}
	if scale == 0 {
		return
	}
	squares := 0.0
	for _, value := range vector {
		scaled := value / scale
		squares += scaled * scaled
	}
	norm := math.Sqrt(squares)
	for i, value := range vector {
		vector[i] = (value / scale) / norm
	}
}

// EstimateUsage fills only absent provider usage. Each upstream batch is a
// separate request; combine its UTF-8 input bytes before rounding once.
func (b *EmbeddingBatch) EstimateUsage(inputs []string) {
	if b.PromptTokens != nil {
		return
	}
	count, remainder := 0, 0
	for _, input := range inputs {
		count += len(input) / 4
		remainder += len(input) % 4
		count += remainder / 4
		remainder %= 4
	}
	if remainder != 0 {
		count++
	}
	b.PromptTokens, b.Estimated = &count, true
}
