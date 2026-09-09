package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
)

type compactChatStore interface {
	BuildCompactSnapshot(chatID string, keptRunCount int) (chat.CompactSnapshot, error)
	CommitCompactCheckpoint(chatID string, snapshot chat.CompactSnapshot, checkpoint chat.CompactCheckpointLine) error
	BuildToolCompactSnapshotToTarget(chatID string, keepRecent, targetTokens int) (chat.ToolCompactSnapshot, error)
	CommitToolCompact(chatID string, snapshot chat.ToolCompactSnapshot, line chat.ToolCompactLine) error
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	var req api.CompactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid request body"))
		return
	}
	resp, err := s.compactChat(r.Context(), req)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(resp))
}

func (s *Server) compactChat(ctx context.Context, req api.CompactRequest) (result api.CompactResponse, resultErr error) {
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: "chatId is required"}
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = newRunID()
	}
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	level, err := normalizeCompactLevel(req.Level)
	if err != nil {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: err.Error()}
	}
	baseResp := api.CompactResponse{
		RequestID: requestID,
		ChatID:    chatID,
		Trigger:   trigger,
		Scope:     "history",
		Level:     level,
		Status:    "skipped",
		Detail:    "skipped",
	}
	if s.deps.Chats == nil {
		return api.CompactResponse{}, &statusError{status: http.StatusServiceUnavailable, message: "chat store is not configured"}
	}
	store, ok := s.deps.Chats.(compactChatStore)
	if !ok {
		baseResp.Detail = "compact store is not supported"
		return baseResp, nil
	}
	chatSummary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return api.CompactResponse{}, err
	}
	if chatSummary == nil {
		return api.CompactResponse{}, &statusError{status: http.StatusNotFound, message: "chat not found"}
	}
	if coordinator, ok := s.deps.Runs.(contracts.ChatCompactCoordinator); ok {
		response, routed, historyOwner, routeErr := s.compactCoordinated(ctx, coordinator, baseResp)
		if routeErr != nil || routed {
			return response, routeErr
		}
		if historyOwner {
			defer func() {
				completion := result
				if resultErr != nil {
					completion = baseResp
					completion.Status = "failed"
					completion.Detail = resultErr.Error()
					completion.Retryable = true
				}
				coordinator.CompleteChatMaintenance(chatID, requestID, completion)
			}()
		}
	} else if response, routed, routeErr := s.compactActiveRun(ctx, baseResp); routed || routeErr != nil {
		return response, routeErr
	}

	agentKey := strings.TrimSpace(req.AgentKey)
	explicitAgentKey := agentKey != ""
	var agentDef catalog.AgentDefinition
	agentOK := false
	teamID, resolvedAgentKey, teamSnapshot, teamErr := resolveQueryTeam(
		s.deps.Registry,
		strings.TrimSpace(chatSummary.TeamID),
		agentKey,
		chatSummary,
	)
	if teamErr != nil {
		return api.CompactResponse{}, teamErr
	}
	if teamSnapshot != nil {
		agentKey = resolvedAgentKey
		agentDef, agentOK = teamSnapshot.AgentDefinition(agentKey)
	} else {
		if agentKey == "" {
			agentKey = strings.TrimSpace(chatSummary.AgentKey)
		}
		if s.deps.Registry != nil && agentKey != "" {
			agentDef, agentOK = s.deps.Registry.AgentDefinition(agentKey)
		}
	}
	if explicitAgentKey && !agentOK {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}

	window := 128000
	if agentOK && s.deps.Models != nil {
		if model, err := s.deps.Models.GetModel(agentDef.ModelKey); err == nil && model.ContextWindow > 0 {
			window = model.ContextWindow
		}
	}
	compactID := "compact_" + newRunID()
	resolvedReq := req
	resolvedReq.AgentKey = agentKey
	resolvedSummary := *chatSummary
	resolvedSummary.TeamID = teamID
	overhead := 0
	if estimator, ok := s.deps.Agent.(contracts.ContextEstimator); ok && agentOK {
		budgetReq := api.QueryRequest{RequestID: requestID, RunID: compactID, ChatID: chatID, AgentKey: agentKey, TeamID: teamID}
		budgetSession, err := s.BuildQuerySession(ctx, budgetReq, resolvedSummary, agentDef, querySessionBuildOptions{IncludeMemory: false, IncludeHistory: false})
		if err != nil {
			return baseResp, err
		}
		overhead, err = estimator.EstimateContext(ctx, budgetReq, budgetSession)
		if err != nil {
			return baseResp, err
		}
	}
	return conversation.CompactHistory(store, baseResp, window, overhead, compactID, func(prompt string, output int) (string, map[string]any, error) {
		if !agentOK || s.deps.Agent == nil {
			return "", nil, fmt.Errorf("summary agent unavailable")
		}
		return s.generateCompactSummary(ctx, resolvedReq, resolvedSummary, agentDef, compactID, prompt, output)
	})
}

