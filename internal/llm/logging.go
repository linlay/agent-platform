package llm

import (
	"fmt"
	"log"
	"strings"

	. "agent-platform/internal/models"
	"agent-platform/internal/observability"
)

const (
	llmConsoleRequest = "request"
	llmConsoleBody    = "body"
	llmConsoleRaw     = "raw"
	llmConsoleParsed  = "parsed"
	llmConsoleUsage   = "usage"
	llmConsoleHitl    = "hitl"
	llmConsolePrompt  = "prompt"
	llmConsoleSystem  = "system"
	llmConsoleMedia   = "media"
	llmConsoleTrace   = "trace"
)

func (e *LLMAgentEngine) logOutgoingRequest(runID string, provider ProviderDefinition, model ModelDefinition, endpoint string, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, body []byte) {
	if strings.TrimSpace(toolChoice) == "" {
		toolChoice = "none"
	}
	if e.llmConsoleEnabled(llmConsoleRequest) {
		log.Printf(
			"[llm][run:%s][request_summary] provider=%s endpoint=%s model=%s messageCount=%d toolCount=%d toolChoice=%s",
			runID,
			e.formatLogText(provider.Key),
			e.formatLogText(endpoint),
			e.formatLogText(model.ModelID),
			len(messages),
			len(toolSpecs),
			e.formatLogText(toolChoice),
		)
	}
	if e.llmConsoleEnabled(llmConsoleBody) {
		log.Printf(
			"[llm][run:%s][request_body] provider=%s endpoint=%s model=%s body=%s",
			runID,
			e.formatLogText(provider.Key),
			e.formatLogText(endpoint),
			e.formatLogText(model.ModelID),
			e.formatLogText(string(body)),
		)
	}
}

func (e *LLMAgentEngine) logMissingToolSpecsWarning(runID string, requestedToolNames []string) {
	log.Printf(
		"[llm][run:%s][warning] requestedTools=%s no tool schema included in provider request",
		runID,
		e.formatLogText(strings.Join(requestedToolNames, ",")),
	)
}

func (e *LLMAgentEngine) logRawChunk(runID string, chunk string) {
	if !e.llmConsoleEnabled(llmConsoleRaw) {
		return
	}
	log.Printf("[llm][run:%s][raw_chunk] %s", runID, e.formatLogText(chunk))
}

func (e *LLMAgentEngine) logParsedDelta(runID string, kind string, value string) {
	if !e.llmConsoleEnabled(llmConsoleParsed) {
		return
	}
	log.Printf("[llm][run:%s][parsed_%s] %s", runID, kind, e.formatLogText(value))
}

func (e *LLMAgentEngine) logParsedToolDelta(runID string, delta openAIStreamToolDelta) {
	if !e.llmConsoleEnabled(llmConsoleParsed) {
		return
	}
	log.Printf(
		"[llm][run:%s][parsed_tool_call] index=%d id=%s type=%s name=%s args=%s",
		runID,
		delta.Index,
		e.formatLogText(delta.ID),
		e.formatLogText(delta.Type),
		e.formatLogText(delta.Function.Name),
		"[REDACTED_TOOL_ARGUMENTS]",
	)
}

func (e *LLMAgentEngine) llmConsoleEnabled(category string) bool {
	if e == nil || !e.cfg.Logging.LLMInteraction.Enabled {
		return false
	}
	return llmConsoleCategoryEnabled(e.cfg.Logging.LLMInteraction.ConsoleCategories, category)
}

func llmConsoleCategoryEnabled(categories []string, category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" {
		return false
	}
	hasAll := false
	hasCategory := false
	for _, raw := range categories {
		value := strings.ToLower(strings.TrimSpace(raw))
		switch value {
		case "none":
			return false
		case "all":
			hasAll = true
		case category:
			hasCategory = true
		}
	}
	return hasAll || hasCategory
}

func (e *LLMAgentEngine) formatLogText(text string) string {
	normalized := observability.SanitizeLog(strings.ReplaceAll(strings.TrimSpace(text), "\n", "\\n"))
	if normalized == "" {
		return `""`
	}
	if e.cfg.Logging.LLMInteraction.MaskSensitive {
		return fmt.Sprintf("[masked chars=%d]", len(normalized))
	}
	return normalized
}
