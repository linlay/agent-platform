package llm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/compaction"
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
	cycleID       string
	forceSummary  bool
}

type contextCompactPlan struct {
	system     []openAIMessage
	pinned     []openAIMessage
	retained   []openAIMessage
	candidates []openAIMessage
}

func compactTriggerThreshold(contextWindow int) int {
	return (contextWindow*compaction.ToolsPercent + 99) / 100
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
	preTokens := s.estimatedNextCallSize()
	if raw := estimateModelContext(s.messages, s.toolSpecs); raw > 0 && preTokens > raw {
		s.compactEstimateScale = max(s.compactEstimateScale, float64(preTokens)/float64(raw))
	}
	if !manual {
		threshold := compactTriggerThreshold(s.effectiveContextWindow())
		triggerTokens := s.estimatedNextCallSize()
		if !s.forceContextCompact && (threshold <= 0 || triggerTokens < threshold) {
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
		fingerprint := s.compactToolsFingerprint()
		if manual || fingerprint != s.lastNoopToolsFingerprint {
			toolMessages, toolsCleared, toolsKept = s.compactRunToolMessages(
				chat.DefaultToolCompactKeepRecent,
				func() int {
					if manual {
						return 0
					}
					return compaction.Target(s.effectiveContextWindow())
				}(),
			)
			if toolsCleared == 0 && !manual {
				s.lastNoopToolsFingerprint = fingerprint
			}
		}
		if toolsCleared == 0 && !manual {
			if !s.forceContextCompact && !compaction.Reached(preTokens, s.effectiveContextWindow(), compaction.SummaryPercent) {
				return false
			}
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
			return s.scheduleContextCompact(finishAfter)
		}
		if s.forceContextCompact || (!manual && compaction.Reached(preTokens, s.effectiveContextWindow(), compaction.SummaryPercent)) {
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
		if manual {
			previousState = s.runControl.State()
			s.runControl.TransitionState(RunLoopStateCompacting)
		} else {
			var claimed bool
			previousState, claimed = s.runControl.BeginAutomaticCompact()
			if !claimed {
				if s.runControl.HasUnclaimedCompact() && !s.runControl.Interrupted() {
					return s.scheduleContextCompact(finishAfter)
				}
				return false
			}
		}
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
		forceSummary:  s.forceContextCompact,
	}
	if !manual {
		s.compactWork.cycleID = request.CompactID
	}
	s.pending = append(s.pending, DeltaContextCompact{
		CycleID:          s.compactWork.cycleID,
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
	for _, group := range compactMessageGroups(history) {
		if compactMessageGroupComplete(group) {
			plan.candidates = append(plan.candidates, group...)
		} else {
			plan.retained = append(plan.retained, group...)
		}
	}
	for i := 0; i < keepFrom; i++ {
		if compactMessageGroupComplete(groups[i]) {
			plan.candidates = append(plan.candidates, groups[i]...)
		} else {
			plan.retained = append(plan.retained, groups[i]...)
		}
	}
	for i := keepFrom; i < len(groups); i++ {
		plan.retained = append(plan.retained, groups[i]...)
	}
	return plan
}

func compactMessageGroupComplete(group []openAIMessage) bool {
	if len(group) > 0 && strings.EqualFold(group[0].Role, "tool") {
		// An orphan result is never independently summarized away from its call.
		return false
	}
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
			for i < len(messages) && strings.EqualFold(messages[i].Role, "tool") && ids[strings.TrimSpace(messages[i].ToolCallID)] && messages[i].OriginRunID == message.OriginRunID && messages[i].OriginActor == message.OriginActor {
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
	baseMessages := append(cloneModelMessages(work.plan.system), work.plan.pinned...)
	baseMessages = append(baseMessages, work.plan.retained...)
	maxInputTokens, maxOutputTokens := compaction.SummaryBudget(s.effectiveContextWindow(), s.estimateCompactContext(baseMessages))
	if maxOutputTokens <= 0 {
		return s.failContextCompact(work, "context_window_uncompactable", false)
	}
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
	modelSummary, modelUsage, modelErr := s.generateContextCompactSummaryWithBudget(work.request, prompt, maxOutputTokens)
	if len(modelUsage) > 0 {
		usage = modelUsage
	}
	if modelErr != nil {
		if errors.Is(modelErr, chat.ErrCompactSummaryInputTooLarge) {
			return s.failContextCompact(work, "summary_input_too_large", false)
		}
		return s.failContextCompact(work, "summary_model_failed", true)
	}
	summary = strings.TrimSpace(modelSummary)
	if summary == "" || summary == "Model returned no assistant content." {
		return s.failContextCompact(work, "summary_empty", true)
	}
	targetTokens := s.effectiveContextWindow() * runCompactTargetPercent / 100

	summaryMessage := openAIMessage{Role: "user", Content: chat.CompactCheckpointSummaryMessage(summary)}
	newMessages := make([]openAIMessage, 0, len(work.plan.system)+1+len(work.plan.pinned)+len(work.plan.retained))
	newMessages = append(newMessages, work.plan.system...)
	newMessages = append(newMessages, summaryMessage)
	newPinnedStart := len(newMessages)
	newMessages = append(newMessages, work.plan.pinned...)
	newPinnedEnd := len(newMessages)
	newMessages = append(newMessages, work.plan.retained...)
	postTokens := s.estimateCompactContext(newMessages)
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
	checkpointMessages := s.checkpointMessages(newMessages[len(work.plan.system):])
	s.pending = append(s.pending, DeltaContextCompact{
		CycleID:                    work.cycleID,
		CycleComplete:              true,
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
		if work.request.Trigger == "manual" {
			s.compactFinishPending = true
		} else {
			s.closeSteersAndFinish()
		}
	}
	return nil
}

func (s *llmRunStream) executeToolContextCompact(work *contextCompactWork) error {
	if work == nil || work.toolsCleared == 0 || len(work.toolMessages) == 0 {
		return s.failContextCompact(work, "no_compactable_tools", false)
	}
	postTokens := s.estimateCompactContext(work.toolMessages)
	if postTokens >= work.preTokens {
		return s.failContextCompact(work, "no_compactable_tools", false)
	}
	needsSummary := work.request.Trigger == "auto" && (work.forceSummary || compaction.Reached(postTokens, s.effectiveContextWindow(), compaction.SummaryPercent))
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
	checkpointMessages := s.checkpointMessages(s.messages[systemEnd:])
	s.pending = append(s.pending, DeltaContextCompact{
		CycleID:                    work.cycleID,
		CycleComplete:              !needsSummary,
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
	if needsSummary {
		request := s.nextAutomaticCompactRequest("summary")
		plan := s.buildContextCompactPlan(false)
		if len(plan.candidates) == 0 {
			s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": "context_window_uncompactable", "message": "Context cannot be reduced below the model window"}})
			s.closeSteersAndFinish()
			return nil
		}
		s.compactWork = &contextCompactWork{
			request: request, previousState: previousState, finishAfter: finishAfter,
			preTokens: postTokens, plan: plan, awaitingID: awaitingID, cycleID: work.cycleID,
		}
		s.pending = append(s.pending, DeltaContextCompact{
			CycleID: work.cycleID, Status: "start", RequestID: request.RequestID, CompactID: request.CompactID,
			ChatID: request.ChatID, RunID: s.session.RunID, Trigger: "auto",
			Level: "summary", Scope: "run", PreviousRunState: string(previousState), AwaitingID: awaitingID,
		})
		return nil
	}
	s.restoreContextCompactState(work)
	if finishAfter {
		if work.request.Trigger == "manual" {
			s.compactFinishPending = true
		} else {
			s.closeSteersAndFinish()
		}
	}
	return nil
}

func (s *llmRunStream) compactRunToolMessages(keepRecent, targetTokens int) ([]openAIMessage, int, int) {
	if s == nil {
		return nil, 0, 0
	}
	raw := modelMessagesToMaps(s.messages)
	if targetTokens > 0 {
		targetTokens = max(1, targetTokens-estimateModelContext(nil, s.toolSpecs))
	}
	projected, cleared, kept := chat.CompactToolMessages(raw, keepRecent, targetTokens, s.pinnedMessageStart, s.pinnedMessageEnd)
	// Keep all ordinary content and protocol fields byte-for-byte in the model
	// representation; only copy the two fields L1 is allowed to change.
	out := cloneModelMessages(s.messages)
	for i := range out {
		if out[i].Role == "tool" {
			if fmt.Sprint(projected[i]["content"]) != fmt.Sprint(raw[i]["content"]) {
				parts, _ := projected[i]["content"].([]any)
				if len(parts) == 1 {
					part, _ := parts[0].(map[string]any)
					if text, ok := part["text"].(string); ok {
						out[i].Content = text
					}
				}
			}
		}
		if out[i].Role == "assistant" {
			calls, _ := projected[i]["tool_calls"].([]any)
			for j, call := range calls {
				mapped, _ := call.(map[string]any)
				fn, _ := mapped["function"].(map[string]any)
				if args, ok := fn["arguments"].(string); ok {
					out[i].ToolCalls[j].Function.Arguments = args
				}
			}
		}
	}
	return out, cleared, kept
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

func (s *llmRunStream) failContextCompact(work *contextCompactWork, detail string, retryable bool) error {
	if work == nil {
		return nil
	}
	s.pending = append(s.pending, DeltaContextCompact{
		CycleID:          work.cycleID,
		CycleComplete:    true,
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
	if work.request.Trigger == "auto" {
		code := detail
		if detail == "summary_input_too_large" || detail == "summary_model_failed" || detail == "summary_empty" {
			code = "context_window_uncompactable"
		}
		s.pending = append(s.pending, DeltaError{Error: map[string]any{"code": code, "message": "Context cannot be reduced below the model window"}})
		s.closeSteersAndFinish()
	} else if work.finishAfter {
		s.compactFinishPending = true
	}
	return nil
}

func (s *llmRunStream) compactToolsFingerprint() string {
	var tools []openAIMessage
	for _, message := range s.messages {
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" {
			tools = append(tools, message)
		}
	}
	encoded, _ := json.Marshal(tools)
	return fmt.Sprintf("%s:%d:%d:%d:%x", s.model.Key, s.effectiveContextWindow(), s.pinnedMessageStart, s.pinnedMessageEnd, sha256.Sum256(encoded))
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

func (s *llmRunStream) generateContextCompactSummaryWithBudget(request CompactControlRequest, prompt string, maxOutputTokens int) (string, map[string]any, error) {
	summaryReq := api.QueryRequest{
		RequestID: request.RequestID,
		RunID:     request.CompactID,
		ChatID:    request.ChatID,
		AgentKey:  s.session.AgentKey,
		TeamID:    s.session.TeamID,
		Role:      api.QueryRoleSystem,
		Message:   prompt,
	}
	stream, err := s.engine.StreamSummary(s.ctx, summaryReq, s.session, prompt, maxOutputTokens)
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
	total := chat.EstimateRawMessageTokens(modelMessagesToMaps(messages))
	if len(tools) > 0 {
		raw, _ := json.Marshal(tools)
		total += chat.EstimateTextTokens(string(raw))
	}
	return total
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
			if message.OriginRunID != "" {
				mapped["runId"] = message.OriginRunID
			}
			if message.OriginActor != "" {
				mapped["agentKey"] = message.OriginActor
			}
			out = append(out, mapped)
		}
	}
	return out
}

func (s *llmRunStream) estimateCompactContext(messages []openAIMessage) int {
	raw := estimateModelContext(messages, s.toolSpecs)
	scale := max(1.0, s.compactEstimateScale)
	return int(float64(raw)*scale + 0.999999)
}

func (s *llmRunStream) checkpointMessages(messages []openAIMessage) []map[string]any {
	raw := modelMessagesToMaps(messages)
	for _, message := range raw {
		if message["runId"] == nil {
			message["runId"] = s.session.RunID
		}
	}
	return raw
}
