package llm

import (
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

func buildInjectedPromptPayload(session QuerySession, req api.QueryRequest, options PromptBuildOptions, messages []openAIMessage) map[string]any {
	if len(messages) == 0 {
		return nil
	}

	lastUserIdx := -1
	for idx, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			lastUserIdx = idx
		}
	}

	providerMessages := make([]any, 0, len(messages))
	historyMessages := make([]any, 0, len(messages))
	var currentUserMessage map[string]any

	for idx, message := range messages {
		normalized := normalizeInjectedPromptMessage(message)
		if len(normalized) == 0 {
			continue
		}
		providerMessages = append(providerMessages, normalized)

		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "system" {
			continue
		}
		if role == "user" && idx == lastUserIdx {
			currentUserMessage = normalized
			continue
		}
		historyMessages = append(historyMessages, normalized)
	}

	systemSections := buildInjectedSystemSections(session, req, options)
	systemPrompt := ""
	if len(systemSections) > 0 {
		parts := make([]string, 0, len(systemSections))
		for _, section := range systemSections {
			content := strings.TrimSpace(stringValue(section["content"]))
			if content != "" {
				parts = append(parts, content)
			}
		}
		systemPrompt = strings.Join(parts, "\n\n")
	}

	payload := map[string]any{
		"providerMessages":       providerMessages,
		"providerMessagesTokens": estimateTokensFromValue(providerMessages),
	}
	if systemPrompt != "" {
		payload["systemPrompt"] = systemPrompt
		payload["systemPromptTokens"] = estimateTokensFromText(systemPrompt)
	}
	if len(systemSections) > 0 {
		payload["systemSections"] = systemSections
	}
	if len(historyMessages) > 0 {
		payload["historyMessages"] = historyMessages
		payload["historyMessagesTokens"] = estimateTokensFromValue(historyMessages)
	}
	if currentUserMessage != nil {
		payload["currentUserMessage"] = currentUserMessage
		payload["currentUserMessageTokens"] = estimateTokensFromValue(currentUserMessage)
	}
	return payload
}

func buildInjectedSystemSections(session QuerySession, req api.QueryRequest, options PromptBuildOptions) []map[string]any {
	appendConfig := effectivePromptAppendConfig(session.PromptAppend)
	stageInstructionsPrompt := strings.TrimSpace(options.StageInstructionsPrompt)
	if stageInstructionsPrompt == "" {
		stageInstructionsPrompt = resolveStageInstructionsPrompt(session, options.Stage)
	}
	stageSystemPrompt := strings.TrimSpace(options.StageSystemPrompt)
	if stageSystemPrompt == "" {
		stageSystemPrompt = resolveStageSystemPrompt(session, options.Stage)
	}

	sections := make([]map[string]any, 0, 16)
	appendSection := func(id, title, category, content string) {
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
		sections = append(sections, map[string]any{
			"id":       id,
			"title":    title,
			"role":     "system",
			"category": category,
			"content":  content,
			"tokens":   estimateTokensFromText(content),
		})
	}

	appendSection("agent-identity", "Agent Identity", "agent.identity", buildAgentIdentitySection(session))
	appendSection("agent-soul", "Soul Prompt", "agent.soul", strings.TrimSpace(session.SoulPrompt))
	appendSection("static-memory", "Static Memory Prompt", "memory.static", strings.TrimSpace(firstNonBlank(session.StaticMemoryPrompt, session.MemoryPrompt)))

	for _, tag := range session.ContextTags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "system":
			appendSection("runtime-system", "Runtime Context: System Environment", "runtime.system", buildSystemEnvironmentSection(session))
		case "session":
			appendSection("runtime-session", "Runtime Context: Session", "runtime.session", buildSessionSection(session, req))
		case "owner":
			appendSection("runtime-owner", "Runtime Context: Owner", "runtime.owner", buildOwnerSection(session.RuntimeContext.LocalPaths))
		case "all-agents":
			appendSection("runtime-all-agents", "Runtime Context: All Agents", "runtime.all_agents", buildAllAgentsSection(session.RuntimeContext.AgentDigests))
		}
	}
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		appendSection("runtime-sandbox", "Runtime Context: Sandbox", "runtime.sandbox", buildSandboxSection(session.RuntimeContext.SandboxContext))
	}
	if session.AgentHasMemoryConfig {
		appendSection("memory-stable", "Runtime Context: Stable Memory", "memory.stable", strings.TrimSpace(session.StableMemoryContext))
		appendSection("memory-session", "Runtime Context: Current Session", "memory.session", strings.TrimSpace(session.SessionMemoryContext))
		appendSection("memory-observation", "Runtime Context: Relevant Observations", "memory.observation", strings.TrimSpace(session.ObservationContext))
		appendSection("memory-workflow", "Runtime Context: Workflow Memory", "memory.workflow", strings.TrimSpace(session.WorkflowContext))
	}

	appendSection("stage-instructions", "Stage Instructions Prompt", "stage.instructions", stageInstructionsPrompt)
	appendSection("stage-system", "Stage System Prompt", "stage.system", stageSystemPrompt)
	appendSection("skill-catalog", "Skill Catalog Prompt", "skills.catalog", strings.TrimSpace(session.SkillCatalogPrompt))
	appendSection("tool-appendix", "Tool Appendix", "tools.appendix", buildToolAppendix(options.ToolDefinitions, appendConfig, options.IncludeAfterCallHints))

	return sections
}

func normalizeInjectedPromptMessage(message openAIMessage) map[string]any {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		return nil
	}

	normalized := map[string]any{
		"role": role,
	}
	if content := strings.TrimSpace(debugPromptContentText(message.Content)); content != "" {
		normalized["content"] = content
	}
	if strings.TrimSpace(message.Name) != "" {
		normalized["name"] = strings.TrimSpace(message.Name)
	}
	if strings.TrimSpace(message.ToolCallID) != "" {
		normalized["toolCallId"] = strings.TrimSpace(message.ToolCallID)
	}
	if len(message.ToolCalls) > 0 {
		toolCalls := make([]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":   call.ID,
				"type": call.Type,
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		normalized["toolCalls"] = toolCalls
	}
	normalized["estimatedTokens"] = estimateTokensFromOpenAIMessage(message)
	return normalized
}

func debugPromptContentText(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []string:
		return strings.Join(typed, "\n\n")
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func estimateTokensFromOpenAIMessage(message openAIMessage) int {
	raw, err := json.Marshal(message)
	if err != nil {
		return 0
	}
	return estimateTokensFromBytes(len(raw))
}

func estimateTokensFromValue(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return estimateTokensFromBytes(len(raw))
}

func estimateTokensFromText(text string) int {
	return estimateTokensFromBytes(len([]byte(text)))
}

func estimateTokensFromBytes(byteCount int) int {
	if byteCount <= 0 {
		return 0
	}
	return (byteCount + 3) / 4
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
