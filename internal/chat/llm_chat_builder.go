package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

type LLMChatBuildOptions struct {
	RunID string
	Stage string
	Seq   int
}

type LLMChat struct {
	Messages        []map[string]any
	Tools           []any
	ToolChoice      string
	Model           map[string]any
	ModelKey        string
	ReasoningEffort string
	RequestOptions  map[string]any
	SystemRef       map[string]any
	Legacy          bool
}

type llmSystemSnapshot struct {
	SystemMessage map[string]any
	Tools         []any
}

func (s *FileStore) BuildLLMChatFromJSONL(chatID string, options LLMChatBuildOptions) (LLMChat, error) {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return LLMChat{}, err
	}
	if len(lines) == 0 {
		return LLMChat{}, nil
	}
	targetIndex := findLLMChatTargetLine(lines, options)
	if targetIndex < 0 {
		return LLMChat{}, fmt.Errorf("llm chat target not found")
	}
	target := lines[targetIndex]
	prefix := lines[:targetIndex]
	systemCache := buildLLMSystemCache(prefix)

	messages := rawMessagesFromJSONLLines(prefix)
	if inputMessages := messageMapsFromAny(target["inputMessages"]); len(inputMessages) > 0 {
		messages = append(messages, inputMessages...)
	}

	systemMessage, tools, systemRef, legacy := resolveLLMChatSystem(target, systemCache)
	if len(systemMessage) > 0 {
		messages = append([]map[string]any{systemMessage}, messages...)
	}

	model := cloneMapDeep(anyMap(target["model"]))
	modelKey := strings.TrimSpace(stringValue(target["modelKey"]))
	reasoningEffort := strings.TrimSpace(stringValue(target["reasoningEffort"]))
	if modelKey == "" {
		modelKey = strings.TrimSpace(stringValue(model["key"]))
	}
	if reasoningEffort == "" {
		reasoningEffort = strings.TrimSpace(stringValue(model["reasoningEffort"]))
	}
	if len(model) == 0 && (modelKey != "" || reasoningEffort != "") {
		model = map[string]any{}
		if modelKey != "" {
			model["key"] = modelKey
		}
		if reasoningEffort != "" {
			model["reasoningEffort"] = reasoningEffort
		}
		legacy = true
	}

	toolChoice := strings.TrimSpace(stringValue(target["toolChoice"]))
	if toolChoice == "" && len(tools) > 0 {
		legacy = true
	}
	requestOptions := cloneMapDeep(anyMap(target["requestOptions"]))
	if len(requestOptions) == 0 {
		legacy = true
	}

	return LLMChat{
		Messages:        cloneMessageMaps(messages),
		Tools:           cloneAnySliceDeep(tools),
		ToolChoice:      toolChoice,
		Model:           model,
		ModelKey:        modelKey,
		ReasoningEffort: reasoningEffort,
		RequestOptions:  requestOptions,
		SystemRef:       cloneMapDeep(systemRef),
		Legacy:          legacy,
	}, nil
}

func findLLMChatTargetLine(lines []map[string]any, options LLMChatBuildOptions) int {
	targetIndex := -1
	for index, line := range lines {
		if !lineIsStep(line) {
			continue
		}
		if runID := strings.TrimSpace(options.RunID); runID != "" && strings.TrimSpace(stringValue(line["runId"])) != runID {
			continue
		}
		if stage := strings.TrimSpace(options.Stage); stage != "" && strings.TrimSpace(stringValue(line["stage"])) != stage {
			continue
		}
		if options.Seq > 0 && toIntFromKeys(line, "seq") != options.Seq {
			continue
		}
		targetIndex = index
		if options.Seq > 0 || strings.TrimSpace(options.Stage) != "" {
			return targetIndex
		}
	}
	return targetIndex
}

func lineIsStep(line map[string]any) bool {
	switch strings.TrimSpace(stringValue(line["_type"])) {
	case StepLineTypeStep, StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute:
		return true
	default:
		return false
	}
}

func buildLLMSystemCache(lines []map[string]any) map[string]llmSystemSnapshot {
	cache := map[string]llmSystemSnapshot{}
	for _, line := range lines {
		if strings.TrimSpace(stringValue(line["_type"])) != "query" {
			continue
		}
		for _, raw := range anySlice(line["systems"]) {
			system := anyMap(raw)
			cacheKey := strings.TrimSpace(stringValue(system["cacheKey"]))
			fingerprint := strings.TrimSpace(stringValue(system["fingerprint"]))
			if cacheKey == "" || fingerprint == "" {
				continue
			}
			cache[systemCacheID(cacheKey, fingerprint)] = llmSystemSnapshot{
				SystemMessage: cloneMapDeep(anyMap(system["systemMessage"])),
				Tools:         cloneAnySliceDeep(anySlice(system["tools"])),
			}
		}
	}
	return cache
}

func resolveLLMChatSystem(target map[string]any, cache map[string]llmSystemSnapshot) (map[string]any, []any, map[string]any, bool) {
	if inline := anyMap(target["system"]); len(inline) > 0 {
		systemMessage := cloneMapDeep(anyMap(inline["systemMessage"]))
		if len(systemMessage) == 0 && strings.TrimSpace(stringValue(inline["role"])) == "system" {
			systemMessage = cloneMapDeep(inline)
		}
		return systemMessage, cloneAnySliceDeep(anySlice(inline["tools"])), nil, false
	}
	systemRef := cloneMapDeep(anyMap(target["systemRef"]))
	if len(systemRef) == 0 {
		return nil, nil, nil, true
	}
	cacheKey := strings.TrimSpace(stringValue(systemRef["cacheKey"]))
	fingerprint := strings.TrimSpace(stringValue(systemRef["fingerprint"]))
	snapshot, ok := cache[systemCacheID(cacheKey, fingerprint)]
	if !ok {
		return nil, nil, systemRef, true
	}
	return cloneMapDeep(snapshot.SystemMessage), cloneAnySliceDeep(snapshot.Tools), systemRef, false
}

func systemCacheID(cacheKey string, fingerprint string) string {
	return strings.TrimSpace(cacheKey) + "\x00" + strings.TrimSpace(fingerprint)
}

func messageMapsFromAny(value any) []map[string]any {
	rawMessages := anySlice(value)
	if len(rawMessages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rawMessages))
	for _, raw := range rawMessages {
		msg := anyMap(raw)
		if len(msg) == 0 {
			continue
		}
		out = append(out, cloneMapDeep(msg))
	}
	return out
}

func anyMap(value any) map[string]any {
	out, _ := value.(map[string]any)
	return out
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func cloneMapDeep(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return cloneStringAnyMap(value)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return cloneStringAnyMap(value)
	}
	return out
}

func cloneAnySliceDeep(value []any) []any {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return append([]any(nil), value...)
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		return append([]any(nil), value...)
	}
	return out
}
