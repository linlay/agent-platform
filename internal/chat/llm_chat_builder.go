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
}

type llmSystemSnapshot struct {
	AgentKey       string
	CacheKey       string
	Fingerprint    string
	SystemMessage  map[string]any
	Tools          []any
	Model          map[string]any
	ToolChoice     string
	RequestOptions map[string]any
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
	systemCache := buildLLMSystemCache(lines[:targetIndex+1])

	messages := llmRequestMessagesFromJSONLLines(prefix)
	if inputMessages := messageMapsFromAny(target["inputMessages"]); len(inputMessages) > 0 {
		messages = append(messages, inputMessages...)
	}

	systemMessage, tools, systemRef, profile, err := resolveLLMChatSystem(target, systemCache)
	if err != nil {
		return LLMChat{}, err
	}
	if len(systemMessage) > 0 {
		messages = append([]map[string]any{systemMessage}, messages...)
	}

	model := cloneMapDeep(profile.Model)
	modelKey := strings.TrimSpace(stringValue(model["key"]))
	if modelKey == "" {
		return LLMChat{}, fmt.Errorf("llm chat system snapshot missing model key")
	}
	reasoningEffort := strings.TrimSpace(stringValue(model["reasoningEffort"]))
	toolChoice := strings.TrimSpace(profile.ToolChoice)
	requestOptions := cloneMapDeep(profile.RequestOptions)

	return LLMChat{
		Messages:        cloneMessageMaps(messages),
		Tools:           cloneAnySliceDeep(tools),
		ToolChoice:      toolChoice,
		Model:           model,
		ModelKey:        modelKey,
		ReasoningEffort: reasoningEffort,
		RequestOptions:  requestOptions,
		SystemRef:       cloneMapDeep(systemRef),
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

func llmRequestMessagesFromJSONLLines(lines []map[string]any) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}
	var messages []map[string]any
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		if steerMessage := llmRequestSteerMessageFromLine(line); len(steerMessage) > 0 {
			messages = append(messages, steerMessage)
			continue
		}
		messages = append(messages, rawMessagesFromJSONLLines([]map[string]any{line})...)
	}
	return messages
}

func llmRequestSteerMessageFromLine(line map[string]any) map[string]any {
	if strings.TrimSpace(stringValue(line["_type"])) != "steer" {
		return nil
	}
	event := anyMap(line["event"])
	if strings.TrimSpace(stringValue(event["type"])) != "request.steer" {
		return nil
	}
	content := strings.TrimSpace(stringValue(event["message"]))
	if content == "" {
		return nil
	}
	role := strings.TrimSpace(stringValue(event["role"]))
	if role == "" {
		role = "user"
	}
	if role != "user" {
		return nil
	}
	msg := map[string]any{
		"role":    role,
		"content": content,
		"ts":      line["updatedAt"],
	}
	if runID := strings.TrimSpace(stringValue(line["runId"])); runID != "" {
		msg["runId"] = runID
	}
	return msg
}

func buildLLMSystemCache(lines []map[string]any) map[string]llmSystemSnapshot {
	cache := map[string]llmSystemSnapshot{}
	for _, line := range lines {
		lineType := strings.TrimSpace(stringValue(line["_type"]))
		if lineType != "query" && !lineIsStep(line) {
			continue
		}
		systems, err := queryLineSystemInitsFromJSONL(line)
		if err != nil {
			continue
		}
		for _, system := range systems {
			cache[systemCacheID(system.AgentKey, system.CacheKey, system.Fingerprint)] = llmSystemSnapshot{
				AgentKey:       system.AgentKey,
				CacheKey:       system.CacheKey,
				Fingerprint:    system.Fingerprint,
				SystemMessage:  cloneMapDeep(system.SystemMessage),
				Tools:          cloneAnySliceDeep(system.Tools),
				Model:          cloneMapDeep(system.Model),
				ToolChoice:     strings.TrimSpace(system.ToolChoice),
				RequestOptions: cloneMapDeep(system.RequestOptions),
			}
		}
	}
	return cache
}

func resolveLLMChatSystem(target map[string]any, cache map[string]llmSystemSnapshot) (map[string]any, []any, map[string]any, llmSystemSnapshot, error) {
	systemRef := cloneMapDeep(anyMap(target["systemRef"]))
	if len(systemRef) == 0 {
		return nil, nil, nil, llmSystemSnapshot{}, fmt.Errorf("llm chat target missing systemRef")
	}
	cacheKey := strings.TrimSpace(stringValue(systemRef["cacheKey"]))
	fingerprint := strings.TrimSpace(stringValue(systemRef["fingerprint"]))
	if cacheKey == "" || fingerprint == "" {
		return nil, nil, systemRef, llmSystemSnapshot{}, fmt.Errorf("llm chat target has incomplete systemRef")
	}
	agentKey := strings.TrimSpace(stringValue(systemRef["agentKey"]))
	if agentKey != "" {
		if snapshot, ok := cache[systemCacheID(agentKey, cacheKey, fingerprint)]; ok {
			return cloneMapDeep(snapshot.SystemMessage), cloneAnySliceDeep(snapshot.Tools), systemRef, snapshot, nil
		}
		if snapshot, ok := cache[systemCacheID("", cacheKey, fingerprint)]; ok {
			return cloneMapDeep(snapshot.SystemMessage), cloneAnySliceDeep(snapshot.Tools), systemRef, snapshot, nil
		}
		return nil, nil, systemRef, llmSystemSnapshot{}, fmt.Errorf("llm chat system snapshot not found")
	}
	var matched *llmSystemSnapshot
	for _, snapshot := range cache {
		if strings.TrimSpace(snapshot.CacheKey) != cacheKey || strings.TrimSpace(snapshot.Fingerprint) != fingerprint {
			continue
		}
		candidate := snapshot
		if matched != nil && matched.AgentKey != candidate.AgentKey {
			return nil, nil, systemRef, llmSystemSnapshot{}, fmt.Errorf("llm chat systemRef is ambiguous across agents")
		}
		matched = &candidate
	}
	if matched == nil {
		return nil, nil, systemRef, llmSystemSnapshot{}, fmt.Errorf("llm chat system snapshot not found")
	}
	return cloneMapDeep(matched.SystemMessage), cloneAnySliceDeep(matched.Tools), systemRef, *matched, nil
}

func systemCacheID(agentKey string, cacheKey string, fingerprint string) string {
	return strings.TrimSpace(agentKey) + "\x00" + strings.TrimSpace(cacheKey) + "\x00" + strings.TrimSpace(fingerprint)
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
