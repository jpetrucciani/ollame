package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// InputTextBytes counts the final translated text, including alias system text,
// reasoning history and tool arguments. Image payloads, schemas, IDs, and JSON
// framing are excluded from this text-only approximation.
func InputTextBytes(body []byte) (int, error) {
	var request struct {
		Prompt   string          `json:"prompt"`
		Suffix   string          `json:"suffix"`
		Messages []OpenAIMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return 0, fmt.Errorf("%w: cannot estimate translated input", ErrInvalidRequest)
	}
	total := len(request.Prompt) + len(request.Suffix)
	for _, message := range request.Messages {
		total += len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.Function.Arguments)
		}
		raw := bytes.TrimSpace(message.Content)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if raw[0] == '"' {
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return 0, err
			}
			total += len(text)
		} else {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &parts); err != nil {
				return 0, err
			}
			for _, part := range parts {
				if part.Type == "text" {
					total += len(part.Text)
				}
			}
		}
	}
	return total, nil
}
