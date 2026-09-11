package translate

import (
	"errors"
	"testing"
)

func TestEmbeddingBatchesKeepUsageAfterFailure(t *testing.T) {
	first, err := DecodeEmbeddings([]byte(`{"data":[{"embedding":[3,4]}],"usage":{"prompt_tokens":2}}`), 1, 0, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeEmbeddings([]byte(`{"data":[{"embedding":[3,4,5]}],"usage":{"prompt_tokens":3}}`), 1, 0, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	collected := NewEmbeddingBatches(2, 4096)
	if err := collected.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := collected.Add(second); !errors.Is(err, ErrInvalidEmbeddings) {
		t.Fatal("accepted cross-batch dimension change")
	}
	if vectors, err := collected.Finish(); !errors.Is(err, ErrInvalidEmbeddings) || vectors != nil {
		t.Fatal("returned partial failed batch")
	}
	if tokens, known := collected.Usage(); !known || tokens != 5 {
		t.Fatal("lost successful upstream usage")
	}
	if err := collected.Add(first); !errors.Is(err, ErrInvalidEmbeddings) {
		t.Fatal("failure was not terminal")
	}
}

func TestEmbeddingBatchesCountAndBudget(t *testing.T) {
	batch, err := DecodeEmbeddings([]byte(`{"data":[{"embedding":[3,4]}]}`), 1, 0, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		count int
		limit int64
		adds  int
	}{{2, 4096, 1}, {1, 4096, 2}, {2, 16, 2}} {
		collected := NewEmbeddingBatches(test.count, test.limit)
		for i := 0; i < test.adds; i++ {
			_ = collected.Add(batch)
		}
		if vectors, err := collected.Finish(); !errors.Is(err, ErrInvalidEmbeddings) || vectors != nil {
			t.Fatalf("accepted count/budget violation: %+v", test)
		}
	}
	collected := NewEmbeddingBatches(2, 32)
	if err := collected.Add(batch); err != nil {
		t.Fatal(err)
	}
	if err := collected.Add(batch); err != nil {
		t.Fatal(err)
	}
	if vectors, err := collected.Finish(); err != nil || len(vectors) != 2 || vectors[1][1] != 4 {
		t.Fatal("lost batch order")
	}
	if _, known := collected.Usage(); known {
		t.Fatal("invented absent usage")
	}
}
