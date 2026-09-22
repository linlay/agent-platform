package chat

import "fmt"

type L1Options struct {
	KeepRecent        int
	PreserveReasoning bool
}

// L1Projection keeps the input's indexing so callers can update pinned ranges.
// A nil entry is a removed message. Category rules never rewrite payload text.
type L1Projection struct {
	Messages                                  []map[string]any
	ToolsCleared, ReasoningCleared, ToolsKept int
}

func ProjectL1(messages []map[string]any, keepRecent, pinnedStart, pinnedEnd int, preserveReasoning bool) L1Projection {
	type round struct {
		indices              []int
		calls                map[string]bool
		results              map[string]bool
		protected, removable bool
	}
	var rounds []*round
	keyed := map[string]*round{}
	pending := map[string]*round{}
	var current *round
	result := L1Projection{Messages: make([]map[string]any, len(messages))}
	for i, message := range messages {
		result.Messages[i] = cloneMessageMap(message)
		scope := stringFromAny(message["runId"]) + "\x00" + stringFromAny(message["agentKey"])
		role := stringFromAny(message["role"])
		switch role {
		case "assistant":
			key := stringFromAny(message["_compactRound"])
			current = nil
			if key != "" {
				current = keyed[key]
			}
			if current == nil {
				current = &round{calls: map[string]bool{}, results: map[string]bool{}, removable: true}
				rounds = append(rounds, current)
				if key != "" {
					keyed[key] = current
				}
			}
			current.indices = append(current.indices, i)
			for _, call := range anyMessageSlice(message["tool_calls"]) {
				id := stringFromAny(call["id"])
				fn, _ := call["function"].(map[string]any)
				if id == "" || !ToolCompactable(stringFromAny(fn["name"])) {
					current.removable = false
				}
				current.calls[scope+"\x00"+id] = true
				pending[scope+"\x00"+id] = current
			}
		case "tool":
			key := scope + "\x00" + compactToolResultID(message)
			current = pending[key]
			if current != nil {
				current.indices = append(current.indices, i)
				current.results[key] = true
				delete(pending, key)
			}
		default:
			current = nil
		}
		if current != nil && i >= pinnedStart && i < pinnedEnd {
			current.protected = true
		}
	}
	complete := make([]*round, 0, len(rounds))
	for _, r := range rounds {
		for id := range r.calls {
			if !r.results[id] {
				r.protected = true
			}
		}
		if len(r.calls) == len(r.results) {
			complete = append(complete, r)
		}
	}
	for i := max(0, len(complete)-keepRecent); i < len(complete); i++ {
		complete[i].protected = true
	}
	// A legacy snapshot may hold many rounds in one physical line. Its category
	// policy cannot express different decisions for those rounds: protect it whole.
	protectedSources := map[string]bool{}
	for _, r := range rounds {
		if r.protected {
			for _, i := range r.indices {
				if source := stringFromAny(messages[i]["_compactSource"]); source != "" {
					protectedSources[source] = true
				}
			}
		}
	}
	for _, r := range rounds {
		for _, i := range r.indices {
			if protectedSources[stringFromAny(messages[i]["_compactSource"])] {
				r.protected = true
			}
		}
		if r.protected {
			result.ToolsKept += len(r.calls)
			continue
		}
		removeTools := r.removable && len(r.calls) > 0
		if removeTools {
			result.ToolsCleared += len(r.calls)
		} else {
			result.ToolsKept += len(r.calls)
		}
		for _, i := range r.indices {
			if !removeTools && (preserveReasoning || !categoryPresent(messages[i], "reasoning")) {
				continue
			}
			keep := map[string]bool{"content": true, "tool": !removeTools, "reasoning": preserveReasoning}
			if !preserveReasoning && anyCompactText(messages[i]["reasoning_content"]) != "" {
				result.ReasoningCleared++
			}
			result.Messages[i] = projectCompactMessage(messages[i], keep)
		}
	}
	return result
}

func compactLineSource(line map[string]any) string {
	switch stringFromAny(line["_type"]) {
	case StepLineTypeReact, StepLineTypeReactTool:
		return fmt.Sprintf("%s:%s:%v", stringFromAny(line["runId"]), stringFromAny(line["taskSubAgentKey"]), line["seq"])
	case CompactCheckpointLineType, RunCompactCheckpointLineType:
		return "checkpoint:" + stringFromAny(line["compactId"])
	}
	return ""
}

func categoryPresent(message map[string]any, category string) bool {
	switch category {
	case "tool":
		return stringFromAny(message["role"]) == "tool" || len(anyMessageSlice(message["tool_calls"])) > 0
	case "content":
		return stringFromAny(message["role"]) != "tool" && hasCompactContent(message["content"])
	case "reasoning":
		return anyCompactText(message["reasoning_content"]) != ""
	}
	return false
}

// Build one category mask per original physical line. A category can only be
// removed when every message of that category in the line is removable.
func buildL1Policies(records []jsonLineRecord, keepRecent int, preserveReasoning bool) (map[int]map[string]bool, L1Projection) {
	var messages []map[string]any
	var locations []int
	for i, record := range records {
		line := record.Value
		if lineIsCompacted(line) || stringFromAny(line["taskSubAgentKey"]) != "" {
			continue
		}
		kind := stringFromAny(line["_type"])
		if kind == RunCompactCheckpointLineType || (kind == CompactCheckpointLineType && len(anyMessageSlice(line["messages"])) > 0) {
			messages = nil
			locations = nil
		} else if kind != StepLineTypeReact && kind != StepLineTypeReactTool {
			continue
		}
		source := compactLineSource(line)
		for _, original := range anyMessageSlice(projectCompactLine(line)["messages"]) {
			m := cloneMessageMap(original)
			if stringFromAny(m["runId"]) == "" {
				m["runId"] = line["runId"]
			}
			m["_compactSource"] = source
			if kind == StepLineTypeReact || kind == StepLineTypeReactTool {
				m["_compactRound"] = source
			}
			messages = append(messages, m)
			locations = append(locations, i)
		}
	}
	projection := ProjectL1(messages, keepRecent, -1, -1, preserveReasoning)
	keeps := map[int]map[string]bool{}
	changed := map[int]bool{}
	for i, old := range messages {
		li := locations[i]
		if keeps[li] == nil {
			keeps[li] = map[string]bool{}
		}
		for _, category := range []string{"content", "reasoning", "tool"} {
			if !categoryPresent(old, category) {
				continue
			}
			if categoryPresent(projection.Messages[i], category) {
				keeps[li][category] = true
			} else {
				changed[li] = true
			}
		}
	}
	for li := range keeps {
		if !changed[li] {
			delete(keeps, li)
		}
	}
	// If a legacy replacement checkpoint becomes wholly excluded, its older
	// physical source must stay excluded too, otherwise replay resurrects it.
	for li, keep := range keeps {
		kind := stringFromAny(records[li].Value["_type"])
		if len(keep) == 0 && (kind == RunCompactCheckpointLineType || kind == CompactCheckpointLineType) {
			for i := 0; i < li; i++ {
				line := records[i].Value
				if len(rawMessagesFromJSONLLines([]map[string]any{line})) > 0 {
					keeps[i] = map[string]bool{}
				}
			}
		}
	}
	return keeps, projection
}
