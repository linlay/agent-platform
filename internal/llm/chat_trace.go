package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/observability"
)

type llmChatTrace struct {
	mu            sync.Mutex
	enabled       bool
	maskSensitive bool
	path          string
	relativeFile  string
	runSeq        int
	payload       map[string]any
	startedAt     time.Time
	status        string
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
		"toolCalls":   []any{},
		"toolResults": []any{},
		"response":    map[string]any{},
	}
	if injectedPrompt := buildInjectedPromptPayload(s.session, s.req, s.promptBuildOptions, s.messages); injectedPrompt != nil {
		payload["injectedPrompt"] = injectedPrompt
	}
	if strings.TrimSpace(s.model.Protocol) == "" {
		payload["protocol"] = "OPENAI"
	}
	relativeFile := traceRelativeFile(s.session.RunID, runSeq)
	return &llmChatTrace{
		enabled:       true,
		maskSensitive: cfg.MaskSensitive,
		path:          filepath.Join(cfg.RecordDir, filepath.FromSlash(relativeFile)),
		relativeFile:  relativeFile,
		runSeq:        runSeq,
		payload:       payload,
	}
}

func traceRelativeFile(runID string, runSeq int) string {
	return filepath.ToSlash(filepath.Join("llm", traceFileName(runID, runSeq)))
}

func traceFileName(runID string, runSeq int) string {
	if runSeq < 1 {
		runSeq = 1
	}
	if runSeq > 999 {
		runSeq = 999
	}
	return fmt.Sprintf("%s_%03d.json", safeTraceRunID(runID), runSeq)
}

func safeTraceRunID(runID string) string {
	return safeTracePathSegment(runID)
}

func safeTraceChatID(chatID string) string {
	return safeTracePathSegment(chatID)
}

func safeTracePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		string(filepath.Separator), "_",
	)
	safe := strings.TrimSpace(replacer.Replace(value))
	if safe == "" || safe == "." || safe == ".." {
		return "unknown"
	}
	return safe
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

func (t *llmChatTrace) runSeqValue() int {
	if t == nil || !t.enabled {
		return 0
	}
	return t.runSeq
}

func (t *llmChatTrace) relativeFileValue() string {
	if t == nil || !t.enabled {
		return ""
	}
	return strings.TrimSpace(t.relativeFile)
}

func (t *llmChatTrace) resourceURL() string {
	relativeFile := t.relativeFileValue()
	if relativeFile == "" {
		return ""
	}
	return "/api/chat/llm-trace?file=" + url.QueryEscape(filepath.ToSlash(relativeFile))
}

func (t *llmChatTrace) statusValue() string {
	if t == nil || !t.enabled {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.status)
}

func (t *llmChatTrace) payloadString(key string) string {
	if t == nil || !t.enabled {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	value, _ := t.payload[key].(string)
	return strings.TrimSpace(value)
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

func (t *llmChatTrace) completeOK(content string, reasoningContent string, toolCalls []openAIToolCall, finishReason string, usage *openAIUsage) {
	t.complete("ok", "", content, reasoningContent, toolCalls, finishReason, usage, nil)
}

func (t *llmChatTrace) completeError(err error) {
	if err == nil {
		return
	}
	t.complete("error", err.Error(), "", "", nil, "", nil, nil)
}

func (t *llmChatTrace) completeInterrupted(info contracts.InterruptInfo) {
	t.complete("interrupted", "run interrupted", "", "", nil, "", nil, &info)
}

func (t *llmChatTrace) complete(status string, errText string, content string, reasoningContent string, toolCalls []openAIToolCall, finishReason string, usage *openAIUsage, interruptInfo *contracts.InterruptInfo) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.completed {
		return
	}
	t.completed = true
	t.status = strings.TrimSpace(status)
	completedAt := time.Now()
	t.payload["completedAt"] = completedAt.Format(time.RFC3339Nano)
	if !t.startedAt.IsZero() {
		t.payload["durationMs"] = completedAt.Sub(t.startedAt).Milliseconds()
	}
	t.payload["status"] = status
	if strings.TrimSpace(errText) != "" {
		t.payload["error"] = errText
	}
	if interruptInfo != nil {
		t.payload["interrupt"] = traceInterruptInfo(*interruptInfo)
	}
	response := map[string]any{
		"content":      content,
		"finishReason": finishReason,
	}
	if reasoningContent != "" {
		response["reasoning_content"] = reasoningContent
	}
	if len(toolCalls) > 0 {
		response["tool_calls"] = traceResponseToolCalls(toolCalls)
	}
	if usage != nil {
		response["usage"] = usage
	}
	t.payload["response"] = response
	t.writeLocked()
}

func traceInterruptInfo(info contracts.InterruptInfo) map[string]any {
	info = contracts.NormalizeInterruptInfo(info)
	out := map[string]any{
		"source":        info.Source,
		"reason":        info.Reason,
		"detail":        info.Detail,
		"interruptedAt": info.InterruptedAt.Format(time.RFC3339Nano),
	}
	if info.RequestID != "" {
		out["requestId"] = info.RequestID
	}
	if info.ChatID != "" {
		out["chatId"] = info.ChatID
	}
	return out
}

func traceResponseToolCalls(toolCalls []openAIToolCall) []any {
	out := make([]any, 0, len(toolCalls))
	for _, call := range toolCalls {
		out = append(out, map[string]any{
			"id":   strings.TrimSpace(call.ID),
			"type": strings.TrimSpace(call.Type),
			"function": map[string]any{
				"name":      strings.TrimSpace(call.Function.Name),
				"arguments": call.Function.Arguments,
			},
		})
	}
	return out
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
		case "request", "toolCalls", "toolResults", "response", "injectedPrompt":
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
