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
	toolMessages  []openAIMessage
	toolsCleared  int
	toolsKept     int
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
			Level:     "l1_tools",
		}
	}
	request.Level = strings.ToLower(strings.TrimSpace(request.Level))
	if request.Level == "" {
		request.Level = "summary"
	}
	plan := contextCompactPlan{}
	toolMessages := []openAIMessage(nil)
	toolsCleared, toolsKept := 0, 0
	if request.Level == "l1_tools" {
		toolMessages, toolsCleared, toolsKept = s.compactRunToolMessages(
			chat.DefaultToolCompactKeepRecent,
			s.effectiveContextWindow()*runCompactTargetPercent/100,
		)
		if toolsCleared == 0 && !manual {
			request = s.nextAutomaticCompactRequest("summary")
		}
	}
	if request.Level == "summary" {
		plan = s.buildContextCompactPlan(manual)
	}
	if (request.Level == "l1_tools" && toolsCleared == 0) || (request.Level == "summary" && len(plan.candidates) == 0) {
		if manual && s.runControl != nil {
			detail := "no_compactable_history"
			if request.Level == "l1_tools" {
				detail = "no_compactable_tools"
			}
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
				Detail:    detail,
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
		toolMessages:  toolMessages,
		toolsCleared:  toolsCleared,
		toolsKept:     toolsKept,
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

func (s *llmRunStream) nextAutomaticCompactRequest(level string) CompactControlRequest {
	s.compactCounter++
	return CompactControlRequest{
		RequestID: fmt.Sprintf("auto_%s_%d", s.session.RunID, s.compactCounter),
		CompactID: fmt.Sprintf("compact_%s_%d", s.session.RunID, s.compactCounter),
		ChatID:    s.session.ChatID,
		Trigger:   "auto",
		Level:     level,
	}
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
	if work.request.Level == "l1_tools" {
		return s.executeToolContextCompact(work)
	}
	rawCandidates := modelMessagesToMaps(work.plan.candidates)
	maxInputTokens := compactTriggerThreshold(s.effectiveContextWindow())
	prompt, err := chat.BuildCompactPromptWithinBudget(rawCandidates, maxInputTokens)
	if errors.Is(err, chat.ErrCompactSummaryInputTooLarge) {
		return s.failContextCompact(work, "summary_input_too_large", false)
	}
	if err != nil {
		return s.failContextCompact(work, "summary_model_failed", true)
	}
	if strings.TrimSpace(prompt) == "" {
		return s.failContextCompact(work, "no_compactable_history", false)
	}
	summary := ""
	summarySource := "model"
	usage := map[string]any{}
	detail := "completed"
	modelSummary, modelUsage, modelErr := s.generateContextCompactSummary(work.request, prompt)
	if len(modelUsage) > 0 {
		usage = modelUsage
	}
	if modelErr != nil {
		return s.failContextCompact(work, "summary_model_failed", true)
	}
	summary = strings.TrimSpace(modelSummary)
	if summary == "" || summary == "Model returned no assistant content." {
		return s.failContextCompact(work, "summary_empty", true)
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
	remainingRatio, releasedRatio := compactPercentages(ratio)
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
		RemainingRatio:             remainingRatio,
		ReleasedRatio:              releasedRatio,
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

func (s *llmRunStream) executeToolContextCompact(work *contextCompactWork) error {
	if work == nil || work.toolsCleared == 0 || len(work.toolMessages) == 0 {
		return s.failContextCompact(work, "no_compactable_tools", false)
	}
	postTokens := estimateModelContext(work.toolMessages, s.toolSpecs)
	if postTokens >= work.preTokens {
		return s.failContextCompact(work, "no_compactable_tools", false)
	}
	s.messages = cloneModelMessages(work.toolMessages)
	s.forceContextCompact = false
	s.resetContextEstimateAfterCompact()
	ratio := 0.0
	if work.preTokens > 0 {
		ratio = float64(postTokens) / float64(work.preTokens)
	}
	remainingRatio, releasedRatio := compactPercentages(ratio)
	systemEnd := 0
	for systemEnd < len(s.messages) && strings.EqualFold(strings.TrimSpace(s.messages[systemEnd].Role), "system") {
		systemEnd++
	}
	checkpointMessages := modelMessagesToMaps(s.messages[systemEnd:])
	s.pending = append(s.pending, DeltaContextCompact{
		Status:                     "complete",
		RequestID:                  work.request.RequestID,
		CompactID:                  work.request.CompactID,
		ChatID:                     work.request.ChatID,
		RunID:                      s.session.RunID,
		Trigger:                    work.request.Trigger,
		Level:                      "l1_tools",
		Scope:                      "run",
		PreCompactEstimatedTokens:  work.preTokens,
		PostCompactEstimatedTokens: postTokens,
		CompressionRatio:           ratio,
		RemainingRatio:             remainingRatio,
		ReleasedRatio:              releasedRatio,
		TokensFreed:                max(work.preTokens-postTokens, 0),
		ToolsCleared:               work.toolsCleared,
		ToolsKept:                  work.toolsKept,
		Detail:                     "completed",
		CheckpointMessages:         checkpointMessages,
		PreviousRunState:           string(work.previousState),
		AwaitingID:                 work.awaitingID,
	})
	finishAfter := work.finishAfter
	previousState := work.previousState
	awaitingID := work.awaitingID
	s.compactWork = nil
	targetTokens := s.effectiveContextWindow() * runCompactTargetPercent / 100
	if work.request.Trigger == "auto" && postTokens > targetTokens {
		request := s.nextAutomaticCompactRequest("summary")
		plan := s.buildContextCompactPlan(false)
		if len(plan.candidates) == 0 {
			s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": "context_window_uncompactable", "message": "Context cannot be reduced below the model window"}})
			s.closeSteersAndFinish()
			return nil
		}
		s.compactWork = &contextCompactWork{
			request: request, previousState: previousState, finishAfter: finishAfter,
			preTokens: postTokens, plan: plan, awaitingID: awaitingID,
		}
		s.pending = append(s.pending, DeltaContextCompact{
			Status: "start", RequestID: request.RequestID, CompactID: request.CompactID,
			ChatID: request.ChatID, RunID: s.session.RunID, Trigger: "auto",
			Level: "summary", Scope: "run", PreviousRunState: string(previousState), AwaitingID: awaitingID,
		})
		return nil
	}
	s.restoreContextCompactState(work)
	if finishAfter {
		s.closeSteers()
		s.finished = true
	}
	return nil
}

type runToolCompactCandidate struct {
	messageIndex int
	callIndex    int
	toolID       string
	toolName     string
	arguments    string
	resultIndex  int
}

func (s *llmRunStream) compactRunToolMessages(keepRecent, targetTokens int) ([]openAIMessage, int, int) {
	if s == nil || len(s.messages) == 0 {
		return nil, 0, 0
	}
	callByID := map[string]runToolCompactCandidate{}
	siblingsByID := map[string][]string{}
	resultIDs := map[string]bool{}
	for messageIndex, message := range s.messages {
		if strings.EqualFold(message.Role, "assistant") && len(message.ToolCalls) > 0 {
			siblings := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if id := strings.TrimSpace(call.ID); id != "" {
					siblings = append(siblings, id)
				}
			}
			for callIndex, call := range message.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					continue
				}
				callByID[id] = runToolCompactCandidate{
					messageIndex: messageIndex, callIndex: callIndex, toolID: id,
					toolName: strings.TrimSpace(call.Function.Name), arguments: call.Function.Arguments,
				}
				siblingsByID[id] = append([]string(nil), siblings...)
			}
		}
		if strings.EqualFold(message.Role, "tool") {
			if id := strings.TrimSpace(message.ToolCallID); id != "" {
				resultIDs[id] = true
			}
		}
	}
	candidates := make([]runToolCompactCandidate, 0)
	for resultIndex, message := range s.messages {
		if !strings.EqualFold(message.Role, "tool") || runCompactIndexPinned(resultIndex, s.pinnedMessageStart, s.pinnedMessageEnd) {
			continue
		}
		id := strings.TrimSpace(message.ToolCallID)
		call, ok := callByID[id]
		if !ok || runCompactIndexPinned(call.messageIndex, s.pinnedMessageStart, s.pinnedMessageEnd) || !chat.ToolCompactable(call.toolName) {
			continue
		}
		complete := true
		for _, siblingID := range siblingsByID[id] {
			if !resultIDs[siblingID] {
				complete = false
				break
			}
		}
		content := strings.TrimSpace(fmt.Sprint(message.Content))
		if !complete || content == "" || content == chat.ToolCompactClearedMessage || strings.HasPrefix(content, "[Compacted tool interaction]") {
			continue
		}
		call.resultIndex = resultIndex
		candidates = append(candidates, call)
	}
	if len(candidates) == 0 {
		return nil, 0, 0
	}
	preferredCount := len(candidates) - keepRecent
	if preferredCount < 0 {
		preferredCount = 0
	}
	out := cloneModelMessages(s.messages)
	cleared := 0
	for index, candidate := range candidates {
		if index >= preferredCount && targetTokens > 0 && estimateModelContext(out, s.toolSpecs) <= targetTokens {
			break
		}
		originalContent := out[candidate.resultIndex].Content
		digest := chat.ToolCompactDigest(candidate.toolName, candidate.toolID, originalContent)
		arguments := chat.CompactToolArguments(candidate.arguments)
		originalCost := chat.EstimateTextTokens(fmt.Sprint(originalContent)) + chat.EstimateTextTokens(candidate.arguments)
		compactCost := chat.EstimateTextTokens(digest) + chat.EstimateTextTokens(arguments)
		if compactCost >= originalCost {
			continue
		}
		out[candidate.resultIndex].Content = digest
		out[candidate.messageIndex].ToolCalls[candidate.callIndex].Function.Arguments = arguments
		cleared++
	}
	if cleared == 0 {
		return nil, 0, len(candidates)
	}
	return out, cleared, len(candidates) - cleared
}

func runCompactIndexPinned(index, start, end int) bool {
	return start >= 0 && end >= start && index >= start && index < end
}

func compactPercentages(ratio float64) (float64, float64) {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	remaining := ratio * 100
	return remaining, 100 - remaining
}

func (s *llmRunStream) restoreContextCompactState(work *contextCompactWork) {
	if work == nil {
		return
	}
	if s.runControl != nil {
		s.runControl.TransitionState(work.previousState)
	}
	if s.execCtx != nil {
		s.execCtx.RunLoopState = work.previousState
	}
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
	s.restoreContextCompactState(work)
	if work.request.Trigger == "auto" || detail == "context_window_uncompactable" {
		code := detail
		if detail == "summary_input_too_large" || detail == "summary_model_failed" || detail == "summary_empty" {
			code = "context_window_uncompactable"
		}
		s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": code, "message": "Context cannot be reduced below the model window"}})
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
