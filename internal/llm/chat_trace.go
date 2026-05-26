package llm

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/observability"
)

type llmChatTrace struct {
	mu            sync.Mutex
	enabled       bool
	maskSensitive bool
	path          string
	payload       map[string]any
	startedAt     time.Time
	completed     bool
}

func (s *llmRunStream) newChatTrace(runSeq int, prepared preparedProviderRequest, effectiveToolChoice string) *llmChatTrace {
	cfg := s.engine.cfg.Logging.LLMInteraction
	if !cfg.RecordEnabled || strings.TrimSpace(cfg.RecordDir) == "" {
		return nil
	}
	payload := map[string]any{
		"runId":       strings.TrimSpace(s.session.RunID),
		"runSeq":      runSeq,
		"chatId":      strings.TrimSpace(s.session.ChatID),
		"requestId":   strings.TrimSpace(s.session.RequestID),
		"agentKey":    strings.TrimSpace(s.session.AgentKey),
		"stage":       strings.TrimSpace(s.promptBuildOptions.Stage),
		"modelKey":    strings.TrimSpace(s.model.Key),
		"modelId":     strings.TrimSpace(s.model.ModelID),
		"providerKey": strings.TrimSpace(s.provider.Key),
		"endpoint":    strings.TrimSpace(prepared.Endpoint),
		"protocol":    strings.TrimSpace(s.model.Protocol),
		"toolChoice":  strings.TrimSpace(effectiveToolChoice),
		"request":     prepared.RequestBody,
		"tools":       traceToolsFromRequest(prepared.RequestBody),
		"toolCalls":   []any{},
		"toolResults": []any{},
		"response":    map[string]any{},
	}
	if strings.TrimSpace(s.model.Protocol) == "" {
		payload["protocol"] = "OPENAI"
	}
	return &llmChatTrace{
		enabled:       true,
		maskSensitive: cfg.MaskSensitive,
		path:          filepath.Join(cfg.RecordDir, "run_"+strconvItoa(runSeq)+".json"),
		payload:       payload,
	}
}

func traceToolsFromRequest(request map[string]any) any {
	if request == nil {
		return nil
	}
	if tools, ok := request["tools"]; ok {
		return tools
	}
	return nil
}

func traceToolMessageContent(msg openAIMessage) string {
	switch typed := msg.Content.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func effectiveTraceToolChoice(toolChoice string, toolSpecs []openAIToolSpec) string {
	effective := strings.TrimSpace(strings.ToLower(toolChoice))
	if effective == "" {
		effective = "auto"
	}
	if len(toolSpecs) == 0 || effective == "none" {
		return ""
	}
	return effective
}

func (t *llmChatTrace) markSent(at time.Time) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startedAt = at
	t.payload["sentAt"] = at.Format(time.RFC3339Nano)
	t.writeLocked()
}

func (t *llmChatTrace) markResponseStarted(at time.Time) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.payload["responseStartedAt"] = at.Format(time.RFC3339Nano)
	t.writeLocked()
}

func (t *llmChatTrace) appendToolCalls(toolCalls []openAIToolCall) {
	if t == nil || !t.enabled || len(toolCalls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	items, _ := t.payload["toolCalls"].([]any)
	for _, call := range toolCalls {
		items = append(items, map[string]any{
			"toolId":       strings.TrimSpace(call.ID),
			"toolName":     strings.TrimSpace(call.Function.Name),
			"rawArguments": call.Function.Arguments,
		})
	}
	t.payload["toolCalls"] = items
	t.writeLocked()
}

func (t *llmChatTrace) appendToolResult(invocation *preparedToolInvocation, content string, result any) {
	if t == nil || !t.enabled || invocation == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	items, _ := t.payload["toolResults"].([]any)
	items = append(items, map[string]any{
		"toolId":   strings.TrimSpace(invocation.toolID),
		"toolName": strings.TrimSpace(invocation.toolName),
		"content":  content,
		"result":   result,
	})
	t.payload["toolResults"] = items
	t.writeLocked()
}

func (t *llmChatTrace) completeOK(content string, finishReason string, usage *openAIUsage) {
	t.complete("ok", "", content, finishReason, usage)
}

func (t *llmChatTrace) completeError(err error) {
	if err == nil {
		return
	}
	t.complete("error", err.Error(), "", "", nil)
}

func (t *llmChatTrace) completeInterrupted() {
	t.complete("interrupted", "run interrupted", "", "", nil)
}

func (t *llmChatTrace) complete(status string, errText string, content string, finishReason string, usage *openAIUsage) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.completed {
		return
	}
	t.completed = true
	completedAt := time.Now()
	t.payload["completedAt"] = completedAt.Format(time.RFC3339Nano)
	if !t.startedAt.IsZero() {
		t.payload["durationMs"] = completedAt.Sub(t.startedAt).Milliseconds()
	}
	t.payload["status"] = status
	if strings.TrimSpace(errText) != "" {
		t.payload["error"] = errText
	}
	response := map[string]any{
		"content":      content,
		"finishReason": finishReason,
	}
	if usage != nil {
		response["usage"] = usage
	}
	t.payload["response"] = response
	t.writeLocked()
}

func (t *llmChatTrace) writeLocked() {
	dataPayload := t.payload
	if t.maskSensitive {
		dataPayload = maskTracePayload(dataPayload)
	}
	data, err := json.MarshalIndent(dataPayload, "", "  ")
	if err != nil {
		log.Printf("[llm][trace][warning] marshal failed path=%s err=%v", t.path, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		log.Printf("[llm][trace][warning] mkdir failed path=%s err=%v", t.path, err)
		return
	}
	if err := os.WriteFile(t.path, append(data, '\n'), 0o644); err != nil {
		log.Printf("[llm][trace][warning] write failed path=%s err=%v", t.path, err)
	}
}

func maskTracePayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		switch key {
		case "request", "tools", "toolCalls", "toolResults", "response":
			out[key] = maskTraceValue(value)
		default:
			out[key] = value
		}
	}
	return out
}

func maskTraceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = maskTraceValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, maskTraceValue(item))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, maskTraceValue(item))
		}
		return out
	case string:
		sanitized := observability.SanitizeLog(typed)
		if sanitized == "" {
			return ""
		}
		return "[masked chars=" + strconvItoa(len(sanitized)) + "]"
	default:
		return typed
	}
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
