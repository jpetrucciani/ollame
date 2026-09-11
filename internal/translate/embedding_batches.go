package translate

import "fmt"

// EmbeddingBatches retains validated batches until the entire input succeeds.
// Usage remains available after failure because successful earlier calls cost
// tokens even when the final response cannot be returned.
type EmbeddingBatches struct {
	vectors    [][]float64
	expected   int
	width      int
	bytes      int64
	limit      int64
	usage      int
	usageKnown bool
	estimated  bool
	failed     error
}

func NewEmbeddingBatches(expected int, limit int64) *EmbeddingBatches {
	return &EmbeddingBatches{expected: expected, limit: limit, vectors: make([][]float64, 0), usageKnown: true}
}
func (b *EmbeddingBatches) Add(batch EmbeddingBatch) error {
	if b.failed != nil {
		return b.failed
	}
	if batch.PromptTokens == nil {
		b.usageKnown = false
	} else if *batch.PromptTokens >= 0 && *batch.PromptTokens <= int(^uint(0)>>1)-b.usage {
		b.usage += *batch.PromptTokens
		b.estimated = b.estimated || batch.Estimated
	} else {
		b.usageKnown = false
	}
	if len(b.vectors) > 0 && batch.Width != b.width {
		return b.Fail("inconsistent dimensions across batches")
	}
	if len(batch.Vectors) > b.expected-len(b.vectors) {
		return b.Fail("too many vectors across batches")
	}
	if batch.RetainedBytes < 0 || batch.RetainedBytes > b.limit-b.bytes {
		return b.Fail("retained output size limit exceeded")
	}
	b.width = batch.Width
	b.bytes += batch.RetainedBytes
	b.vectors = append(b.vectors, batch.Vectors...)
	return nil
}
func (b *EmbeddingBatches) Fail(reason string) error {
	if b.failed == nil {
		b.failed = fmt.Errorf("%w: %s", ErrInvalidEmbeddings, reason)
	}
	b.vectors = nil
	return b.failed
}
func (b *EmbeddingBatches) Usage() (int, bool) { return b.usage, b.usageKnown }
func (b *EmbeddingBatches) Finish() ([][]float64, error) {
	if b.failed != nil {
		return nil, b.failed
	}
	if len(b.vectors) != b.expected {
		return nil, b.Fail("incomplete embedding batches")
	}
	return b.vectors, nil
}

func (b *EmbeddingBatches) HasEstimates() bool { return b.estimated }
