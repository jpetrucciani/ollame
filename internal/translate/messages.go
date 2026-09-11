package translate

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Thinking   string     `json:"thinking,omitempty"`
	Images     []string   `json:"images,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Index     int             `json:"index"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}
type OpenAIMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type MessageResult struct {
	Messages    []OpenAIMessage
	Dropped     []Dropped
	Synthesized int
}

func Messages(input []Message, entry catalog.Entry, cfg config.Config) (MessageResult, error) {
	result := MessageResult{Messages: []OpenAIMessage{}}
	imageCount := 0
	sortMessages := cfg.Compat.SortModelMessages
	if entry.Behavior.SortModelMessages != nil {
		sortMessages = *entry.Behavior.SortModelMessages
	}
	var systemParts []string
	hasSystem := false
	for _, message := range input {
		if strings.EqualFold(message.Role, "system") {
			hasSystem = true
			if sortMessages {
				systemParts = append(systemParts, message.Content)
			}
		}
		if len(message.Images) > cfg.Limits.MaxImages-imageCount {
			return result, fmt.Errorf("%w: too many images", ErrInvalidRequest)
		}
		imageCount += len(message.Images)
	}
	if sortMessages && hasSystem {
		message, err := textMessage("system", strings.Join(systemParts, "\n\n"))
		if err != nil {
			return result, err
		}
		result.Messages = append(result.Messages, message)
	}
	if !hasSystem && entry.System != "" {
		message, err := textMessage("system", entry.System)
		if err != nil {
			return result, err
		}
		result.Messages = append(result.Messages, message)
	}
	pending := []OpenAIToolCall{}
	consumed := map[string]string{}
	allIDs := map[string]bool{}
	flush := func() error {
		if cfg.Compat.MissingToolResult == "synthesize" {
			for _, call := range pending {
				if _, done := consumed[call.ID]; done {
					continue
				}
				message, err := textMessage("tool", "[no result provided]")
				if err != nil {
					return err
				}
				message.ToolCallID = call.ID
				result.Messages = append(result.Messages, message)
				result.Synthesized++
			}
		}
		pending = nil
		return nil
	}
	for index, message := range input {
		role := strings.ToLower(message.Role)
		// Remove system messages before matching tools so they cannot split a
		// call from its result. Keep original indexes for generated call IDs.
		if sortMessages && role == "system" {
			if len(message.Images) > 0 {
				result.Dropped = append(result.Dropped, Dropped{Field: "images_non_user", Name: "images"})
			}
			continue
		}
		if role != "tool" {
			if err := flush(); err != nil {
				return result, err
			}
		}
		mapped, err := textMessage(role, message.Content)
		if err != nil {
			return result, err
		}
		if role != "user" && len(message.Images) > 0 {
			result.Dropped = append(result.Dropped, Dropped{Field: "images_non_user", Name: "images"})
		}
		switch role {
		case "user":
			if len(message.Images) > 0 {
				if cfg.Compat.StrictCapabilities && entry.Knowledge["vision"] == catalog.No {
					return result, fmt.Errorf("%w: model does not support images", ErrInvalidRequest)
				}
				parts := []imagePart{}
				if message.Content != "" {
					parts = append(parts, imagePart{Type: "text", Text: message.Content})
				}
				for _, image := range message.Images {
					url, err := imageURL(image, cfg.Compat.DefaultImageMIME)
					if err != nil {
						return result, err
					}
					parts = append(parts, imagePart{Type: "image_url", ImageURL: &imageLocation{URL: url}})
				}
				mapped.Content, err = json.Marshal(parts)
				if err != nil {
					return result, err
				}
			}
		case "assistant":
			if cfg.Compat.HistoryThinking == "reasoning_content" {
				mapped.ReasoningContent = message.Thinking
			}
			for callIndex, call := range message.ToolCalls {
				id := call.ID
				if id == "" {
					id = toolID(index, callIndex, call.Function.Name)
				}
				if allIDs[id] {
					return result, fmt.Errorf("%w: duplicate assistant tool-call ID", ErrInvalidRequest)
				}
				allIDs[id] = true
				raw := bytes.TrimSpace(call.Function.Arguments)
				if len(raw) == 0 {
					raw = []byte("{}")
				}
				if raw[0] != '{' || !json.Valid(raw) {
					return result, fmt.Errorf("%w: tool-call arguments must be an object", ErrInvalidRequest)
				}
				var compact bytes.Buffer
				if err = json.Compact(&compact, raw); err != nil {
					return result, fmt.Errorf("%w: invalid tool-call arguments", ErrInvalidRequest)
				}
				outgoing := OpenAIToolCall{ID: id, Type: "function"}
				outgoing.Function.Name = call.Function.Name
				outgoing.Function.Arguments = compact.String()
				mapped.ToolCalls = append(mapped.ToolCalls, outgoing)
			}
			if len(mapped.ToolCalls) > 0 && message.Content == "" {
				mapped.Content = json.RawMessage("null")
			}
			pending = mapped.ToolCalls
		case "tool":
			if previous, ok := consumed[message.ToolCallID]; message.ToolCallID != "" && ok && previous == message.Content {
				result.Dropped = append(result.Dropped, Dropped{Field: "duplicate_tool_result", Name: "tool_call_id"})
				continue
			}
			chosen := -1
			for i, call := range pending {
				if _, used := consumed[call.ID]; !used && message.ToolCallID != "" && call.ID == message.ToolCallID {
					chosen = i
					break
				}
			}
			if chosen < 0 && message.ToolName != "" {
				for i, call := range pending {
					if _, used := consumed[call.ID]; !used && call.Function.Name == message.ToolName {
						chosen = i
						break
					}
				}
			}
			if chosen < 0 {
				for i, call := range pending {
					if _, used := consumed[call.ID]; !used {
						chosen = i
						break
					}
				}
			}
			if chosen < 0 {
				if cfg.Compat.OrphanToolResult == "error" {
					return result, fmt.Errorf("%w: tool message has no matching assistant tool call", ErrInvalidRequest)
				}
				name := message.ToolName
				if name == "" {
					name = "unknown"
				}
				mapped, err = textMessage("user", "[tool result: "+name+"]\n"+message.Content)
				if err != nil {
					return result, err
				}
			} else {
				mapped.ToolCallID = pending[chosen].ID
				consumed[mapped.ToolCallID] = message.Content
			}
		}
		result.Messages = append(result.Messages, mapped)
	}
	if err := flush(); err != nil {
		return result, err
	}
	return result, nil
}
func textMessage(role, content string) (OpenAIMessage, error) {
	raw, err := json.Marshal(content)
	return OpenAIMessage{Role: role, Content: raw}, err
}
func toolID(message, call int, name string) string {
	var positions [16]byte
	binary.BigEndian.PutUint64(positions[:8], uint64(message))
	binary.BigEndian.PutUint64(positions[8:], uint64(call))
	hash := sha256.New()
	hash.Write(positions[:])
	hash.Write([]byte(name))
	return "call_" + hex.EncodeToString(hash.Sum(nil)[:12])
}

type imageLocation struct {
	URL string `json:"url"`
}
type imagePart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL *imageLocation `json:"image_url,omitempty"`
}

func imageURL(value, fallback string) (string, error) {
	if strings.HasPrefix(value, "data:") {
		return value, nil
	}
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(value))
	header := make([]byte, 512)
	n, err := io.ReadFull(reader, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	mime := http.DetectContentType(header[:n])
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
	default:
		mime = fallback
	}
	return "data:" + mime + ";base64," + value, nil
}
