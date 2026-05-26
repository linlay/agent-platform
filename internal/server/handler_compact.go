package server

import (
	"context"
	"encoding/json"
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

type compactStore interface {
	BuildCompactSnapshot(chatID string, keptRunCount int) (chat.CompactSnapshot, error)
	AppendCompactLine(chatID string, line chat.CompactLine) error
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
	startedAt := time.Now()
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: "chatId is required"}
	}
	store, ok := s.deps.Chats.(compactStore)
	if !ok {
		return api.CompactResponse{}, &statusError{status: http.StatusServiceUnavailable, message: "chat store does not support compact"}
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return api.CompactResponse{}, err
	}
	if summary == nil {
		return api.CompactResponse{}, &statusError{status: http.StatusNotFound, message: "chat not found"}
	}
	agentKey := strings.TrimSpace(req.AgentKey)
	if agentKey == "" {
		agentKey = strings.TrimSpace(summary.AgentKey)
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return api.CompactResponse{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = newRunID()
	}
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	snapshot, err := store.BuildCompactSnapshot(chatID, chat.DefaultCompactKeptRunCount)
	if err != nil {
		if errors.Is(err, chat.ErrNoCompactableHistory) {
			return api.CompactResponse{
				Accepted:  false,
				Status:    "skipped",
				RequestID: requestID,
				ChatID:    chatID,
				Detail:    "没有可压缩的历史上下文",
			}, nil
		}
		return api.CompactResponse{}, err
	}
	compactID := "compact_" + newRunID()
	summaryText, usage, summaryErr := s.generateCompactSummary(ctx, requestID, compactID, *summary, agentDef, snapshot)
	source := "model"
	errText := ""
	if summaryErr != nil || strings.TrimSpace(summaryText) == "" {
		source = "deterministic_fallback"
		errText = fmt.Sprint(summaryErr)
		summaryText = snapshot.FallbackSummary
		log.Printf("[compact] model summary failed chatId=%s compactId=%s err=%v", chatID, compactID, summaryErr)
	}
	if strings.TrimSpace(summaryText) == "" {
		summaryText = "此前对话已被压缩，但没有生成有效摘要。请优先参考 checkpoint 之后的最新消息。"
	}
	compactUpdatedAt := time.Now().UnixMilli()
	staticPromptTokens := s.estimateStaticPromptTokens(ctx, api.QueryRequest{
		RequestID:   requestID,
		RunID:       compactID,
		ChatID:      chatID,
		AgentKey:    agentDef.Key,
		TeamID:      summary.TeamID,
		AccessLevel: contracts.AccessLevelDefault,
	}, *summary, agentDef)
	preCompactTokens := snapshot.PreCompactTokens + staticPromptTokens
	postCompactTokens := chat.EstimateCompactPostTokens(summaryText, source, compactUpdatedAt, snapshot.TailMessages, 20) + staticPromptTokens
	compressionRatio := 1.0
	if preCompactTokens > 0 {
		compressionRatio = float64(postCompactTokens) / float64(preCompactTokens)
	}
	line := chat.CompactLine{
		Type:              "compact",
		ChatID:            chatID,
		RunID:             snapshot.BoundaryRunID,
		CompactID:         compactID,
		UpdatedAt:         compactUpdatedAt,
		BoundaryRunID:     snapshot.BoundaryRunID,
		BoundarySeq:       snapshot.BoundarySeq,
		Generation:        snapshot.Generation,
		Summary:           summaryText,
		SummarySource:     source,
		KeptRunCount:      chat.DefaultCompactKeptRunCount,
		CompactedRunCount: snapshot.CompactedRunCount,
		ToolDigests:       snapshot.ToolDigests,
		DigestedRunIDs:    snapshot.DigestedRunIDs,
		CompactionUsage:   usage,
		CacheMetrics:      snapshot.CacheMetrics,
		Error:             errText,
		Trigger:           trigger,
		OriginalMessages:  snapshot.OriginalMessages,
		ProjectedMessages: snapshot.ProjectedMessages,
		PreCompactTokens:  preCompactTokens,
		PostCompactTokens: postCompactTokens,
		CompressionRatio:  compressionRatio,
		ElapsedMs:         time.Since(startedAt).Milliseconds(),
	}
	if err := store.AppendCompactLine(chatID, line); err != nil {
		return api.CompactResponse{}, err
	}
	return api.CompactResponse{
		Accepted:          true,
		Status:            "compacted",
		RequestID:         requestID,
		ChatID:            chatID,
		CompactID:         compactID,
		SummarySource:     source,
		BoundaryRunID:     snapshot.BoundaryRunID,
		BoundarySeq:       snapshot.BoundarySeq,
		Generation:        snapshot.Generation,
		KeptRunCount:      chat.DefaultCompactKeptRunCount,
		CompactedRunCount: snapshot.CompactedRunCount,
		ToolDigestCount:   len(snapshot.ToolDigests),
		DigestedRunIDs:    snapshot.DigestedRunIDs,
		OriginalMessages:  snapshot.OriginalMessages,
		ProjectedMessages: snapshot.ProjectedMessages,
		PreCompactTokens:  preCompactTokens,
		PostCompactTokens: postCompactTokens,
		CompressionRatio:  compressionRatio,
		CompactionUsage:   usage,
		CacheMetrics:      snapshot.CacheMetrics,
		ElapsedMs:         line.ElapsedMs,
		Detail:            errText,
	}, nil
}

