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
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	var req api.CompactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid request body"))
		return
	}
	resp, err := s.compactChat(r.Context(), req)
	if err != nil {
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

func (s *Server) compactChat(ctx context.Context, req api.CompactRequest) (api.CompactResponse, error) {
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
	baseResp := api.CompactResponse{
		RequestID: requestID,
		ChatID:    chatID,
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

	agentKey := strings.TrimSpace(req.AgentKey)
	explicitAgentKey := agentKey != ""
	if agentKey == "" {
		agentKey = strings.TrimSpace(chatSummary.AgentKey)
	}
	var agentDef catalog.AgentDefinition
	agentOK := false
	if s.deps.Registry != nil && agentKey != "" {
		agentDef, agentOK = s.deps.Registry.AgentDefinition(agentKey)
	}
	if explicitAgentKey && !agentOK {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}

	snapshot, err := store.BuildCompactSnapshot(chatID, chat.DefaultCompactKeptRunCount)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			baseResp.Detail = "no_compactable_history"
			return baseResp, nil
		}
		return api.CompactResponse{}, err
	}

	compactID := "compact_" + newRunID()
	summaryText := strings.TrimSpace(snapshot.FallbackSummary)
	summarySource := "deterministic_fallback"
	compactionUsage := map[string]any{}
	modelErrDetail := ""
	if strings.TrimSpace(snapshot.Prompt) != "" && agentOK && s.deps.Agent != nil {
		modelSummary, usage, err := s.generateCompactSummary(ctx, req, *chatSummary, agentDef, compactID, snapshot.Prompt)
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
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		CompactionUsage:            compactionUsage,
		Detail:                     detail,
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
	session.ReactMaxSteps = 1
	session.SystemInitCache = nil

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
		case contracts.DeltaDebugPostCall:
			usage = compactUsageFromDebugPostCall(d)
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
	reloaded, err := s.deps.Chats.LoadRawMessages(req.ChatID, s.deps.Config.ChatStorage.K)
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
		d.ModelKey,
		d.ReasoningEffort,
		d.ContextWindow,
		d.CurrentContextSize,
		d.EstimatedNextCallSize,
		d.LLMReturnPromptTokens,
		d.LLMReturnCompletionTokens,
		d.LLMReturnTotalTokens,
		d.LLMReturnCachedTokens,
		d.LLMReturnReasoningTokens,
		d.LLMReturnPromptCacheHitTokens,
		d.LLMReturnPromptCacheMissTokens,
		d.LLMReturnLLMChatCompletionCount,
		d.LLMReturnToolCallCount,
	)
}

func compactUsageFromDebugPostCall(d contracts.DeltaDebugPostCall) map[string]any {
	return compactUsageMap(
		d.ModelKey,
		d.ReasoningEffort,
		d.ContextWindow,
		d.CurrentContextSize,
		d.EstimatedNextCallSize,
		d.LLMReturnPromptTokens,
		d.LLMReturnCompletionTokens,
		d.LLMReturnTotalTokens,
		d.LLMReturnCachedTokens,
		d.LLMReturnReasoningTokens,
		d.LLMReturnPromptCacheHitTokens,
		d.LLMReturnPromptCacheMissTokens,
		d.LLMReturnLLMChatCompletionCount,
		d.LLMReturnToolCallCount,
	)
}

func compactUsageMap(modelKey string, reasoningEffort string, contextWindow int, currentContextSize int, estimatedNextCallSize int, promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int) map[string]any {
	usage := map[string]any{}
	if strings.TrimSpace(modelKey) != "" {
		usage["modelKey"] = strings.TrimSpace(modelKey)
	}
	if strings.TrimSpace(reasoningEffort) != "" {
		usage["reasoningEffort"] = strings.TrimSpace(reasoningEffort)
	}
	if contextWindow > 0 {
		usage["contextWindow"] = contextWindow
	}
	if currentContextSize > 0 {
		usage["currentContextSize"] = currentContextSize
	}
	if estimatedNextCallSize > 0 {
		usage["estimatedNextCallSize"] = estimatedNextCallSize
	}
	if promptTokens > 0 {
		usage["promptTokens"] = promptTokens
	}
	if completionTokens > 0 {
		usage["completionTokens"] = completionTokens
	}
	if totalTokens > 0 {
		usage["totalTokens"] = totalTokens
	}
	if cachedTokens > 0 {
		usage["cachedTokens"] = cachedTokens
	}
	if reasoningTokens > 0 {
		usage["reasoningTokens"] = reasoningTokens
	}
	if promptCacheHitTokens > 0 {
		usage["promptCacheHitTokens"] = promptCacheHitTokens
	}
	if promptCacheMissTokens > 0 {
		usage["promptCacheMissTokens"] = promptCacheMissTokens
	}
	if llmChatCompletionCount > 0 {
		usage["llmChatCompletionCount"] = llmChatCompletionCount
	}
	if toolCallCount > 0 {
		usage["toolCallCount"] = toolCallCount
	}
	return usage
}
