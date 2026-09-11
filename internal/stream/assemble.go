package stream

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/translate"
)

type Output struct {
	Content   string
	Thinking  string
	ToolCalls []translate.ToolCall
	Logprobs  []json.RawMessage
}

type Assembly struct {
	Aggregate        bool
	Visible          bool
	ThinkTags        bool
	ThinkInitial     bool
	Limits           config.Limits
	BadToolArguments string
}

// Assembler synchronously translates each chunk and invokes emit before another
// chunk is read. Only aggregate mode retains text and log probabilities.
type Assembler struct {
	options         Assembly
	tools           *ToolAccumulator
	splitter        *ThinkSplitter
	content         strings.Builder
	thinking        strings.Builder
	logprobs        []json.RawMessage
	retained        int64
	Usage           *Usage
	UsageEstimated  bool
	textBytes       int
	First           time.Time
	Last            time.Time
	Reason          string
	ContentFiltered bool
	WrappedTools    int
}

func NewAssembler(options Assembly) (*Assembler, error) {
	tools, err := NewToolAccumulator(options.Limits.MaxToolCalls, int(options.Limits.MaxToolCallBytes), options.BadToolArguments)
	if err != nil {
		return nil, err
	}
	a := &Assembler{options: options, tools: tools, Reason: "stop"}
	if options.ThinkTags {
		a.splitter = NewThinkSplitter(options.ThinkInitial)
	}
	return a, nil
}

func (a *Assembler) output(output Output, emit func(Output) error) error {
	if err := a.countText(len(output.Content)); err != nil {
		return err
	}
	if err := a.countText(len(output.Thinking)); err != nil {
		return err
	}
	if !a.options.Visible {
		output.Thinking = ""
	}
	if a.options.Aggregate {
		additional := int64(len(output.Content)) + int64(len(output.Thinking))
		for _, raw := range output.Logprobs {
			additional += int64(len(raw))
		}
		if additional > int64(a.options.Limits.MaxAggregateBytes)-a.retained {
			return ErrLimit
		}
		a.retained += additional
		a.content.WriteString(output.Content)
		a.thinking.WriteString(output.Thinking)
		a.logprobs = append(a.logprobs, output.Logprobs...)
		return nil
	}
	if output.Content == "" && output.Thinking == "" && len(output.ToolCalls) == 0 && len(output.Logprobs) == 0 {
		return nil
	}
	return emit(output)
}

func (a *Assembler) Feed(chunk Chunk, now time.Time, emit func(Output) error) error {
	if chunk.Usage != nil {
		a.Usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue
		}
		if choice.FinishReason != nil {
			a.Reason = "stop"
			if *choice.FinishReason == "length" {
				a.Reason = "length"
			}
			if *choice.FinishReason == "content_filter" {
				a.ContentFiltered = true
			}
		}
		delta := choice.Delta
		content := delta.Content
		if content == "" {
			content = choice.Text
		}
		reasoning := delta.ReasoningContent
		if reasoning == "" {
			reasoning = delta.Reasoning
		}
		if content != "" || reasoning != "" || len(delta.ToolCalls) > 0 {
			if a.First.IsZero() {
				a.First = now
			}
			a.Last = now
		}
		if err := a.output(Output{Thinking: reasoning}, emit); err != nil {
			return err
		}
		if a.splitter != nil {
			if err := a.splitter.Feed(content, func(text Text) error { return a.output(Output{Content: text.Content, Thinking: text.Thinking}, emit) }); err != nil {
				return err
			}
		} else if err := a.output(Output{Content: content}, emit); err != nil {
			return err
		}
		for _, part := range delta.ToolCalls {
			if err := a.countText(len(part.Function.Arguments)); err != nil {
				return err
			}
			if err := a.tools.Add(part); err != nil {
				return err
			}
		}
		if len(choice.Logprobs) > 0 && string(choice.Logprobs) != "null" {
			probabilities, err := Logprobs(choice.Logprobs)
			if err != nil {
				return err
			}
			if err := a.output(Output{Logprobs: probabilities}, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Assembler) Finish(emit func(Output) error) (Output, error) {
	if a.splitter != nil {
		if err := a.splitter.Finish(func(text Text) error { return a.output(Output{Content: text.Content, Thinking: text.Thinking}, emit) }); err != nil {
			return Output{}, err
		}
	}
	calls, wrapped, err := a.tools.Finish()
	if err != nil {
		return Output{}, err
	}
	a.WrappedTools = wrapped
	if a.options.Aggregate {
		return Output{Content: a.content.String(), Thinking: a.thinking.String(), ToolCalls: calls, Logprobs: a.logprobs}, nil
	}
	if err := a.output(Output{ToolCalls: calls}, emit); err != nil {
		return Output{}, err
	}
	return Output{}, nil
}

// DecodeAggregate converts a bounded non-SSE chat body into the same assembly
// input used by streaming responses, preserving argument strings and logprobs.
func DecodeAggregate(raw []byte) (Chunk, error) {
	var body struct {
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Delta
				ToolCalls []translate.OpenAIToolCall `json:"tool_calls"`
			} `json:"message"`
			Text         string          `json:"text"`
			FinishReason *string         `json:"finish_reason"`
			Logprobs     json.RawMessage `json:"logprobs"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Choices) == 0 {
		return Chunk{}, ErrMalformed
	}
	chunk := Chunk{Usage: body.Usage}
	for _, choice := range body.Choices {
		if choice.Index != 0 {
			continue
		}
		delta := choice.Message.Delta
		for index, call := range choice.Message.ToolCalls {
			part := ToolFragment{Index: index, ID: call.ID}
			part.Function.Name, part.Function.Arguments = call.Function.Name, call.Function.Arguments
			delta.ToolCalls = append(delta.ToolCalls, part)
		}
		chunk.Choices = append(chunk.Choices, Choice{Index: choice.Index, Delta: delta, Text: choice.Text, FinishReason: choice.FinishReason, Logprobs: choice.Logprobs})
	}
	return chunk, nil
}

func (a *Assembler) countText(size int) error {
	if size > math.MaxInt-a.textBytes {
		return ErrLimit
	}
	a.textBytes += size
	return nil
}

// EstimateUsage is called only after a successful response is assembled. Provider
// usage, including an explicit zero, always wins. Round once over the complete
// text, so the result does not depend on SSE chunk boundaries.
func (a *Assembler) EstimateUsage(promptBytes int) {
	if a.Usage != nil || promptBytes < 0 {
		return
	}
	estimate := func(size int) int {
		result := size / 4
		if size%4 != 0 {
			result++
		}
		return result
	}
	a.Usage = &Usage{PromptTokens: estimate(promptBytes), CompletionTokens: estimate(a.textBytes)}
	a.UsageEstimated = true
}