func (s *Server) generateCompactSummary(ctx context.Context, requestID string, compactID string, summary chat.Summary, agentDef catalog.AgentDefinition, snapshot chat.CompactSnapshot) (string, map[string]any, error) {
	if s.deps.Agent == nil {
		return "", nil, fmt.Errorf("agent engine is not configured")
	}
	hidden := true
	req := api.QueryRequest{
		RequestID: requestID,
		RunID:     compactID,
		ChatID:    summary.ChatID,
		AgentKey:  agentDef.Key,
		TeamID:    summary.TeamID,
		Message:   snapshot.Prompt,
		Hidden:    &hidden,
	}
	session, err := s.BuildQuerySession(ctx, req, summary, agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    false,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
	})
	if err != nil {
		return "", nil, err
	}
	session.Mode = "ONESHOT"
	session.ToolNames = []string{"__compact_no_tools__"}
	session.HistoryMessages = nil
	session.ReactMaxSteps = 1
	stream, err := s.deps.Agent.Stream(ctx, req, session)
	if err != nil {
		return "", nil, err
	}
	defer stream.Close()

	var b strings.Builder
	var usage map[string]any
	for {
		delta, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return strings.TrimSpace(b.String()), usage, err
		}
		switch value := delta.(type) {
		case contracts.DeltaContent:
			b.WriteString(value.Text)
		case contracts.DeltaUsageSnapshot:
			usage = usageMapFromDelta(value)
		case contracts.DeltaDebugPostCall:
			if usage == nil {
				usage = usageMapFromDebugPostCall(value)
			}
		case contracts.DeltaError:
			return strings.TrimSpace(b.String()), usage, fmt.Errorf("%v", value.Error)
		}
	}
	return strings.TrimSpace(b.String()), usage, nil
}

func (s *Server) estimateStaticPromptTokens(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition) int {
	if s == nil || s.deps.SystemInits == nil || s.deps.Tools == nil {
		return 0
	}
	session, err := s.BuildQuerySession(ctx, req, summary, agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    false,
		IncludeMemory:     false,
		AllowInvokeAgents: canUseInvokeAgentsTool(agentDef.Mode),
	})
	if err != nil {
		return 0
	}
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		session,
		req,
		s.deps.Tools.Definitions(),
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	if len(profiles) == 0 {
		return 0
	}
	return estimateSystemInitProfileTokens(profiles[0])
}

