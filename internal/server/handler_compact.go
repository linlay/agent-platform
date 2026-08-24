package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

type compactChatStore interface {
	BuildCompactSnapshot(chatID string, keptRunCount int) (chat.CompactSnapshot, error)
	CommitCompactCheckpoint(chatID string, snapshot chat.CompactSnapshot, checkpoint chat.CompactCheckpointLine) error
	BuildToolCompactSnapshot(chatID string, keepRecent int) (chat.ToolCompactSnapshot, error)
	CommitToolCompact(chatID string, snapshot chat.ToolCompactSnapshot, line chat.ToolCompactLine) error
}

const historyCompactTargetPercent = 60

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
	if level == "summary" {
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
	} else if s.deps.Runs != nil {
		if active, ok, activeErr := s.deps.Runs.ActiveRunForChat(chatID); activeErr != nil {
			return api.CompactResponse{}, activeErr
		} else if ok {
			baseResp.RunID = active.RunID
			baseResp.Scope = "run"
			baseResp.Detail = "unsupported_active_level"
			return baseResp, nil
		}
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
	if level == "l1_tools" {
		return s.compactChatToolResults(baseResp, store, chatID, requestID, trigger)
	}

	keptRunCount := chat.DefaultCompactKeptRunCount
	historyTargetTokens := 0
	if agentOK && s.deps.Models != nil {
		if model, modelErr := s.deps.Models.GetModel(agentDef.ModelKey); modelErr == nil && model.ContextWindow > 0 {
			historyTargetTokens = model.ContextWindow * historyCompactTargetPercent / 100
		}
	}
	snapshot, err := store.BuildCompactSnapshot(chatID, keptRunCount)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_history"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}
	for historyTargetTokens > 0 && snapshot.PostCompactEstimatedTokens > historyTargetTokens && keptRunCount > 0 {
		keptRunCount--
		snapshot, err = store.BuildCompactSnapshot(chatID, keptRunCount)
		if err != nil {
			if errors.Is(err, chat.ErrNoCompactableHistory) {
				baseResp.Detail = "no_compactable_history"
				return baseResp, nil
			}
			return api.CompactResponse{}, err
		}
	}

	compactID := "compact_" + newRunID()
	summaryText := strings.TrimSpace(snapshot.FallbackSummary)
	summarySource := "deterministic_fallback"
	compactionUsage := map[string]any{}
	modelErrDetail := ""
	if strings.TrimSpace(snapshot.Prompt) != "" && agentOK && s.deps.Agent != nil {
		resolvedReq := req
		resolvedReq.AgentKey = agentKey
		resolvedSummary := *chatSummary
		resolvedSummary.TeamID = teamID
		modelSummary, usage, err := s.generateCompactSummary(ctx, resolvedReq, resolvedSummary, agentDef, compactID, snapshot.Prompt)
		if len(usage) > 0 {
			compactionUsage = usage
		}
		if err != nil {
			modelErrDetail = err.Error()
		} else if strings.TrimSpace(modelSummary) != "" {
			summaryText = strings.TrimSpace(modelSummary)
			summarySource = "model"
		} else {
			modelErrDetail = "empty compact summary"
		}
	}

	postTokens := chat.EstimateCompactPostTokens(summaryText, snapshot.TailMessages)
	ratio := 0.0
	if snapshot.PreCompactEstimatedTokens > 0 {
		ratio = float64(postTokens) / float64(snapshot.PreCompactEstimatedTokens)
	}
	checkpoint := chat.CompactCheckpointLine{
		Type:                       chat.CompactCheckpointLineType,
		ChatID:                     chatID,
		CompactID:                  compactID,
		UpdatedAt:                  time.Now().UnixMilli(),
		Trigger:                    trigger,
		Summary:                    summaryText,
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		TokensFreed:                max(snapshot.PreCompactEstimatedTokens-postTokens, 0),
		CompactionUsage:            compactionUsage,
	}
	if err := store.CommitCompactCheckpoint(chatID, snapshot, checkpoint); err != nil {
		if errors.Is(err, chat.ErrCompactHistoryChanged) {
			baseResp.CompactID = compactID
			baseResp.Detail = "history_changed"
			return baseResp, nil
		}
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.CompactID = compactID
			baseResp.Detail = "no_compactable_history"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}

	detail := "completed"
	if modelErrDetail != "" && summarySource != "model" {
		detail = "completed_with_fallback: " + modelErrDetail
	}
	return api.CompactResponse{
		Accepted:                   true,
		Status:                     "completed",
		RequestID:                  requestID,
		ChatID:                     chatID,
		CompactID:                  compactID,
		Trigger:                    trigger,
		Scope:                      "history",
		Level:                      level,
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		TokensFreed:                max(snapshot.PreCompactEstimatedTokens-postTokens, 0),
		CompactionUsage:            compactionUsage,
		Detail:                     detail,
	}, nil
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

func (s *Server) compactChatToolResults(baseResp api.CompactResponse, store compactChatStore, chatID string, requestID string, trigger string) (api.CompactResponse, error) {
	snapshot, err := store.BuildToolCompactSnapshot(chatID, chat.DefaultToolCompactKeepRecent)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_tool_results"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}
	baseResp.ToolsKept = snapshot.ToolsKept
	if snapshot.ToolsCleared == 0 {
		baseResp.Detail = "no_compactable_tool_results"
		return baseResp, nil
	}

	compactID := "compact_" + newRunID()
	line := chat.ToolCompactLine{
		Type:                       chat.ToolCompactLineType,
		ChatID:                     chatID,
		CompactID:                  compactID,
		UpdatedAt:                  time.Now().UnixMilli(),
		Trigger:                    trigger,
		Level:                      "l1_tools",
		ToolsCleared:               snapshot.ToolsCleared,
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: snapshot.PostCompactEstimatedTokens,
		CompressionRatio:           snapshot.CompressionRatio,
	}
	if err := store.CommitToolCompact(chatID, snapshot, line); err != nil {
		if errors.Is(err, chat.ErrCompactHistoryChanged) {
			baseResp.CompactID = compactID
			baseResp.Detail = "history_changed"
			return baseResp, nil
		}
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.CompactID = compactID
			baseResp.Detail = "no_compactable_tool_results"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}

	return api.CompactResponse{
		Accepted:                   true,
		Status:                     "completed",
		RequestID:                  requestID,
		ChatID:                     chatID,
		CompactID:                  compactID,
		Trigger:                    trigger,
		Scope:                      "history",
		Level:                      "l1_tools",
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: snapshot.PostCompactEstimatedTokens,
		CompressionRatio:           snapshot.CompressionRatio,
		ToolsCleared:               snapshot.ToolsCleared,
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		Detail:                     "completed",
	}, nil
}

func (s *Server) generateCompactSummary(ctx context.Context, req api.CompactRequest, chatSummary chat.Summary, agentDef catalog.AgentDefinition, compactID string, prompt string) (string, map[string]any, error) {
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
	session.HistoryMessages = nil
	session.StableMemoryContext = ""
	session.SessionMemoryContext = ""
	session.ObservationContext = ""
	session.MemoryUsageSummary = nil
	session.ResolvedBudget = contracts.NormalizeBudget(contracts.Budget{MaxSteps: 1})
	s.hydrateSystemInitCache(summaryReq, &session)

	agentStream, err := s.deps.Agent.Stream(ctx, summaryReq, session)
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

func (s *Server) maybeAutoCompact(ctx context.Context, req api.QueryRequest, agentDef catalog.AgentDefinition, session *contracts.QuerySession) {
	if session == nil || s.deps.Models == nil || s.deps.Chats == nil {
		return
	}
	if req.ChatID == "" || len(session.HistoryMessages) == 0 {
		return
	}
	model, err := s.deps.Models.GetModel(session.ModelKey)
	if err != nil || model.ContextWindow <= 0 {
		return
	}
	estimated := chat.EstimateRawMessageTokens(session.HistoryMessages) + chat.EstimateTextTokens(req.Message)
	if estimated < compactTriggerThreshold(model.ContextWindow) {
		return
	}
	resp, err := s.compactChat(ctx, api.CompactRequest{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		AgentKey:  agentDef.Key,
		Trigger:   "auto",
	})
	if err != nil {
		log.Printf("[compact][auto] skipped chatId=%s err=%v", req.ChatID, err)
		return
	}
	if !resp.Accepted {
		log.Printf("[compact][auto] skipped chatId=%s detail=%s", req.ChatID, resp.Detail)
		return
	}
	reloaded, err := s.deps.Chats.LoadRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
	if err != nil {
		log.Printf("[compact][auto] reload history failed chatId=%s err=%v", req.ChatID, err)
		return
	}
	session.HistoryMessages = reloaded
	log.Printf("[compact][auto] completed chatId=%s compactId=%s pre=%d post=%d ratio=%.4f",
		req.ChatID,
		resp.CompactID,
		resp.PreCompactEstimatedTokens,
		resp.PostCompactEstimatedTokens,
		resp.CompressionRatio,
	)
}

func compactTriggerThreshold(contextWindow int) int {
	if contextWindow <= 0 {
		return 0
	}
	reserve := contextWindow * 15 / 100
	if reserve < 4000 {
		reserve = 4000
	}
	if reserve > 12000 {
		reserve = 12000
	}
	if reserve >= contextWindow {
		reserve = contextWindow / 4
	}
	return contextWindow - reserve
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
