package llm

import (
	"context"
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

// Apply the isolated summary contract after provider compatibility overrides,
// then check the actual tool-free request, not only the prompt projection.
func (s *llmRunStream) prepareSummaryRequest(prepared *preparedProviderRequest) error {
	body := prepared.RequestBody
	for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls", "functions", "function_call"} {
		delete(body, key)
	}
	_, legacyLimit := body["max_tokens"]
	if strings.EqualFold(s.model.Protocol, "ANTHROPIC") || legacyLimit {
		body["max_tokens"] = s.stageSettings.MaxOutputTokens
		delete(body, "max_completion_tokens")
	} else {
		body["max_completion_tokens"] = s.stageSettings.MaxOutputTokens
		delete(body, "max_tokens")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if chat.EstimateTextTokens(string(encoded))+s.stageSettings.MaxOutputTokens > s.effectiveContextWindow() {
		return chat.ErrCompactSummaryInputTooLarge
	}
	prepared.RequestBodyJSON = encoded
	return nil
}

func (e *LLMAgentEngine) EstimateContext(ctx context.Context, req api.QueryRequest, session QuerySession) (int, error) {
	stream, err := e.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{Stage: "main", DisableRunControl: true, DisableContextCompaction: true, EstimateOnly: true})
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	return stream.(*llmRunStream).fallbackContextEstimate(), nil
}

func (e *LLMAgentEngine) StreamSummary(ctx context.Context, req api.QueryRequest, session QuerySession, prompt string, maxOutputTokens int) (AgentStream, error) {
	session.RequestID, session.RunID = req.RequestID, req.RunID
	session.Mode, session.SubTaskID = "ONESHOT", ""
	session.ToolNames, session.ModeToolDefinitions = nil, nil
	session.HistoryMessages, session.SystemInitCache = nil, nil
	session.TeamRuntime = nil
	session.StableMemoryContext, session.SessionMemoryContext, session.ObservationContext = "", "", ""
	session.MemoryUsageSummary = nil
	session.RunLimits = RunLimits{}
	session.ResolvedBudget = NormalizeBudget(Budget{MaxSteps: 1})
	stream, err := e.newRunStreamWithOptions(ctx, req, session, false, runStreamOptions{
		SummaryOutputTokens: maxOutputTokens,
		Stage:               "summary", MaxSteps: 1,
		Messages:                     []openAIMessage{{Role: "user", Content: prompt}},
		PreserveProvidedSystemPrompt: true,
		DisableContextCompaction:     true, DisableRunControl: true,
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}
