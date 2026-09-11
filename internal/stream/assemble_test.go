package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
)

func TestAssemblyRecordedStreamingAndAggregate(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		a, err := NewAssembler(Assembly{Aggregate: aggregate, Visible: true, Limits: config.Defaults().Limits, BadToolArguments: "error"})
		if err != nil {
			t.Fatal(err)
		}
		decoder, err := NewDecoder(bytes.NewReader(recording(t)), MaxEventBytes)
		if err != nil {
			t.Fatal(err)
		}
		var content strings.Builder
		emit := func(out Output) error { content.WriteString(out.Content); return nil }
		for {
			chunk, err := decoder.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Feed(chunk, time.Now(), emit); err != nil {
				t.Fatal(err)
			}
		}
		out, err := a.Finish(emit)
		if err != nil {
			t.Fatal(err)
		}
		if aggregate && content.Len() != 0 {
			t.Fatal("aggregate emitted early")
		}
		content.WriteString(out.Content)
		if content.String() != "hello" || a.Usage == nil || a.First.IsZero() || a.Last.Before(a.First) {
			t.Fatal("lost response data")
		}
	}
}

func TestAssemblyLimitsSuppressionAndBackpressure(t *testing.T) {
	limits := config.Defaults().Limits
	limits.MaxAggregateBytes = 3
	a, err := NewAssembler(Assembly{Aggregate: true, ThinkTags: true, Visible: false, Limits: limits, BadToolArguments: "wrap"})
	if err != nil {
		t.Fatal(err)
	}
	emit := func(Output) error { t.Fatal("aggregate emitted early"); return nil }
	chunk := Chunk{Choices: []Choice{{Delta: Delta{Content: "<think>discard this</think>abc"}}}}
	if err := a.Feed(chunk, time.Now(), emit); err != nil {
		t.Fatal(err)
	}
	chunk.Choices[0].Delta.Content = "d"
	if !errors.Is(a.Feed(chunk, time.Now(), emit), ErrLimit) {
		t.Fatal("unbounded retained content")
	}
	a, err = NewAssembler(Assembly{Visible: true, Limits: limits, BadToolArguments: "wrap"})
	if err != nil {
		t.Fatal(err)
	}
	stalled := errors.New("downstream stalled")
	if err := a.Feed(chunk, time.Now(), func(Output) error { return stalled }); !errors.Is(err, stalled) {
		t.Fatal("downstream failure ignored")
	}
}

func TestEstimatedRecordedUsage(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		for _, removeUsage := range []bool{false, true} {
			a, err := NewAssembler(Assembly{Aggregate: aggregate, Visible: true, Limits: config.Defaults().Limits, BadToolArguments: "error"})
			if err != nil {
				t.Fatal(err)
			}
			decoder, err := NewDecoder(bytes.NewReader(recording(t)), MaxEventBytes)
			if err != nil {
				t.Fatal(err)
			}
			emit := func(Output) error { return nil }
			for {
				chunk, err := decoder.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				// Derive the missing-usage case from the real recorded provider bytes.
				if removeUsage {
					chunk.Usage = nil
				}
				if err := a.Feed(chunk, time.Now(), emit); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Finish(emit); err != nil {
				t.Fatal(err)
			}
			a.EstimateUsage(5)
			if a.Usage == nil || a.UsageEstimated != removeUsage {
				t.Fatal("usage provenance lost")
			}
			wantPrompt := 19
			if removeUsage {
				wantPrompt = 2
			}
			if a.Usage.PromptTokens != wantPrompt || a.Usage.CompletionTokens != 2 {
				t.Fatalf("incorrect rounded usage: %+v", a.Usage)
			}
			if removeUsage && a.Usage.PromptTokensDetails != nil {
				t.Fatal("invented cache usage")
			}
			if !aggregate && (a.content.Len() != 0 || a.thinking.Len() != 0) {
				t.Fatal("stream estimate retained text")
			}
		}
	}
}

func TestEstimateRoundingAndSuppression(t *testing.T) {
	for _, step := range []int{1, 2, 100} {
		a, err := NewAssembler(Assembly{ThinkTags: true, Visible: false, Limits: config.Defaults().Limits, BadToolArguments: "error"})
		if err != nil {
			t.Fatal(err)
		}
		text := "<think>你好</think>é"
		emit := func(Output) error { return nil }
		for start := 0; start < len(text); start += step {
			end := min(start+step, len(text))
			if err := a.Feed(Chunk{Choices: []Choice{{Delta: Delta{Content: text[start:end]}}}}, time.Now(), emit); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.Finish(emit); err != nil {
			t.Fatal(err)
		}
		a.EstimateUsage(0)
		if a.Usage.PromptTokens != 0 || a.Usage.CompletionTokens != 2 {
			t.Fatalf("suppressed thinking or chunk rounding changed estimate: %+v", a.Usage)
		}
	}
	a := &Assembler{Usage: &Usage{}}
	a.EstimateUsage(100)
	if a.UsageEstimated || a.Usage.PromptTokens != 0 {
		t.Fatal("replaced provider zero usage")
	}
}
