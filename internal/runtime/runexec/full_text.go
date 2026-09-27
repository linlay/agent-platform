package runexec

import (
	"encoding/json"
	"fmt"
	"strings"

	sessionbuild "agent-platform/internal/runtime/session"
	"agent-platform/internal/stream"
)

func (b *FullTextBuilder) Observe(event stream.EventData) {
	if event.String("status") == "failed" && (event.Type == "reasoning.end" || event.Type == "content.end" || event.Type == "tool.end") {
		b.DiscardModelTurn(map[string]any{"reasoningIds": []string{event.String("reasoningId")}, "toolIds": []string{event.String("toolId")}})
		return
	}
	b.Consume(event)
}
func (b *FullTextBuilder) ReasoningBuffer(id string) *strings.Builder {
	if existing := b.ReasoningBuffers[id]; existing != nil {
		return existing
	}
	next := &strings.Builder{}
	b.ReasoningBuffers[id] = next
	return next
}
func (b *FullTextBuilder) AppendPart(title string, body string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return
	}
	if body == "" {
		b.AppendLine(title)
		return
	}
	if title == "" {
		b.AppendLine(body)
		return
	}
	b.AppendLine(title + "\n" + body)
}
func (b *FullTextBuilder) Text(content string) string {
	if b == nil {
		return strings.TrimSpace(content)
	}
	parts := make([]string, 0, len(b.Parts)+1)
	for _, part := range b.Parts {
		parts = append(parts, part.Text)
	}
	if answer := strings.TrimSpace(content); answer != "" {
		parts = append(parts, "Answer\n"+answer)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
func NewFullTextBuilder() *FullTextBuilder {
	return &FullTextBuilder{
		ReasoningBuffers:  map[string]*strings.Builder{},
		ReasoningRecorded: map[string]bool{},
		ToolArgsBuffers:   map[string]*strings.Builder{},
		ToolNames:         map[string]string{},
		ToolRecorded:      map[string]bool{},
	}
}

type fullTextPart struct {
	Kind string
	Id   string
	Text string
}

func (b *FullTextBuilder) AppendOnce(seen map[string]bool, kind string, key string, title string, body string) {
	if seen[key] {
		return
	}
	seen[key] = true
	b.AppendModelPart(kind, key, title, body)
}
func (b *FullTextBuilder) ToolArgsBuffer(id string) *strings.Builder {
	if existing := b.ToolArgsBuffers[id]; existing != nil {
		return existing
	}
	next := &strings.Builder{}
	b.ToolArgsBuffers[id] = next
	return next
}
func (b *FullTextBuilder) DiscardModelTurn(recovery any) {
	if b == nil {
		return
	}
	payload, _ := recovery.(map[string]any)
	reasoningIDs := StringSetFromAny(payload["reasoningIds"])
	toolIDs := StringSetFromAny(payload["toolIds"])
	for id := range reasoningIDs {
		delete(b.ReasoningBuffers, id)
		delete(b.ReasoningRecorded, id)
	}
	for id := range toolIDs {
		delete(b.ToolArgsBuffers, id)
		delete(b.ToolNames, id)
		delete(b.ToolRecorded, id)
	}
	if len(reasoningIDs)+len(toolIDs) == 0 {
		return
	}
	filtered := b.Parts[:0]
	for _, part := range b.Parts {
		switch part.Kind {
		case "reasoning":
			if reasoningIDs[part.Id] {
				continue
			}
		case "tool":
			if toolIDs[part.Id] {
				continue
			}
		}
		filtered = append(filtered, part)
	}
	b.Parts = filtered
}
func IsDiscardIncompleteModelTurnRecovery(value any) bool {
	recovery, _ := value.(map[string]any)
	return strings.TrimSpace(sessionbuild.AnyString(recovery["action"])) == "discard_incomplete_model_turn"
}

type FullTextBuilder struct {
	Parts             []fullTextPart
	ReasoningBuffers  map[string]*strings.Builder
	ReasoningRecorded map[string]bool
	ToolArgsBuffers   map[string]*strings.Builder
	ToolNames         map[string]string
	ToolRecorded      map[string]bool
}

func FormatFullTextValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		data, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(typed))
		}
		return strings.TrimSpace(string(data))
	}
}
func FirstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func (b *FullTextBuilder) AppendModelPart(kind string, id string, title string, body string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return
	}
	text := title
	if text == "" {
		text = body
	} else if body != "" {
		text += "\n" + body
	}
	b.Parts = append(b.Parts, fullTextPart{Kind: kind, Id: id, Text: text})
}
func StringSetFromAny(value any) map[string]bool {
	result := map[string]bool{}
	switch values := value.(type) {
	case []string:
		for _, item := range values {
			if item = strings.TrimSpace(item); item != "" {
				result[item] = true
			}
		}
	case []any:
		for _, raw := range values {
			if item := strings.TrimSpace(sessionbuild.AnyString(raw)); item != "" {
				result[item] = true
			}
		}
	}
	return result
}
func (b *FullTextBuilder) Consume(event stream.EventData) {
	if b == nil {
		return
	}
	if event.Type == "tool.result" && event.Value("internalOnly") == true {
		return
	}
	switch event.Type {
	case "reasoning.delta":
		id := FirstNonBlankString(event.String("reasoningId"), "reasoning")
		b.ReasoningBuffer(id).WriteString(event.String("delta"))
	case "reasoning.end", "reasoning.snapshot":
		id := FirstNonBlankString(event.String("reasoningId"), "reasoning")
		text := strings.TrimSpace(event.String("text"))
		if text == "" {
			text = strings.TrimSpace(b.ReasoningBuffer(id).String())
		}
		b.AppendOnce(b.ReasoningRecorded, "reasoning", id, "Reasoning", text)
	case "tool.start":
		id := FirstNonBlankString(event.String("toolId"), "tool")
		b.ToolNames[id] = FirstNonBlankString(event.String("toolName"), event.String("toolLabel"), id)
	case "tool.args":
		id := FirstNonBlankString(event.String("toolId"), "tool")
		b.ToolArgsBuffer(id).WriteString(event.String("delta"))
	case "tool.snapshot":
		id := FirstNonBlankString(event.String("toolId"), "tool")
		name := FirstNonBlankString(event.String("toolName"), b.ToolNames[id], id)
		args := strings.TrimSpace(event.String("arguments"))
		if args == "" {
			args = strings.TrimSpace(b.ToolArgsBuffer(id).String())
		}
		b.AppendOnce(b.ToolRecorded, "tool", id, "Tool: "+name, FormatFullTextValue(args))
	case "tool.result":
		name := FirstNonBlankString(event.String("toolName"), event.String("toolId"), "tool")
		b.AppendPart("Tool result: "+name, FormatFullTextValue(event.Value("result")))
	case "planning.snapshot":
		b.AppendPart("Plan", FormatFullTextValue(event.Value("text")))
	case "planning.start":
		b.AppendLine("Planning started")
	case "planning.end":
		b.AppendLine("Planning finished")
	case "task.start":
		name := FirstNonBlankString(event.String("taskName"), event.String("taskId"), "task")
		detail := strings.TrimSpace(event.String("description"))
		if detail != "" {
			name += ": " + detail
		}
		b.AppendLine("Task started: " + name)
	case "task.complete":
		name := FirstNonBlankString(event.String("taskName"), event.String("taskId"), "task")
		b.AppendLine("Task completed: " + name)
	case "run.error":
		b.AppendPart("Run error", FormatFullTextValue(event.Value("error")))
	case "run.cancel":
		b.AppendLine("Run canceled")
	}
}
func (b *FullTextBuilder) AppendLine(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.Parts = append(b.Parts, fullTextPart{Text: text})
}
