package llm

import (
	"fmt"
	"log"
	"strings"

	. "agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/observability"
)

func (e *LLMAgentEngine) logOutgoingRequest(runID string, provider ProviderDefinition, model ModelDefinition, endpoint string, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, body []byte) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	if strings.TrimSpace(toolChoice) == "" {
		toolChoice = "none"
	}
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
	log.Printf(
		"[llm][run:%s][request_body] provider=%s endpoint=%s model=%s body=%s",
		runID,
		e.formatLogText(provider.Key),
		e.formatLogText(endpoint),
		e.formatLogText(model.ModelID),
		e.formatLogText(string(body)),
	)
}

func (e *LLMAgentEngine) logMissingToolSpecsWarning(runID string, requestedToolNames []string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf(
		"[llm][run:%s][warning] requestedTools=%s no tool schema included in provider request",
		runID,
		e.formatLogText(strings.Join(requestedToolNames, ",")),
	)
}

func (e *LLMAgentEngine) logRawChunk(runID string, chunk string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][raw_chunk] %s", runID, e.formatLogText(chunk))
}

func (e *LLMAgentEngine) logParsedDelta(runID string, kind string, value string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][parsed_%s] %s", runID, kind, e.formatLogText(value))
}

func (e *LLMAgentEngine) logParsedToolDelta(runID string, delta openAIStreamToolDelta) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf(
		"[llm][run:%s][parsed_tool_call] index=%d id=%s type=%s name=%s args=%s",
		runID,
		delta.Index,
		e.formatLogText(delta.ID),
		e.formatLogText(delta.Type),
		e.formatLogText(delta.Function.Name),
		e.formatLogText(delta.Function.Arguments),
	)
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
