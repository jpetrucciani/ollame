package translate

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func TestRecordedEmbeddingResponse(t *testing.T) {
	raw, err := os.ReadFile("../../test/recordings/embedding/response.json")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := DecodeEmbeddings(raw, 2, 0, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Width != 768 || len(batch.Vectors) != 2 || batch.PromptTokens == nil || *batch.PromptTokens <= 0 {
		t.Fatal("recorded embedding contract changed")
	}
	normalized, err := DecodeEmbeddings(raw, 2, 8, true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, vector := range normalized.Vectors {
		squares := 0.0
		for _, value := range vector {
			squares += value * value
		}
		if len(vector) != 8 || math.Abs(squares-1) > 1e-12 {
			t.Fatal("recorded vector truncation failed")
		}
	}
}

func TestEmbeddingInputs(t *testing.T) {
	for _, raw := range []string{`null`, `[null]`, `[1]`, `{}`, `["a","b","c"]`} {
		if _, _, err := EmbeddingInputs(json.RawMessage(raw), 2); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`""`, `"hello"`, `[]`, `["a","b"]`} {
		if _, _, err := EmbeddingInputs(json.RawMessage(raw), 2); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
}

func TestEmbeddingOrderingNormalizationAndTruncation(t *testing.T) {
	raw := []byte(`{"data":[{"index":1,"embedding":[0,0,0]},{"index":0,"embedding":[3,4,12]}],"usage":{"prompt_tokens":7}}`)
	batch, err := DecodeEmbeddings(raw, 2, 2, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Width != 3 || batch.RetainedBytes != 32 || batch.PromptTokens == nil || *batch.PromptTokens != 7 {
		t.Fatal("lost batch metadata")
	}
	if math.Abs(batch.Vectors[0][0]-.6) > 1e-15 || math.Abs(batch.Vectors[0][1]-.8) > 1e-15 || batch.Vectors[1][0] != 0 || batch.Vectors[1][1] != 0 {
		t.Fatal("incorrect ordering or normalization")
	}
	batch, err = DecodeEmbeddings(raw, 2, 0, false, 4096)
	if err != nil || batch.Vectors[0][2] != 12 {
		t.Fatal("legacy vector changed")
	}
	batch, err = DecodeEmbeddings([]byte(`{"data":[{"embedding":[3,4]},{"embedding":[4,3]}]}`), 2, 0, false, 4096)
	if err != nil || batch.Vectors[0][0] != 3 || batch.Vectors[1][0] != 4 {
		t.Fatal("unindexed order changed")
	}
}

func TestEmbeddingInvalidResponsesAreAllOrNothing(t *testing.T) {
	for _, raw := range []string{
		`{"data":[]}`,
		`{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[1,2]}]}`,
		`{"data":[{"index":0,"embedding":[1,2]},{"index":2,"embedding":[1,2]}]}`,
		`{"data":[{"index":0,"embedding":[1,2]},{"embedding":[1,2]}]}`,
		`{"data":[{"embedding":[1,2]},{"embedding":[1]}]}`,
		`{"data":[{"embedding":[1,2]},{"embedding":[1,null]}]}`,
		`{"data":[{"embedding":[1,2]},{"embedding":[1,"2"]}]}`,
		`{"data":[{"embedding":[1,2]},{"embedding":[1,1e999]}]}`,
		`{"data":[{"embedding":[1]},{"embedding":[2]}]}`,
	} {
		batch, err := DecodeEmbeddings([]byte(raw), 2, 2, true, 4096)
		if !errors.Is(err, ErrInvalidEmbeddings) || batch.Vectors != nil {
			t.Fatalf("partial or accepted invalid response: %s, %v", raw, err)
		}
	}
}

func TestEmbeddingExtremeFiniteNormalization(t *testing.T) {
	for _, values := range [][]float64{{math.MaxFloat64, math.MaxFloat64}, {math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}, {0, 0}} {
		normalizeVector(values)
		if values[0] == 0 {
			if values[1] != 0 {
				t.Fatal("zero norm changed")
			}
			continue
		}
		if math.Abs(values[0]-1/math.Sqrt2) > 1e-15 || math.IsNaN(values[1]) {
			t.Fatalf("unstable norm: %v", values)
		}
	}
}

func TestEmbeddingSizeBounds(t *testing.T) {
	raw := []byte(`{"data":[{"embedding":[` + strings.Repeat("0,", 99) + `0]}]}`)
	// Encoded zeros fit this budget, but their retained float64 storage does not.
	if len(raw) >= 400 {
		t.Fatal("fixture exceeds encoded budget")
	}
	if batch, err := DecodeEmbeddings(raw, 1, 0, false, 400); !errors.Is(err, ErrInvalidEmbeddings) || batch.Vectors != nil {
		t.Fatal("unbounded retained vectors")
	}
	if batch, err := DecodeEmbeddings(raw, 1, 0, false, 10); !errors.Is(err, ErrInvalidEmbeddings) || batch.Vectors != nil {
		t.Fatal("unbounded encoded response")
	}
	batch, err := DecodeEmbeddings(raw, 1, 2, false, 400)
	if err != nil || batch.RetainedBytes != 16 || len(batch.Vectors[0]) != 2 {
		t.Fatal("truncation retained discarded dimensions")
	}
}

func TestEmbeddingUsageEstimates(t *testing.T) {
	raw, err := os.ReadFile("../../test/recordings/embedding/response.json")
	if err != nil {
		t.Fatal(err)
	}
	reported, err := DecodeEmbeddings(raw, 2, 0, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	original := *reported.PromptTokens
	reported.EstimateUsage([]string{"x", "y"})
	if reported.Estimated || *reported.PromptTokens != original {
		t.Fatal("replaced reported usage")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "usage")
	missing, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	estimated, err := DecodeEmbeddings(missing, 2, 0, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	estimated.EstimateUsage([]string{"é", "你好"})
	if !estimated.Estimated || estimated.PromptTokens == nil || *estimated.PromptTokens != 2 {
		t.Fatal("UTF-8 batch estimate wrong")
	}
	batches := NewEmbeddingBatches(4, 1<<20)
	if err := batches.Add(reported); err != nil {
		t.Fatal(err)
	}
	if err := batches.Add(estimated); err != nil {
		t.Fatal(err)
	}
	if vectors, err := batches.Finish(); err != nil || len(vectors) != 4 {
		t.Fatal("mixed-usage batches failed")
	}
	if total, known := batches.Usage(); !known || total != original+2 || !batches.HasEstimates() {
		t.Fatal("mixed usage total or provenance lost")
	}
	batches.Fail("later failure")
	if total, known := batches.Usage(); !known || total != original+2 || !batches.HasEstimates() {
		t.Fatal("usage lost after failure")
	}
	zero := 0
	batch := EmbeddingBatch{PromptTokens: &zero}
	batch.EstimateUsage([]string{"nonempty"})
	if batch.Estimated || *batch.PromptTokens != 0 {
		t.Fatal("replaced explicit zero")
	}
	batch = EmbeddingBatch{}
	batch.EstimateUsage([]string{"a", "b", "c", "d"})
	if *batch.PromptTokens != 1 {
		t.Fatal("rounded per input instead of per batch")
	}
}
