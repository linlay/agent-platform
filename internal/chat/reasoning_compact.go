package chat

// reasoningReplacements selects only the effective root context. An unfinished
// tool interaction protects its run/actor's reasoning, including split reasoning
// and tool-call messages. This deliberately errs on the side of preserving the
// provider's current tool-continuation protocol.
func reasoningReplacements(records []jsonLineRecord, limit int) []toolCompactReplacement {
	type entry struct {
		line, message int
		value         map[string]any
		scope         string
	}
	var entries []entry
	for li, record := range records {
		line := record.Value
		if lineIsCompacted(line) || stringFromAny(line["taskSubAgentKey"]) != "" {
			continue
		}
		switch stringFromAny(line["_type"]) {
		case RunCompactCheckpointLineType, CompactCheckpointLineType:
			if len(anyMessageSlice(line["messages"])) == 0 {
				continue
			}
			entries = nil
		case StepLineTypeReact, StepLineTypeReactTool:
		default:
			continue
		}
		for mi, message := range anyMessageSlice(line["messages"]) {
			run := stringFromAny(message["runId"])
			if run == "" {
				run = stringFromAny(line["runId"])
			}
			scope := run + "\x00" + stringFromAny(message["agentKey"]) + "\x00" + stringFromAny(message["taskSubAgentKey"])
			entries = append(entries, entry{li, mi, message, scope})
		}
	}
	pending := map[string]map[string]bool{}
	for _, e := range entries {
		if pending[e.scope] == nil {
			pending[e.scope] = map[string]bool{}
		}
		if stringFromAny(e.value["role"]) == "assistant" {
			for _, call := range anyMessageSlice(e.value["tool_calls"]) {
				pending[e.scope][stringFromAny(call["id"])] = true
			}
		} else if stringFromAny(e.value["role"]) == "tool" {
			delete(pending[e.scope], compactToolResultID(e.value))
		}
	}
	total := 0
	var selected []toolCompactReplacement
	for _, e := range entries {
		if stringFromAny(e.value["role"]) != "assistant" {
			continue
		}
		text := anyCompactText(e.value["reasoning_content"])
		total += EstimateTextTokens(text)
		if text == "" || len(pending[e.scope]) != 0 || e.value["_compactPinned"] == true {
			continue
		}
		selected = append(selected, toolCompactReplacement{LineIndex: e.line, MessageIndex: e.message, AssistantLineIndex: e.line, ClearReasoning: true})
	}
	if total < limit || limit <= 0 {
		return nil
	}
	return selected
}

// CompactReasoningMessages drops reasoning only; the original maps and all
// content/tool protocol fields remain unchanged. The count is messages cleared.
func CompactReasoningMessages(messages []map[string]any, limit, pinnedStart, pinnedEnd int) ([]map[string]any, int) {
	out := make([]map[string]any, len(messages))
	items := make([]any, len(messages))
	for i, m := range messages {
		out[i] = cloneMessageMap(m)
		if i >= pinnedStart && i < pinnedEnd {
			out[i]["_compactPinned"] = true
		}
		items[i] = out[i]
	}
	replacements := reasoningReplacements([]jsonLineRecord{{Value: map[string]any{"_type": StepLineTypeReact, "messages": items}}}, limit)
	for _, r := range replacements {
		delete(out[r.MessageIndex], "reasoning_content")
	}
	for _, m := range out {
		delete(m, "_compactPinned")
	}
	return out, len(replacements)
}
