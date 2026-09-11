package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type ToolFragment struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Delta struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	Reasoning        string         `json:"reasoning"`
	ToolCalls        []ToolFragment `json:"tool_calls"`
}
type Choice struct {
	Index        int             `json:"index"`
	Delta        Delta           `json:"delta"`
	Text         string          `json:"text"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs"`
}
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
type Chunk struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage"`
}

type RemoteError struct{ Message string }

func (e *RemoteError) Error() string { return e.Message }

type Decoder struct {
	reader *Reader
	finish string
	done   bool
}

func NewDecoder(source io.Reader, max int) (*Decoder, error) {
	reader, err := NewReader(source, max)
	if err != nil {
		return nil, err
	}
	return &Decoder{reader: reader}, nil
}
func (d *Decoder) DoneReason() string {
	if d.finish == "" {
		return "stop"
	}
	return d.finish
}
func (d *Decoder) Next() (Chunk, error) {
	if d.done {
		return Chunk{}, io.EOF
	}
	for {
		event, err := d.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				d.done = true
				if d.finish == "" {
					return Chunk{}, ErrUnexpectedEOF
				}
			}
			return Chunk{}, err
		}
		if event.Type != "" && event.Type != "message" && event.Type != "error" {
			continue
		}
		data := bytes.TrimSpace(event.Data)
		if bytes.Equal(data, []byte("[DONE]")) {
			d.done = true
			return Chunk{}, io.EOF
		}
		if len(data) == 0 || data[0] != '{' {
			return Chunk{}, ErrMalformed
		}
		var payload struct {
			Chunk
			Error json.RawMessage `json:"error"`
		}
		if err = json.Unmarshal(data, &payload); err != nil {
			return Chunk{}, ErrMalformed
		}
		if len(payload.Error) > 0 && !bytes.Equal(payload.Error, []byte("null")) {
			var remote struct {
				Message string `json:"message"`
			}
			if err = json.Unmarshal(payload.Error, &remote); err != nil || remote.Message == "" {
				remote.Message = "upstream stream error"
			}
			return Chunk{}, &RemoteError{Message: remote.Message}
		}
		choices := payload.Choices[:0]
		for _, choice := range payload.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				d.finish = *choice.FinishReason
			}
			choices = append(choices, choice)
		}
		payload.Choices = choices
		if len(choices) == 0 && payload.Usage == nil {
			continue
		}
		return payload.Chunk, nil
	}
}
