package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func recordedToolFragments(t *testing.T) []ToolFragment {
	t.Helper()
	raw, err := os.ReadFile("../../test/recordings/stream/tools.sse")
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(bytes.NewReader(raw), MaxEventBytes)
	if err != nil {
		t.Fatal(err)
	}
	var fragments []ToolFragment
	for {
		chunk, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			fragments = append(fragments, choice.Delta.ToolCalls...)
		}
	}
	if len(fragments) < 2 {
		t.Fatal("recording has no fragmented tool call")
	}
	return fragments
}

func TestRecordedToolAssemblyAndInterleaving(t *testing.T) {
	fragments := recordedToolFragments(t)
	a, err := NewToolAccumulator(2, 1024, "error")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		for _, index := range []int{7, 0} {
			part := fragment
			part.Index = index
			if part.ID != "" && index == 7 {
				part.ID += "second"
			}
			if err := a.Add(part); err != nil {
				t.Fatal(err)
			}
		}
	}
	calls, wrapped, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || wrapped != 0 {
		t.Fatal("lost calls")
	}
	for i, call := range calls {
		if call.Function.Index != []int{0, 7}[i] || call.Function.Name != "get_weather" || string(call.Function.Arguments) != `{"city":"Indianapolis"}` {
			t.Fatalf("incorrect tool assembly: %+v", call)
		}
	}
	if calls[0].ID != fragments[0].ID || calls[1].ID != fragments[0].ID+"second" {
		t.Fatal("lost tool IDs")
	}
}

func TestToolArgumentsPolicyAndTerminalFailure(t *testing.T) {
	fragments := recordedToolFragments(t)
	for _, policy := range []string{"wrap", "error"} {
		a, err := NewToolAccumulator(128, 1024, policy)
		if err != nil {
			t.Fatal(err)
		}
		var original strings.Builder
		// Drop the recorded closing fragment, leaving a truncated JSON object.
		for _, fragment := range fragments[:len(fragments)-1] {
			original.WriteString(fragment.Function.Arguments)
			if err := a.Add(fragment); err != nil {
				t.Fatal(err)
			}
		}
		calls, wrapped, err := a.Finish()
		if policy == "error" {
			if !errors.Is(err, ErrToolArguments) || calls != nil {
				t.Fatalf("partial invalid result: %v", err)
			}
			if !errors.Is(a.Add(fragments[0]), ErrToolArguments) {
				t.Fatal("terminal failure lost")
			}
			continue
		}
		if err != nil || len(calls) != 1 || wrapped != 1 {
			t.Fatalf("wrap failed: %v", err)
		}
		var value struct {
			Raw string `json:"_raw"`
		}
		if err := json.Unmarshal(calls[0].Function.Arguments, &value); err != nil {
			t.Fatal(err)
		}
		if value.Raw != original.String() {
			t.Fatal("wrap lost original arguments")
		}
	}
}

func TestToolStorageBounds(t *testing.T) {
	fragments := recordedToolFragments(t)
	for _, field := range []string{"id", "name", "arguments", "count"} {
		t.Run(field, func(t *testing.T) {
			a, err := NewToolAccumulator(1, 64, "error")
			if err != nil {
				t.Fatal(err)
			}
			part := fragments[0]
			if err := a.Add(part); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "id":
				part.ID = strings.Repeat("x", 65)
			case "name":
				part.Function.Name = strings.Repeat("x", 65)
			case "arguments":
				part.Function.Arguments = strings.Repeat("x", 65)
			case "count":
				part.Index++
			}
			if !errors.Is(a.Add(part), ErrLimit) {
				t.Fatal("accepted oversized tool state")
			}
			if calls, _, err := a.Finish(); !errors.Is(err, ErrLimit) || calls != nil {
				t.Fatal("returned partial calls after overflow")
			}
			if a.calls != nil {
				t.Fatal("retained failed response")
			}
		})
	}
}

func TestToolObjectOrderAndEmptyArguments(t *testing.T) {
	for _, arguments := range []string{"", ` { "z":9007199254740993, "a":1 } `} {
		a, err := NewToolAccumulator(1, 1024, "error")
		if err != nil {
			t.Fatal(err)
		}
		part := ToolFragment{}
		part.Function.Arguments = arguments
		if err := a.Add(part); err != nil {
			t.Fatal(err)
		}
		calls, _, err := a.Finish()
		if err != nil {
			t.Fatal(err)
		}
		want := `{}`
		if arguments != "" {
			want = `{"z":9007199254740993,"a":1}`
		}
		if string(calls[0].Function.Arguments) != want {
			t.Fatal("object representation changed")
		}
	}
}
