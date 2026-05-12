package chat

import (
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/stream"
)

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

func isNewFormat(lines []map[string]any) bool {
	for _, line := range lines {
		if _, ok := line["_type"]; ok {
			return true
		}
		if _, ok := line["type"]; ok {
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Step line → SSE events reconstruction
// ---------------------------------------------------------------------------

func synthesizedContextWindow(contextWindow map[string]any) map[string]any {
	cw := map[string]any{}
	if len(contextWindow) == 0 {
		return cw
	}
	if v := toIntFromKeys(contextWindow, "maxSize", "max_size"); v > 0 {
		cw["maxSize"] = v
	}
	if v := toIntFromKeys(contextWindow, "actualSize", "actual_size"); v > 0 {
		cw["actualSize"] = v
	}
	if v := toIntFromKeys(contextWindow, "estimatedSize", "estimated_size"); v > 0 {
		cw["estimatedSize"] = v
	}
	return cw
}

func cumulativeUsagePayload(cumulative map[string]int) map[string]any {
	if cumulative == nil {
		return map[string]any{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
	}
	return map[string]any{
		"promptTokens":     cumulative["promptTokens"],
		"completionTokens": cumulative["completionTokens"],
		"totalTokens":      cumulative["totalTokens"],
	}
}

func synthesizePreCallEvent(runID, chatID string, runCumulative, chatCumulative map[string]int, contextWindow map[string]any, preCallData map[string]any, ts int64, nextSeq func() int64) *stream.EventData {
	data := cloneStringAnyMap(preCallData)
	if data == nil {
		data = map[string]any{}
	}
	if cw := synthesizedContextWindow(contextWindow); len(cw) > 0 {
		data["contextWindow"] = cw
	}
	data["usage"] = map[string]any{
		"runUsage":  cumulativeUsagePayload(runCumulative),
		"chatUsage": cumulativeUsagePayload(chatCumulative),
	}
	return &stream.EventData{
		Seq:       nextSeq(),
		Type:      "debug.preCall",
		Timestamp: ts,
		Payload: map[string]any{
			"runId":  runID,
			"chatId": chatID,
			"data":   data,
		},
	}
}

func debugPreCallData(debug map[string]any, system map[string]any) map[string]any {
	if len(debug) > 0 {
		data, _ := debug["preCall"].(map[string]any)
		if len(data) > 0 {
			return cloneStringAnyMap(data)
		}
	}
	// TODO(compat-cleanup): remove system.debugPreCall fallback once old chat traces have been migrated.
	if len(system) > 0 {
		data, _ := system["debugPreCall"].(map[string]any)
		if len(data) > 0 {
			return cloneStringAnyMap(data)
		}
	}
	return nil
}

func synthesizePostCallEvent(runID, chatID string, usage map[string]any, runCumulative, chatCumulative map[string]int, contextWindow map[string]any, ts int64, nextSeq func() int64) *stream.EventData {
	llm := map[string]any{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
	if usage != nil {
		llm = map[string]any{
			"promptTokens":     toIntFromKeys(usage, "promptTokens", "prompt_tokens"),
			"completionTokens": toIntFromKeys(usage, "completionTokens", "completion_tokens"),
			"totalTokens":      toIntFromKeys(usage, "totalTokens", "total_tokens"),
		}
	}
	data := map[string]any{}
	if cw := synthesizedContextWindow(contextWindow); len(cw) > 0 {
		data["contextWindow"] = cw
	}
	data["usage"] = map[string]any{
		"llmReturnUsage": llm,
		"runUsage":       cumulativeUsagePayload(runCumulative),
		"chatUsage":      cumulativeUsagePayload(chatCumulative),
	}
	return &stream.EventData{
		Seq:       nextSeq(),
		Type:      "debug.postCall",
		Timestamp: ts,
		Payload: map[string]any{
			"runId":  runID,
			"chatId": chatID,
			"data":   data,
		},
	}
}

func toIntValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func toIntFromKeys(values map[string]any, keys ...string) int {
	if values == nil {
		return 0
	}
	for _, key := range keys {
		if v := toIntValue(values[key]); v > 0 {
			return v
		}
	}
	return 0
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