func (s *Server) compactCoordinated(ctx context.Context, coordinator contracts.ChatCompactCoordinator, base api.CompactResponse) (api.CompactResponse, bool, bool, error) {
	compactID := "compact_" + newRunID()
	ack, err := coordinator.RouteCompactForChat(contracts.CompactControlRequest{
		RequestID: base.RequestID,
		CompactID: compactID,
		ChatID:    base.ChatID,
		Trigger:   base.Trigger,
		Level:     base.Level,
	})
	if err != nil {
		return api.CompactResponse{}, true, false, err
	}
	switch ack.Status {
	case "history_acquired":
		return api.CompactResponse{}, false, true, nil
	case "history_joined", "queued", "joined", "completed":
		select {
		case <-ack.Handle.Done():
			return ack.Handle.Result(), true, false, nil
		case <-ctx.Done():
			return api.CompactResponse{}, true, false, ctx.Err()
		}
	case "unsupported":
		base.RunID = ack.Run.RunID
		base.CompactID = compactID
		base.Scope = "run"
		base.Detail = "unsupported_active_run"
		return base, true, false, nil
	case "busy":
		base.RunID = ack.Run.RunID
		base.CompactID = compactID
		base.Status = "busy"
		base.Detail = "compact_in_progress"
		base.Retryable = true
		return base, true, false, nil
	case "invalid":
		return api.CompactResponse{}, true, false, &statusError{status: http.StatusBadRequest, message: "invalid compact request"}
	default:
		return api.CompactResponse{}, true, false, fmt.Errorf("unsupported compact routing status %q", ack.Status)
	}
}

func (s *Server) compactActiveRun(ctx context.Context, base api.CompactResponse) (api.CompactResponse, bool, error) {
	if s == nil || s.deps.Runs == nil {
		return api.CompactResponse{}, false, nil
	}
	router, ok := s.deps.Runs.(contracts.ActiveRunCompactService)
	if !ok {
		return api.CompactResponse{}, false, nil
	}
	compactID := "compact_" + newRunID()
	ack, err := router.RequestCompactForChat(base.ChatID, contracts.CompactControlRequest{
		RequestID: base.RequestID,
		CompactID: compactID,
		ChatID:    base.ChatID,
		Trigger:   base.Trigger,
		Level:     base.Level,
	})
	if err != nil {
		return api.CompactResponse{}, true, err
	}
	switch ack.Status {
	case "unmatched", "":
		return api.CompactResponse{}, false, nil
	case "unsupported":
		base.RunID = ack.Run.RunID
		base.CompactID = compactID
		base.Scope = "run"
		base.Detail = "unsupported_active_run"
		return base, true, nil
	case "busy":
		base.RunID = ack.Run.RunID
		base.CompactID = compactID
		base.Scope = "run"
		base.Status = "busy"
		base.Detail = "compact_in_progress"
		base.Retryable = true
		return base, true, nil
	case "invalid":
		return api.CompactResponse{}, true, &statusError{status: http.StatusBadRequest, message: "invalid compact request"}
	case "queued", "joined", "completed":
		select {
		case <-ack.Handle.Done():
			return ack.Handle.Result(), true, nil
		case <-ctx.Done():
			// The compact request remains owned by the active run even when the
			// caller disconnects; replay exposes its eventual terminal event.
			return api.CompactResponse{}, true, ctx.Err()
		}
	default:
		return api.CompactResponse{}, true, fmt.Errorf("unsupported compact routing status %q", ack.Status)
	}
}

func normalizeCompactLevel(level string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "summary":
		return "summary", nil
	case "l1_tools":
		return "l1_tools", nil
	default:
		return "", fmt.Errorf("invalid compact level")
	}
}

