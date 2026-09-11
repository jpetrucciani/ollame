package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jpetrucciani/ollame/internal/translate"
)

var ErrToolArguments = errors.New("upstream returned invalid tool arguments")

type toolParts struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

// ToolAccumulator keeps fragments by upstream index and emits complete calls in
// index order. Name and ID storage are bounded too, so empty argument fragments
// cannot bypass the response's memory bounds.
type ToolAccumulator struct {
	calls    map[int]*toolParts
	maxCalls int
	maxBytes int
	policy   string
	terminal error
	finished bool
}

func NewToolAccumulator(maxCalls, maxBytes int, policy string) (*ToolAccumulator, error) {
	if maxCalls <= 0 || maxBytes <= 0 || (policy != "wrap" && policy != "error") {
		return nil, ErrLimit
	}
	return &ToolAccumulator{calls: make(map[int]*toolParts), maxCalls: maxCalls, maxBytes: maxBytes, policy: policy}, nil
}

func (a *ToolAccumulator) Add(fragment ToolFragment) error {
	if a.terminal != nil {
		return a.terminal
	}
	if a.finished {
		return ErrMalformed
	}
	if fragment.Index < 0 {
		return a.fail(ErrMalformed)
	}
	call := a.calls[fragment.Index]
	if call == nil {
		if len(a.calls) >= a.maxCalls {
			return a.fail(ErrLimit)
		}
		call = &toolParts{}
		a.calls[fragment.Index] = call
	}
	if len(fragment.ID) > a.maxBytes-call.id.Len() ||
		len(fragment.Function.Name) > a.maxBytes-call.name.Len() ||
		len(fragment.Function.Arguments) > a.maxBytes-call.arguments.Len() {
		return a.fail(ErrLimit)
	}
	call.id.WriteString(fragment.ID)
	call.name.WriteString(fragment.Function.Name)
	call.arguments.WriteString(fragment.Function.Arguments)
	return nil
}

// Finish validates every call before returning any of them. The wrap count is
// returned for telemetry; the original JSON object member order is preserved.
func (a *ToolAccumulator) Finish() ([]translate.ToolCall, int, error) {
	if a.terminal != nil {
		return nil, 0, a.terminal
	}
	if a.finished {
		return nil, 0, ErrMalformed
	}
	a.finished = true
	indexes := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]translate.ToolCall, 0, len(indexes))
	wrapped := 0
	for _, index := range indexes {
		parts := a.calls[index]
		arguments := []byte(parts.arguments.String())
		if len(arguments) == 0 {
			arguments = []byte(`{}`)
		}
		trimmed := bytes.TrimSpace(arguments)
		if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
			if a.policy == "error" {
				return nil, 0, a.fail(ErrToolArguments)
			}
			var err error
			arguments, err = json.Marshal(struct {
				Raw string `json:"_raw"`
			}{Raw: parts.arguments.String()})
			if err != nil {
				return nil, 0, a.fail(ErrToolArguments)
			}
			wrapped++
		} else {
			var compact bytes.Buffer
			if err := json.Compact(&compact, arguments); err != nil {
				return nil, 0, a.fail(ErrToolArguments)
			}
			arguments = compact.Bytes()
		}
		if len(arguments) > a.maxBytes {
			return nil, 0, a.fail(ErrLimit)
		}
		call := translate.ToolCall{ID: parts.id.String()}
		call.Function.Index = index
		call.Function.Name = parts.name.String()
		call.Function.Arguments = arguments
		calls = append(calls, call)
	}
	a.calls = nil
	return calls, wrapped, nil
}

func (a *ToolAccumulator) fail(err error) error {
	a.terminal = err
	a.calls = nil
	return err
}
