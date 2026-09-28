// Package modelresponses contains wire types and stateless input conversion for
// the Responses API. It has no dependency on the agent loop or tool executor.
package modelresponses

import (
	"agent-platform/internal/contracts"
	"encoding/json"
	"fmt"
	"strings"
)

const Protocol = "OPENAI_RESPONSES"

type Item struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content,omitempty"`
}
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}
type Response struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output []Item `json:"output"`
	Usage  *Usage `json:"usage"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (r Response) Text() string {
	var b strings.Builder
	for _, item := range r.Output {
		for _, c := range item.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}
func (i Item) Reasoning() contracts.ReasoningPart {
	summary := i.Summary
	if len(summary) == 0 || string(summary) == "null" {
		summary = json.RawMessage("[]")
	}
	return contracts.ReasoningPart{Type: "encrypted_text", ID: i.ID, EncryptedText: i.EncryptedContent, Summary: summary}
}
func Input(messages []contracts.ModelMessage, modelKey string) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, m := range messages {
		if m.Role == "assistant" && (m.OriginModelKey == "" || m.OriginModelKey == modelKey) {
			for _, r := range m.EncryptedReasoning {
				if r.EncryptedText == "" {
					continue
				}
				summary := r.Summary
				if len(summary) == 0 {
					summary = json.RawMessage("[]")
				}
				input = append(input, map[string]any{"type": "reasoning", "id": r.ID, "summary": summary, "encrypted_content": r.EncryptedText})
			}
		}
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				return nil, fmt.Errorf("responses tool result missing call_id")
			}
			value := m.Content
			if value == nil {
				value = ""
			}
			if _, ok := value.(string); !ok {
				b, err := json.Marshal(value)
				if err != nil {
					return nil, err
				}
				value = string(b)
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": value})
			continue
		}
		if m.Content != nil {
			content, err := Content(m.Content, m.Role)
			if err != nil {
				return nil, err
			}
			if len(content) > 0 {
				input = append(input, map[string]any{"role": m.Role, "content": content})
			}
		}
		for _, call := range m.ToolCalls {
			if call.ID == "" {
				return nil, fmt.Errorf("responses function call missing call_id")
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	return input, nil
}
func Content(value any, role string) ([]any, error) {
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": textType, "text": text}}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var parts []map[string]any
	if err = json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("responses unsupported message content: %w", err)
	}
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part["type"] {
		case "text", "input_text", "output_text":
			out = append(out, map[string]any{"type": textType, "text": part["text"]})
		case "image_url":
			image := part["image_url"]
			block := map[string]any{"type": "input_image"}
			if v, ok := image.(map[string]any); ok {
				block["image_url"] = v["url"]
				if v["detail"] != nil {
					block["detail"] = v["detail"]
				}
			} else {
				block["image_url"] = image
			}
			out = append(out, block)
		case "input_image":
			out = append(out, part)
		default:
			return nil, fmt.Errorf("responses unsupported content type %v", part["type"])
		}
	}
	return out, nil
}

// Tools keeps existing schemas non-strict: Responses otherwise normalizes some
// schemas to strict mode, changing optional arguments in existing tool contracts.
func Tools(specs any) ([]any, error) {
	raw, err := json.Marshal(specs)
	if err != nil {
		return nil, err
	}
	var wrapped []struct {
		Type     string         `json:"type"`
		Function map[string]any `json:"function"`
	}
	if err = json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	var out []any
	for _, t := range wrapped {
		if t.Type != "function" {
			return nil, fmt.Errorf("responses only supports platform function tools")
		}
		f := t.Function
		if f == nil {
			return nil, fmt.Errorf("missing function definition")
		}
		f["type"] = "function"
		f["strict"] = false
		out = append(out, f)
	}
	return out, nil
}
