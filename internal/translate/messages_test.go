package translate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

func call(id, name, args string) ToolCall {
	var result ToolCall
	result.ID = id
	result.Function.Name = name
	result.Function.Arguments = json.RawMessage(args)
	return result
}
func TestHistoryToolResolution(t *testing.T) {
	cfg := config.Defaults()
	history := []Message{{Role: "assistant", ToolCalls: []ToolCall{call("a", "first", `{"z":9007199254740993,"a":1}`), call("b", "second", `{}`)}}, {Role: "tool", ToolName: "second", Content: "two"}, {Role: "tool", Content: "one"}}
	result, err := Messages(history, catalog.Entry{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 3 || result.Messages[1].ToolCallID != "b" || result.Messages[2].ToolCallID != "a" {
		t.Fatalf("wrong resolution: %+v", result)
	}
	if result.Messages[0].ToolCalls[0].Function.Arguments != `{"z":9007199254740993,"a":1}` || string(result.Messages[0].Content) != "null" {
		t.Fatal("arguments or assistant null changed")
	}
}
func TestMissingAndDuplicateResults(t *testing.T) {
	cfg := config.Defaults()
	history := []Message{{Role: "assistant", ToolCalls: []ToolCall{call("a", "first", "{}"), call("b", "second", "{}"), call("c", "third", "{}")}}, {Role: "tool", ToolCallID: "a", Content: "one"}, {Role: "tool", ToolCallID: "a", Content: "one"}, {Role: "tool", ToolCallID: "a", Content: "different"}, {Role: "user", Content: "continue"}}
	result, err := Messages(history, catalog.Entry{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dropped) != 1 || result.Synthesized != 1 || len(result.Messages) != 5 {
		t.Fatalf("bad counts: %+v", result)
	}
	if result.Messages[2].ToolCallID != "b" || result.Messages[3].ToolCallID != "c" || result.Messages[4].Role != "user" {
		t.Fatal("duplicate rematch or synthesis order wrong")
	}
	cfg.Compat.MissingToolResult = "passthrough"
	result, err = Messages(history[:1], catalog.Entry{}, cfg)
	if err != nil || result.Synthesized != 0 || len(result.Messages) != 1 {
		t.Fatal("missing-result passthrough failed")
	}
}
func TestDeterministicToolIDsAndOrphans(t *testing.T) {
	cfg := config.Defaults()
	history := []Message{{Role: "assistant", ToolCalls: []ToolCall{call("", "function", "{}")}}}
	first, err := Messages(history, catalog.Entry{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Messages(history, catalog.Entry{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := first.Messages[0].ToolCalls[0].ID
	if id != second.Messages[0].ToolCalls[0].ID || len(id) != 29 || first.Messages[1].ToolCallID != id {
		t.Fatal("unstable tool ID or missing result reference")
	}
	result, err := Messages([]Message{{Role: "tool", ToolName: "weather", Content: "sunny"}}, catalog.Entry{}, cfg)
	if err != nil || result.Messages[0].Role != "user" {
		t.Fatal("orphan conversion failed")
	}
	cfg.Compat.OrphanToolResult = "error"
	if _, err = Messages([]Message{{Role: "tool"}}, catalog.Entry{}, cfg); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("orphan accepted")
	}
}
func TestSystemAndThinkingHistory(t *testing.T) {
	cfg := config.Defaults()
	entry := catalog.Entry{System: "alias system"}
	result, err := Messages([]Message{{Role: "user", Content: "hello"}}, entry, cfg)
	if err != nil || len(result.Messages) != 2 || result.Messages[0].Role != "system" {
		t.Fatal("alias system missing")
	}
	result, err = Messages([]Message{{Role: "SYSTEM", Content: "request"}, {Role: "assistant", Thinking: "private", Content: "answer"}}, entry, cfg)
	if err != nil || len(result.Messages) != 2 || result.Messages[1].ReasoningContent != "" {
		t.Fatal("system override/history policy failed")
	}
	cfg.Compat.HistoryThinking = "reasoning_content"
	result, err = Messages([]Message{{Role: "assistant", Thinking: "reason"}}, entry, cfg)
	if err != nil || result.Messages[1].ReasoningContent != "reason" {
		t.Fatal("reasoning history missing")
	}
}
func TestImagesAndLimits(t *testing.T) {
	cfg := config.Defaults()
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	result, err := Messages([]Message{{Role: "user", Images: []string{png}}}, catalog.Entry{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Messages[0].Content), "data:image/png;base64,") || strings.Contains(string(result.Messages[0].Content), `"text"`) {
		t.Fatal("image-only content incorrectly encoded")
	}
	for _, bad := range []string{"!!!", strings.Repeat("YQ==", 300)} {
		if _, err = Messages([]Message{{Role: "user", Images: []string{bad}}}, catalog.Entry{}, cfg); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("invalid base64 accepted")
		}
	}
	cfg.Limits.MaxImages = 1
	if _, err = Messages([]Message{{Images: []string{png, png}}}, catalog.Entry{}, cfg); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("image limit bypassed")
	}
	cfg.Compat.StrictCapabilities = true
	entry := catalog.Entry{Knowledge: map[string]catalog.Knowledge{"vision": catalog.No}}
	if _, err = Messages([]Message{{Role: "user", Images: []string{png}}}, entry, cfg); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("known-no vision accepted")
	}
}

func TestInvalidToolHistoryRejected(t *testing.T) {
	cfg := config.Defaults()
	for _, calls := range [][]ToolCall{{call("a", "f", "[]")}, {call("a", "f", "null")}, {call("a", "f", "{broken")}, {call("a", "f", "{}"), call("a", "g", "{}")}} {
		if _, err := Messages([]Message{{Role: "assistant", ToolCalls: calls}}, catalog.Entry{}, cfg); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("invalid tool history accepted")
		}
	}
}

func TestSortModelMessages(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "hello"},
		{Role: "SYSTEM", Content: "first"},
		{Role: "assistant", ToolCalls: []ToolCall{call("a", "lookup", "{}")}},
		{Role: "system", Content: "second"},
		{Role: "tool", ToolCallID: "a", Content: "result"},
		{Role: "user", Content: "continue"},
	}
	before, _ := json.Marshal(history)
	cfg := config.Defaults()
	cfg.Compat.SortModelMessages = true
	result, err := Messages(history, catalog.Entry{System: "alias default"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 5 || result.Synthesized != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for i, role := range []string{"system", "user", "assistant", "tool", "user"} {
		if result.Messages[i].Role != role {
			t.Fatalf("message %d: %+v", i, result.Messages[i])
		}
	}
	if string(result.Messages[0].Content) != `"first\n\nsecond"` || result.Messages[3].ToolCallID != "a" {
		t.Fatalf("content or tool linkage changed: %+v", result)
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	disabled := false
	entry := catalog.Entry{Behavior: config.Behavior{SortModelMessages: &disabled}}
	result, err = Messages(history[:2], entry, cfg)
	if err != nil || result.Messages[0].Role != "user" || result.Messages[1].Role != "system" {
		t.Fatalf("override false not honored: %+v, %v", result, err)
	}
	enabled := true
	cfg.Compat.SortModelMessages = false
	entry.Behavior.SortModelMessages = &enabled
	result, err = Messages(history[:2], entry, cfg)
	if err != nil || result.Messages[0].Role != "system" {
		t.Fatalf("override true not honored: %+v, %v", result, err)
	}
	result, err = Messages(history[:2], catalog.Entry{}, cfg)
	if err != nil || result.Messages[0].Role != "user" {
		t.Fatalf("default reordered history: %+v, %v", result, err)
	}
}
