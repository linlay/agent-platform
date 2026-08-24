package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

const runCompactTargetPercent = 60

type contextCompactWork struct {
	request       CompactControlRequest
	previousState RunLoopState
	finishAfter   bool
	preTokens     int
	plan          contextCompactPlan
	awaitingID    string
}

type contextCompactPlan struct {
	system     []openAIMessage
	pinned     []openAIMessage
	retained   []openAIMessage
	candidates []openAIMessage
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

func (s *llmRunStream) scheduleContextCompact(finishAfter bool) bool {
	if s == nil || s.compactDisabled || s.compactWork != nil || strings.TrimSpace(s.session.SubTaskID) != "" {
		return false
	}
	var request CompactControlRequest
	manual := false
	if s.runControl != nil {
		request, manual = s.runControl.ClaimCompact()
	}
	preTokens := s.fallbackContextEstimate()
	if !manual {
		threshold := compactTriggerThreshold(s.effectiveContextWindow())
		if !s.forceContextCompact && (threshold <= 0 || preTokens < threshold) {
			return false
		}
		s.compactCounter++
		request = CompactControlRequest{
			RequestID: fmt.Sprintf("auto_%s_%d", s.session.RunID, s.compactCounter),
			CompactID: fmt.Sprintf("compact_%s_%d", s.session.RunID, s.compactCounter),
			ChatID:    s.session.ChatID,
			Trigger:   "auto",
			Level:     "summary",
		}
	}
	plan := s.buildContextCompactPlan(manual)
	if len(plan.candidates) == 0 {
		if manual && s.runControl != nil {
			s.runControl.CompleteCompact(request.RequestID, api.CompactResponse{
				Accepted:  false,
				Status:    "skipped",
				RequestID: request.RequestID,
				ChatID:    request.ChatID,
				RunID:     s.session.RunID,
				CompactID: request.CompactID,
				Trigger:   request.Trigger,
				Scope:     "run",
				Level:     request.Level,
				Detail:    "no_compactable_history",
			})
			return false
		}
		if s.forceContextCompact {
			s.forceContextCompact = false
			s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": "context_window_uncompactable", "message": "Context cannot be reduced below the model window"}})
			s.closeSteersAndFinish()
			return true
		}
		return false
	}
	previousState := RunLoopStateIdle
	awaitingID := s.currentCompactAwaitingID()
	if s.runControl != nil {
		previousState = s.runControl.State()
		s.runControl.TransitionState(RunLoopStateCompacting)
	}
	if s.execCtx != nil {
		s.execCtx.RunLoopState = RunLoopStateCompacting
	}
	s.compactWork = &contextCompactWork{
		request:       request,
		previousState: previousState,
		finishAfter:   finishAfter,
		preTokens:     preTokens,
		plan:          plan,
		awaitingID:    awaitingID,
	}
	s.pending = append(s.pending, DeltaContextCompact{
		Status:           "start",
		RequestID:        request.RequestID,
		CompactID:        request.CompactID,
		ChatID:           request.ChatID,
		RunID:            s.session.RunID,
		Trigger:          request.Trigger,
		Level:            request.Level,
		Scope:            "run",
		PreviousRunState: string(previousState),
		AwaitingID:       awaitingID,
	})
	return true
}

func (s *llmRunStream) buildContextCompactPlan(force bool) contextCompactPlan {
	if s == nil || len(s.messages) == 0 {
		return contextCompactPlan{}
	}
	systemEnd := 0
	for systemEnd < len(s.messages) && strings.EqualFold(strings.TrimSpace(s.messages[systemEnd].Role), "system") {
		systemEnd++
	}
	pinnedStart := s.pinnedMessageStart
	pinnedEnd := s.pinnedMessageEnd
	if pinnedStart < systemEnd || pinnedStart > len(s.messages) {
		pinnedStart = len(s.messages)
	}
	if pinnedEnd < pinnedStart || pinnedEnd > len(s.messages) {
		pinnedEnd = pinnedStart
	}
	plan := contextCompactPlan{
		system: cloneModelMessages(s.messages[:systemEnd]),
		pinned: cloneModelMessages(s.messages[pinnedStart:pinnedEnd]),
	}
	history := cloneModelMessages(s.messages[systemEnd:pinnedStart])
	progress := cloneModelMessages(s.messages[pinnedEnd:])
	groups := compactMessageGroups(progress)
	target := s.effectiveContextWindow() * runCompactTargetPercent / 100
	mandatory := estimateModelContext(plan.system, s.toolSpecs) + estimateModelContext(plan.pinned, nil)
	summaryAllowance := s.effectiveContextWindow() / 10
	if summaryAllowance > 4096 {
		summaryAllowance = 4096
	}
	remaining := target - mandatory - summaryAllowance
	if remaining < 0 {
		remaining = 0
	}
	keepFrom := len(groups)
	keptTokens := 0
	for i := len(groups) - 1; i >= 0 && !compactMessageGroupComplete(groups[i]); i-- {
		keepFrom = i
		keptTokens += estimateModelContext(groups[i], nil)
	}
	for i := keepFrom - 1; i >= 0; i-- {
		groupTokens := estimateModelContext(groups[i], nil)
		if keptTokens+groupTokens > remaining {
			break
		}
		keptTokens += groupTokens
		keepFrom = i
	}
	if force && len(history) == 0 && keepFrom == 0 && len(groups) > 0 && compactMessageGroupComplete(groups[0]) {
		keepFrom = 1
	}
	plan.candidates = append(plan.candidates, history...)
	for i := 0; i < keepFrom; i++ {
		plan.candidates = append(plan.candidates, groups[i]...)
	}
	for i := keepFrom; i < len(groups); i++ {
		plan.retained = append(plan.retained, groups[i]...)
	}
	return plan
}

func compactMessageGroupComplete(group []openAIMessage) bool {
	if len(group) == 0 || !strings.EqualFold(group[0].Role, "assistant") || len(group[0].ToolCalls) == 0 {
		return true
	}
	results := map[string]bool{}
	for _, message := range group[1:] {
		if strings.EqualFold(message.Role, "tool") {
			results[strings.TrimSpace(message.ToolCallID)] = true
		}
	}
	for _, call := range group[0].ToolCalls {
		if !results[strings.TrimSpace(call.ID)] {
			return false
		}
	}
	return true
}

func compactMessageGroups(messages []openAIMessage) [][]openAIMessage {
	groups := make([][]openAIMessage, 0, len(messages))
	for i := 0; i < len(messages); {
		message := messages[i]
		group := []openAIMessage{message}
		i++
		if strings.EqualFold(message.Role, "assistant") && len(message.ToolCalls) > 0 {
			ids := map[string]bool{}
			for _, call := range message.ToolCalls {
				ids[strings.TrimSpace(call.ID)] = true
			}
			for i < len(messages) && strings.EqualFold(messages[i].Role, "tool") && ids[strings.TrimSpace(messages[i].ToolCallID)] {
				group = append(group, messages[i])
				i++
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func (s *llmRunStream) executeContextCompact() error {
	work := s.compactWork
	if work == nil {
		return nil
	}
	rawCandidates := modelMessagesToMaps(work.plan.candidates)
	fallback := chat.DeterministicCompactSummary(rawCandidates)
	summary := strings.TrimSpace(fallback)
	summarySource := "deterministic_fallback"
	usage := map[string]any{}
	detail := "completed"
	if prompt := chat.BuildCompactPrompt(rawCandidates); strings.TrimSpace(prompt) != "" {
		modelSummary, modelUsage, err := s.generateContextCompactSummary(work.request, prompt)
		if len(modelUsage) > 0 {
			usage = modelUsage
		}
		if err == nil && strings.TrimSpace(modelSummary) != "" {
			summary = strings.TrimSpace(modelSummary)
			summarySource = "model"
		} else if err != nil {
			detail = "completed_with_fallback: " + err.Error()
		} else {
			detail = "completed_with_fallback: empty compact summary"
		}
	}
	if summary == "" {
		return s.failContextCompact(work, "no_compactable_history", false)
	}
	targetTokens := s.effectiveContextWindow() * runCompactTargetPercent / 100
	baseMessages := make([]openAIMessage, 0, len(work.plan.system)+len(work.plan.pinned)+len(work.plan.retained))
	baseMessages = append(baseMessages, work.plan.system...)
	baseMessages = append(baseMessages, work.plan.pinned...)
	baseMessages = append(baseMessages, work.plan.retained...)
	availableSummaryTokens := targetTokens - estimateModelContext(baseMessages, s.toolSpecs) - 64
	if availableSummaryTokens <= 0 {
		return s.failContextCompact(work, "context_window_uncompactable", false)
	}
	summary = truncateContextCompactSummary(summary, availableSummaryTokens*4)
	summaryMessage := openAIMessage{Role: "user", Content: chat.CompactCheckpointSummaryMessage(summary)}
	newMessages := make([]openAIMessage, 0, len(work.plan.system)+1+len(work.plan.pinned)+len(work.plan.retained))
	newMessages = append(newMessages, work.plan.system...)
	newMessages = append(newMessages, summaryMessage)
	newPinnedStart := len(newMessages)
	newMessages = append(newMessages, work.plan.pinned...)
	newPinnedEnd := len(newMessages)
	newMessages = append(newMessages, work.plan.retained...)
	postTokens := estimateModelContext(newMessages, s.toolSpecs)
	if postTokens >= work.preTokens || postTokens > targetTokens {
		return s.failContextCompact(work, "context_window_uncompactable", false)
	}
	s.messages = newMessages
	s.forceContextCompact = false
	s.pinnedMessageStart = newPinnedStart
	s.pinnedMessageEnd = newPinnedEnd
	s.resetContextEstimateAfterCompact()
	ratio := 0.0
	if work.preTokens > 0 {
		ratio = float64(postTokens) / float64(work.preTokens)
	}
	checkpointMessages := modelMessagesToMaps(newMessages[len(work.plan.system):])
	s.pending = append(s.pending, DeltaContextCompact{
		Status:                     "complete",
		RequestID:                  work.request.RequestID,
		CompactID:                  work.request.CompactID,
		ChatID:                     work.request.ChatID,
		RunID:                      s.session.RunID,
		Trigger:                    work.request.Trigger,
		Level:                      work.request.Level,
		Scope:                      "run",
		SummarySource:              summarySource,
		PreCompactEstimatedTokens:  work.preTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		TokensFreed:                max(work.preTokens-postTokens, 0),
		CompactionUsage:            usage,
		Detail:                     detail,
		CheckpointMessages:         checkpointMessages,
		PreviousRunState:           string(work.previousState),
		AwaitingID:                 work.awaitingID,
	})
	finishAfter := work.finishAfter
	s.compactWork = nil
	if s.runControl != nil {
		s.runControl.TransitionState(work.previousState)
	}
	if s.execCtx != nil {
		s.execCtx.RunLoopState = work.previousState
	}
	if finishAfter {
		s.closeSteers()
		s.finished = true
	}
	return nil
}

func truncateContextCompactSummary(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if len(string(runes[:mid])) <= maxBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return strings.TrimSpace(string(runes[:low])) + "…"
}

func (s *llmRunStream) failContextCompact(work *contextCompactWork, detail string, retryable bool) error {
	if work == nil {
		return nil
	}
	s.pending = append(s.pending, DeltaContextCompact{
		Status:           "failed",
		RequestID:        work.request.RequestID,
		CompactID:        work.request.CompactID,
		ChatID:           work.request.ChatID,
		RunID:            s.session.RunID,
		Trigger:          work.request.Trigger,
		Level:            work.request.Level,
		Scope:            "run",
		Detail:           detail,
		Retryable:        retryable,
		PreviousRunState: string(work.previousState),
		AwaitingID:       work.awaitingID,
	})
	s.compactWork = nil
	if s.runControl != nil {
		s.runControl.TransitionState(work.previousState)
	}
	if detail == "context_window_uncompactable" {
		s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": detail, "message": "Context cannot be reduced below the model window"}})
		s.closeSteersAndFinish()
	}
	return nil
}

func (s *llmRunStream) currentCompactAwaitingID() string {
	if s == nil {
		return ""
	}
	if value := strings.TrimSpace(s.hitlAwaitingID); value != "" {
		return value
	}
	if s.hitlPendingBatch != nil {
		return strings.TrimSpace(s.hitlPendingBatch.awaitingID)
	}
	if s.execCtx != nil && s.execCtx.RunLoopState == RunLoopStateWaitingSubmit {
		return strings.TrimSpace(s.execCtx.CurrentToolID)
	}
	return ""
}

func (s *llmRunStream) generateContextCompactSummary(request CompactControlRequest, prompt string) (string, map[string]any, error) {
	summaryReq := api.QueryRequest{
		RequestID: request.RequestID,
		RunID:     request.CompactID,
		ChatID:    request.ChatID,
		AgentKey:  s.session.AgentKey,
		TeamID:    s.session.TeamID,
		Role:      api.QueryRoleSystem,
		Message:   prompt,
	}
	summarySession := s.session
	summarySession.RequestID = request.RequestID
	summarySession.RunID = request.CompactID
	summarySession.Mode = "ONESHOT"
	summarySession.SubTaskID = ""
	summarySession.ToolNames = nil
	summarySession.ModeToolDefinitions = nil
	summarySession.HistoryMessages = nil
	summarySession.CurrentMessages = []map[string]any{{"role": "user", "content": prompt}}
	summarySession.StableMemoryContext = ""
	summarySession.SessionMemoryContext = ""
	summarySession.ObservationContext = ""
	summarySession.MemoryUsageSummary = nil
	summarySession.RunLimits = RunLimits{}
	summarySession.ResolvedBudget = NormalizeBudget(Budget{MaxSteps: 1})
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, summaryReq, summarySession, false, runStreamOptions{
		Stage:                        "summary",
		MaxSteps:                     1,
		Messages:                     []openAIMessage{{Role: "user", Content: prompt}},
		PreserveProvidedSystemPrompt: true,
		DisableContextCompaction:     true,
		DisableRunControl:            true,
	})
	if err != nil {
		return "", nil, err
	}
	defer stream.Close()
	var output strings.Builder
	usage := map[string]any{}
	for {
		delta, nextErr := stream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return strings.TrimSpace(output.String()), usage, nextErr
		}
		switch value := delta.(type) {
		case DeltaContent:
			output.WriteString(value.Text)
		case DeltaUsageSnapshot:
			usage = map[string]any{
				"promptTokens":          value.LLMReturnPromptTokens,
				"completionTokens":      value.LLMReturnCompletionTokens,
				"totalTokens":           value.LLMReturnTotalTokens,
				"reasoningTokens":       value.LLMReturnReasoningTokens,
				"promptCacheHitTokens":  value.LLMReturnPromptCacheHitTokens,
				"promptCacheMissTokens": value.LLMReturnPromptCacheMissTokens,
			}
		case DeltaError:
			return strings.TrimSpace(output.String()), usage, fmt.Errorf("compact summary model error: %v", value.Error)
		}
	}
	return strings.TrimSpace(output.String()), usage, nil
}

func (s *llmRunStream) resetContextEstimateAfterCompact() {
	s.lastCallPromptTokens = 0
	s.lastCallCompletionTokens = 0
	s.lastCallTotalTokens = 0
	s.lastCallCachedTokens = 0
	s.lastCallReasoningTokens = 0
	s.lastCallPromptCacheHitTokens = 0
	s.lastCallPromptCacheMissTokens = 0
	s.lastCallLLMChatCompletionCount = 0
	s.lastCallToolCallCount = 0
	s.lastCallFirstTokenLatencyMs = 0
	s.lastCallGenerationDurationMs = 0
}

func estimateModelContext(messages []openAIMessage, tools []openAIToolSpec) int {
	total := 0
	for _, message := range messages {
		raw, _ := json.Marshal(message)
		total += len(raw)
	}
	if len(tools) > 0 {
		raw, _ := json.Marshal(tools)
		total += len(raw) / 2
	}
	return total / 4
}

func cloneModelMessages(messages []openAIMessage) []openAIMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]openAIMessage, len(messages))
	copy(out, messages)
	for i := range out {
		out[i].ToolCalls = append([]ModelToolCall(nil), messages[i].ToolCalls...)
	}
	return out
}

func modelMessagesToMaps(messages []openAIMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			continue
		}
		var mapped map[string]any
		if json.Unmarshal(raw, &mapped) == nil && len(mapped) > 0 {
			out = append(out, mapped)
		}
	}
	return out
}
