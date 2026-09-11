package stream

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestRecordedCompletionLogprobs(t *testing.T) {
	raw, err := os.ReadFile("../../test/recordings/stream/completion.json")
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := DecodeAggregate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatal("recording lost choice")
	}
	values, err := Logprobs(chunk.Choices[0].Logprobs)
	if err != nil || len(values) != 4 {
		t.Fatalf("recorded probabilities: %d, %v", len(values), err)
	}
	for _, raw := range values {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["id"]; ok {
			t.Fatal("provider token ID leaked")
		}
		if len(fields["bytes"]) == 0 || len(fields["top_logprobs"]) == 0 {
			t.Fatal("lost probability fields")
		}
	}
}

func TestLegacyLogprobConversion(t *testing.T) {
	raw := json.RawMessage(`{"tokens":["é"],"token_logprobs":[-0.1234567890123456789],"top_logprobs":[{"z":-2,"a":-2,"é":-0.1234567890123456789}]}`)
	values, err := Logprobs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatal("lost token")
	}
	want := `{"token":"é","logprob":-0.1234567890123456789,"bytes":[195,169],"top_logprobs":[{"token":"é","logprob":-0.1234567890123456789,"bytes":[195,169]},{"token":"a","logprob":-2,"bytes":[97]},{"token":"z","logprob":-2,"bytes":[122]}]}`
	if string(values[0]) != want {
		t.Fatalf("wrong conversion: %s", values[0])
	}
}
func TestLogprobsRejectMalformedArrays(t *testing.T) {
	for _, raw := range []string{
		`{"tokens":["a"],"token_logprobs":[]}`,
		`{"tokens":["a"],"token_logprobs":[null]}`,
		`{"tokens":["a"],"token_logprobs":["-1"]}`,
		`{"tokens":["a"],"token_logprobs":[-1],"top_logprobs":[{},{}]}`,
		`{"content":[{"token":"a","logprob":1e999}]}`,
		`{"content":[{"token":"a","logprob":-1,"bytes":[256]}]}`,
	} {
		if values, err := Logprobs(json.RawMessage(raw)); !errors.Is(err, ErrMalformed) || values != nil {
			t.Fatalf("accepted malformed probabilities %s", raw)
		}
	}
}
func TestChatLogprobWireFields(t *testing.T) {
	values, err := Logprobs(json.RawMessage(`{"content":[{"token":"a","logprob":-1,"bytes":[97],"provider_only":true,"top_logprobs":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || string(values[0]) != `{"token":"a","logprob":-1,"bytes":[97]}` {
		t.Fatalf("unexpected wire fields: %s", values)
	}
}
