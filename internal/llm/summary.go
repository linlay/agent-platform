package llm

import (
	"context"
	"encoding/json"

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
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	inputTokens := chat.EstimateTextTokens(string(encoded))
	remaining := s.effectiveContextWindow() - inputTokens - 64
	if remaining < s.summaryTargetTokens {
		return chat.ErrCompactSummaryInputTooLarge
	}
	// No platform output cap: only a limit the provider itself requires
	// (Anthropic max_tokens, or one set by compat) is kept, clamped to what the
	// window leaves beside the actual input.
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if limit := AnyIntNode(body[key]); limit > remaining {
			body[key] = remaining
		}
	}
	encoded, err = json.Marshal(body)
	if err != nil {
		return err
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

func (e *LLMAgentEngine) StreamSummary(ctx context.Context, req api.QueryRequest, session QuerySession, prompt string, targetTokens int) (AgentStream, error) {
	session.RequestID, session.RunID = req.RequestID, req.RunID
	session.Mode, session.SubTaskID = "ONESHOT", ""
	session.ToolNames, session.ModeToolDefinitions = nil, nil
	session.GlobalMemoryContext = ""
	session.AgentMemoryContext = ""
	session.HistoryMessages, session.SystemInitCache = nil, nil
	session.TeamRuntime = nil
	session.RunLimits = RunLimits{}
	session.ResolvedBudget = NormalizeBudget(Budget{MaxSteps: 1})
	stream, err := e.newRunStreamWithOptions(ctx, req, session, false, runStreamOptions{
		SummaryTargetTokens: targetTokens,
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
