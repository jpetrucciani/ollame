package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func recording(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile("../../test/recordings/stream/chat.sse")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func decode(t *testing.T, reader io.Reader) ([]Chunk, string, error) {
	t.Helper()
	decoder, err := NewDecoder(reader, MaxEventBytes)
	if err != nil {
		t.Fatal(err)
	}
	var chunks []Chunk
	for {
		chunk, err := decoder.Next()
		if err != nil {
			return chunks, decoder.DoneReason(), err
		}
		chunks = append(chunks, chunk)
	}
}
func TestRecordedChat(t *testing.T) {
	chunks, reason, err := decode(t, bytes.NewReader(recording(t)))
	if !errors.Is(err, io.EOF) || reason != "stop" {
		t.Fatalf("finish: %s %v", reason, err)
	}
	var content string
	var usage *Usage
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if content != "hello" || usage == nil || usage.PromptTokens != 19 || usage.CompletionTokens != 2 {
		t.Fatalf("lost recorded response: %s %+v", content, usage)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens == nil || *usage.PromptTokensDetails.CachedTokens != 0 {
		t.Fatal("lost explicit cached count zero")
	}
}
func TestSSEFramingVariants(t *testing.T) {
	data := recording(t)
	expected, _, _ := decode(t, bytes.NewReader(data))
	var multiline bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		payload, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			continue
		}
		multiline.WriteString(": heartbeat\nid: ignored\nretry: 1\nevent: message\n")
		var formatted bytes.Buffer
		if json.Indent(&formatted, payload, "", "  ") == nil {
			for _, part := range bytes.Split(formatted.Bytes(), []byte("\n")) {
				multiline.WriteString("data: ")
				multiline.Write(part)
				multiline.WriteByte('\n')
			}
		} else {
			multiline.Write(line)
			multiline.WriteByte('\n')
		}
		multiline.WriteByte('\n')
	}
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(ending, "\r", "CR"), func(t *testing.T) {
			variant := append([]byte{0xef, 0xbb, 0xbf}, bytes.ReplaceAll(multiline.Bytes(), []byte("\n"), []byte(ending))...)
			got, reason, err := decode(t, iotest.OneByteReader(bytes.NewReader(variant)))
			if !errors.Is(err, io.EOF) || reason != "stop" || !reflect.DeepEqual(expected, got) {
				t.Fatalf("framing changed response: %v", err)
			}
		})
	}
}
func TestRecordedStreamFaults(t *testing.T) {
	data := recording(t)
	firstEnd := bytes.Index(data, []byte("\n\n")) + 2
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"truncated before finish", data[:firstEnd], ErrUnexpectedEOF},
		{"unterminated event", data[:firstEnd-1], ErrUnexpectedEOF},
		{"malformed JSON", bytes.Replace(data, []byte(`{"id"`), []byte(`{invalid"id"`), 1), ErrMalformed},
		{"finish without done", bytes.Replace(data, []byte("data: [DONE]\n\n"), nil, 1), io.EOF},
		{"unknown event type", append([]byte("event: unknown\n"), data...), io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decode(t, bytes.NewReader(tc.data))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
func TestSSEBounds(t *testing.T) {
	data := recording(t)
	reader, err := NewReader(bytes.NewReader(data), 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Next(); !errors.Is(err, ErrLimit) {
		t.Fatalf("line bound failed: %v", err)
	}
	first := bytes.SplitN(data, []byte("\n\n"), 2)[0]
	assembled := bytes.Repeat(append(append([]byte(nil), first...), '\n'), 10)
	assembled = append(assembled, '\n')
	reader, err = NewReader(bytes.NewReader(assembled), len(first)*2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Next(); !errors.Is(err, ErrLimit) {
		t.Fatalf("multiline event bound failed: %v", err)
	}
}
func TestIgnoreNonzeroChoicesAndEmptyEvents(t *testing.T) {
	data := recording(t)
	nonzero := bytes.ReplaceAll(data, []byte(`"index":0`), []byte(`"index":1`))
	chunks, _, err := decode(t, bytes.NewReader(nonzero))
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if len(chunk.Choices) != 0 || chunk.Usage == nil {
			t.Fatal("nonzero choice was retained")
		}
	}
}
func TestThinkSplitterEveryBoundary(t *testing.T) {
	cases := []struct {
		input, content, thinking string
		initial                  bool
	}{
		{"before<think>reason</think>after", "beforeafter", "reason", false},
		{"reason</think>answer", "answer", "reason", true},
		{"unmatched</think>plain", "unmatched</think>plain", "", false},
		{"<think>cut off</thi", "", "cut off</thi", false},
		{"a<think>b</think>c<think>d</think>", "ac<think>d</think>", "b", false},
		{"text<thi", "text<thi", "", false},
		{"reason<thi", "", "reason<thi", true},
	}
	for _, tc := range cases {
		for split := 0; split <= len(tc.input); split++ {
			splitter := NewThinkSplitter(tc.initial)
			var content, thinking strings.Builder
			emit := func(text Text) error {
				content.WriteString(text.Content)
				thinking.WriteString(text.Thinking)
				return nil
			}
			for _, part := range []string{tc.input[:split], tc.input[split:]} {
				if err := splitter.Feed(part, emit); err != nil {
					t.Fatal(err)
				}
				if len(splitter.pending) > 8 {
					t.Fatal("unbounded holdback")
				}
			}
			if err := splitter.Finish(emit); err != nil {
				t.Fatal(err)
			}
			if content.String() != tc.content || thinking.String() != tc.thinking {
				t.Fatalf("%q split %d: %q / %q", tc.input, split, content.String(), thinking.String())
			}
		}
	}
}
func TestThinkSplitterPropagatesWriteFailure(t *testing.T) {
	failure := errors.New("write failed")
	splitter := NewThinkSplitter(false)
	if err := splitter.Feed("output", func(Text) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("write error swallowed")
	}
}
func FuzzThinkChunking(f *testing.F) {
	for _, text := range []string{"hello", "<think>a</think>b", "a<think>b</think>c<think>d</think>", "é<think>模型</think>answer"} {
		f.Add(text, uint16(2), false)
	}
	f.Fuzz(func(t *testing.T, text string, width uint16, initial bool) {
		render := func(chunk int) (string, string) {
			splitter := NewThinkSplitter(initial)
			var content, thinking strings.Builder
			emit := func(text Text) error {
				content.WriteString(text.Content)
				thinking.WriteString(text.Thinking)
				return nil
			}
			for offset := 0; offset < len(text); offset += chunk {
				if err := splitter.Feed(text[offset:min(offset+chunk, len(text))], emit); err != nil {
					t.Fatal(err)
				}
				if len(splitter.pending) > 8 {
					t.Fatal("holdback exceeded")
				}
			}
			if err := splitter.Finish(emit); err != nil {
				t.Fatal(err)
			}
			return content.String(), thinking.String()
		}
		wantContent, wantThinking := render(max(1, len(text)))
		content, thinking := render(int(width)%64 + 1)
		if content != wantContent || thinking != wantThinking {
			t.Fatal("chunk boundaries changed extraction")
		}
	})
}

func TestRecordedEventWithInjectedUpstreamError(t *testing.T) {
	data := recording(t)
	first := bytes.SplitN(data, []byte("\n"), 2)[0]
	payload, ok := bytes.CutPrefix(first, []byte("data: "))
	if !ok {
		t.Fatal("recording has no first data event")
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	injected, err := json.Marshal(map[string]string{"message": "injected upstream failure"})
	if err != nil {
		t.Fatal(err)
	}
	event["error"] = injected
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	variant := append([]byte("data: "), encoded...)
	variant = append(variant, []byte("\n\n")...)
	decoder, err := NewDecoder(bytes.NewReader(variant), MaxEventBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = decoder.Next()
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Message != "injected upstream failure" {
		t.Fatalf("lost upstream error: %v", err)
	}
}