func (s *Server) generateCompactSummary(ctx context.Context, req api.CompactRequest, chatSummary chat.Summary, agentDef catalog.AgentDefinition, compactID string, prompt string, maxOutputTokens int) (string, map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summaryReq := api.QueryRequest{
		RequestID: strings.TrimSpace(req.RequestID),
		RunID:     compactID,
		ChatID:    strings.TrimSpace(req.ChatID),
		AgentKey:  agentDef.Key,
		TeamID:    chatSummary.TeamID,
		Role:      api.QueryRoleSystem,
		Message:   prompt,
	}
	if summaryReq.RequestID == "" {
		summaryReq.RequestID = compactID
	}
	session, err := s.BuildQuerySession(ctx, summaryReq, chatSummary, agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    false,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
	})
	if err != nil {
		return "", nil, err
	}
	session.RequestID = summaryReq.RequestID
	session.RunID = compactID
	session.Mode = "ONESHOT"
	session.ToolNames = nil
	session.ModeToolDefinitions = nil
	session.HistoryMessages = nil
	session.StableMemoryContext = ""
	session.SessionMemoryContext = ""
	session.ObservationContext = ""
	session.MemoryUsageSummary = nil
	session.ResolvedBudget = contracts.NormalizeBudget(contracts.Budget{MaxSteps: 1})
	var agentStream contracts.AgentStream
	if engine, ok := s.deps.Agent.(contracts.SummaryAgentEngine); ok {
		agentStream, err = engine.StreamSummary(ctx, summaryReq, session, prompt, maxOutputTokens)
	} else {
		return "", nil, fmt.Errorf("dedicated summary engine unavailable")
	}
	if err != nil {
		return "", nil, err
	}
	defer agentStream.Close()

	var b strings.Builder
	usage := map[string]any{}
	for {
		delta, nextErr := agentStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return strings.TrimSpace(b.String()), usage, nextErr
		}
		switch d := delta.(type) {
		case contracts.DeltaContent:
			b.WriteString(d.Text)
		case contracts.DeltaUsageSnapshot:
			usage = compactUsageFromUsageSnapshot(d)
		case contracts.DeltaDebugLLMChat:
			usage = compactUsageFromDebugLLMChat(d)
		case contracts.DeltaError:
			return strings.TrimSpace(b.String()), usage, fmt.Errorf("compact summary model error: %v", d.Error)
		}
	}
	return strings.TrimSpace(b.String()), usage, nil
}

func compactUsageFromUsageSnapshot(d contracts.DeltaUsageSnapshot) map[string]any {
	return compactUsageMap(
		d.LLMReturnPromptTokens,
		d.LLMReturnCompletionTokens,
		d.LLMReturnTotalTokens,
		d.LLMReturnReasoningTokens,
		d.LLMReturnPromptCacheHitTokens,
		d.LLMReturnPromptCacheMissTokens,
		d.LLMReturnLLMChatCompletionCount,
		d.LLMReturnToolCallCount,
	)
}

func compactUsageFromDebugLLMChat(d contracts.DeltaDebugLLMChat) map[string]any {
	return compactUsageMap(
		d.LLMReturnPromptTokens,
		d.LLMReturnCompletionTokens,
		d.LLMReturnTotalTokens,
		d.LLMReturnReasoningTokens,
		d.LLMReturnPromptCacheHitTokens,
		d.LLMReturnPromptCacheMissTokens,
		d.LLMReturnLLMChatCompletionCount,
		d.LLMReturnToolCallCount,
	)
}

func compactUsageMap(promptTokens int, completionTokens int, totalTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int) map[string]any {
	usage := map[string]any{}
	if promptTokens > 0 {
		usage["promptTokens"] = promptTokens
	}
	if completionTokens > 0 {
		usage["completionTokens"] = completionTokens
	}
	if totalTokens > 0 {
		usage["totalTokens"] = totalTokens
	}
	promptDetails := map[string]any{}
	if promptCacheHitTokens > 0 {
		promptDetails["cacheHitTokens"] = promptCacheHitTokens
	}
	if promptCacheMissTokens > 0 {
		promptDetails["cacheMissTokens"] = promptCacheMissTokens
	} else if promptTokens > promptCacheHitTokens && promptCacheHitTokens > 0 {
		promptDetails["cacheMissTokens"] = promptTokens - promptCacheHitTokens
	}
	if len(promptDetails) > 0 {
		usage["promptTokensDetails"] = promptDetails
	}
	if reasoningTokens > 0 {
		usage["completionTokensDetails"] = map[string]any{"reasoningTokens": reasoningTokens}
	}
	if llmChatCompletionCount > 0 {
		usage["llmChatCompletionCount"] = llmChatCompletionCount
	}
	if toolCallCount > 0 {
		usage["toolCallCount"] = toolCallCount
	}
	return usage
}