func estimateSystemInitProfileTokens(profile contracts.SystemInitProfile) int {
	totalChars := 0
	if len(profile.SystemMessage) > 0 {
		if raw, err := json.Marshal(profile.SystemMessage); err == nil {
			totalChars += len(raw)
		}
	}
	if len(profile.Tools) > 0 {
		if raw, err := json.Marshal(profile.Tools); err == nil {
			totalChars += len(raw) / 2
		}
	}
	if totalChars == 0 {
		return 0
	}
	return totalChars/4 + 1
}

func usageMapFromDelta(delta contracts.DeltaUsageSnapshot) map[string]any {
	return usageDataMap(chat.UsageData{
		PromptTokens:           delta.LLMReturnPromptTokens,
		CompletionTokens:       delta.LLMReturnCompletionTokens,
		TotalTokens:            delta.LLMReturnTotalTokens,
		CachedTokens:           delta.LLMReturnCachedTokens,
		ReasoningTokens:        delta.LLMReturnReasoningTokens,
		PromptCacheHitTokens:   delta.LLMReturnPromptCacheHitTokens,
		PromptCacheMissTokens:  delta.LLMReturnPromptCacheMissTokens,
		LlmChatCompletionCount: delta.LLMReturnLLMChatCompletionCount,
	})
}

func usageMapFromDebugPostCall(delta contracts.DeltaDebugPostCall) map[string]any {
	return usageDataMap(chat.UsageData{
		PromptTokens:           delta.LLMReturnPromptTokens,
		CompletionTokens:       delta.LLMReturnCompletionTokens,
		TotalTokens:            delta.LLMReturnTotalTokens,
		CachedTokens:           delta.LLMReturnCachedTokens,
		ReasoningTokens:        delta.LLMReturnReasoningTokens,
		PromptCacheHitTokens:   delta.LLMReturnPromptCacheHitTokens,
		PromptCacheMissTokens:  delta.LLMReturnPromptCacheMissTokens,
		LlmChatCompletionCount: delta.LLMReturnLLMChatCompletionCount,
	})
}

func (s *Server) maybeAutoCompact(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition, session *contracts.QuerySession) {
	if session == nil || len(session.HistoryMessages) == 0 || s.deps.Models == nil {
		return
	}
	model, _, err := s.deps.Models.Get(session.ModelKey)
	if err != nil || model.ContextWindow <= 0 {
		return
	}
	estimated := estimatePromptTokens(session.HistoryMessages, req.Message)
	buffer := compactThresholdBuffer(model.ContextWindow)
	if estimated < model.ContextWindow-buffer {
		return
	}
	resp, err := s.compactChat(ctx, api.CompactRequest{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		AgentKey:  agentDef.Key,
		Trigger:   "auto",
	})
	if err != nil {
		log.Printf("[compact] auto compact failed chatId=%s runId=%s err=%v", req.ChatID, req.RunID, err)
		return
	}
	if !resp.Accepted {
		log.Printf("[compact] auto compact skipped chatId=%s runId=%s status=%s detail=%s", req.ChatID, req.RunID, resp.Status, resp.Detail)
		return
	}
	if messages, err := s.deps.Chats.LoadRawMessages(req.ChatID, s.deps.Config.ChatStorage.K); err == nil {
		session.HistoryMessages = messages
	}
	log.Printf("[compact] auto compact completed chatId=%s runId=%s compactId=%s source=%s", req.ChatID, req.RunID, resp.CompactID, resp.SummarySource)
}

func estimatePromptTokens(messages []map[string]any, nextMessage string) int {
	totalChars := len(nextMessage)
	for _, message := range messages {
		if data, err := json.Marshal(message); err == nil {
			totalChars += len(data)
		} else {
			totalChars += len(fmt.Sprint(message))
		}
	}
	if totalChars == 0 {
		return 0
	}
	return totalChars/4 + 1
}

func compactThresholdBuffer(contextWindow int) int {
	buffer := int(float64(contextWindow) * 0.15)
	if buffer < 4000 {
		buffer = 4000
	}
	if buffer > 12000 {
		buffer = 12000
	}
	return buffer
}
