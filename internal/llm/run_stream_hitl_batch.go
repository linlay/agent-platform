package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) approvalHITLResult(invocation *preparedToolInvocation) hitl.InterceptResult {
	if _, plan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
		return fileWriteInterceptResult(*plan)
	}
	if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		return fileAccessInterceptResult(*plan)
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) {
		return fileWriteInterceptResult(*plan)
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return bashSecurityInterceptResult(invocation, review)
	}
	if review := s.lookupBashAccessReview(invocation); review.RequiresApproval() {
		return bashAccessInterceptResult(invocation, review)
	}
	if result := s.lookupWorkspaceHITL(invocation); result.Intercepted {
		return result
	}
	return s.lookupPrecheckedHITL(invocation)
}

func (s *llmRunStream) executeApprovedBashInvocation(invocation *preparedToolInvocation, result hitl.InterceptResult) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_rule_run":
		s.registerRuleWhitelist(result.Rule.RuleKey)
		if review := s.rawBashAccessReview(invocation); review.RequiresApproval() {
			accesspolicy.RegisterExactApproval(s.execCtx, review.Fingerprint)
		}
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	case "approve":
		if review := s.rawBashAccessReview(invocation); review.RequiresApproval() {
			accesspolicy.RegisterExactApproval(s.execCtx, review.Fingerprint)
		}
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.executeOriginalBash(invocation)
	}
}

func (s *llmRunStream) shouldAutoApproveHITL(result hitl.InterceptResult) bool {
	if s.execCtx == nil || !strings.EqualFold(result.Rule.ViewportType, "builtin") {
		return false
	}
	if len(s.execCtx.AutoApproveLevels) == 0 {
		return false
	}
	return s.execCtx.AutoApproveLevels[result.Rule.Level]
}

func (s *llmRunStream) emitHITLConfirmDeltas(invocation *preparedToolInvocation, result hitl.InterceptResult) error {
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildHITLArgs(invocation, result)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args, result.Rule.TimeoutMs))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	s.execCtx.CurrentToolID = ""
	s.execCtx.CurrentToolName = ""
	return nil
}

func (s *llmRunStream) prepareQueuedBashApprovalBatch() bool {
	if len(s.queuedToolCalls) == 0 || s.hitlPendingBatch != nil || s.hitlPendingCall != nil {
		return false
	}

	approvals := make([]any, 0)
	invocations := make([]*preparedToolInvocation, 0)
	for _, invocation := range s.queuedToolCalls {
		if !isBashTool(invocation.toolName) {
			continue
		}
		if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
			if s.isRuleWhitelisted(review.RuleKey) {
				s.applyHITLDecision(invocation, bashSecurityInterceptResult(invocation, review), "", "approve_rule_run", "", true)
				continue
			}
			if s.shouldAutoApproveBashSecurity(review) || s.hasBashSecurityApproval(review.Fingerprint) {
				continue
			}
			approvals = append(approvals, s.buildApprovalAskItem(invocation))
			invocations = append(invocations, invocation)
			continue
		}
		if review := s.lookupBashAccessReview(invocation); review.RequiresApproval() {
			if s.isRuleWhitelisted(review.RuleKey) {
				s.applyHITLDecision(invocation, bashAccessInterceptResult(invocation, review), "", "approve_rule_run", "", true)
				accesspolicy.RegisterRuleApproval(s.execCtx, review.RuleKey)
				continue
			}
			if accesspolicy.HasApproval(s.execCtx, review) {
				continue
			}
			approvals = append(approvals, s.buildApprovalAskItem(invocation))
			invocations = append(invocations, invocation)
			continue
		}
		if result := s.lookupWorkspaceHITL(invocation); result.Intercepted {
			if !strings.EqualFold(result.Rule.ViewportType, "builtin") {
				continue
			}
			if s.isRuleWhitelisted(result.Rule.RuleKey) {
				s.applyHITLDecision(invocation, result, "", "approve_rule_run", "", true)
				continue
			}
			if s.shouldAutoApproveHITL(result) {
				continue
			}
			approvals = append(approvals, s.buildApprovalAskItem(invocation))
			invocations = append(invocations, invocation)
			continue
		}
		if s.checker == nil {
			continue
		}
		result := s.lookupPrecheckedHITL(invocation)
		if !result.Intercepted {
			continue
		}
		if !strings.EqualFold(result.Rule.ViewportType, "builtin") {
			continue
		}
		if s.isRuleWhitelisted(result.Rule.RuleKey) {
			s.applyHITLDecision(invocation, result, "", "approve_rule_run", "", true)
			continue
		}
		if s.shouldAutoApproveHITL(result) {
			continue
		}
		approvals = append(approvals, s.buildApprovalAskItem(invocation))
		invocations = append(invocations, invocation)
	}
	if len(invocations) == 0 {
		return false
	}

	awaitingID := buildHITLBatchAwaitingID(s.session.RunID, s.step)
	args := map[string]any{
		"mode":      "approval",
		"approvals": approvals,
	}
	maxRuleTimeout := 0
	for _, invocation := range invocations {
		if invocation == nil || invocation.precheckedHITL == nil {
			continue
		}
		if invocation.precheckedHITL.Rule.TimeoutMs > maxRuleTimeout {
			maxRuleTimeout = invocation.precheckedHITL.Rule.TimeoutMs
		}
	}
	s.hitlPendingBatch = &pendingHITLApprovalBatch{
		awaitingID:  awaitingID,
		awaitArgs:   CloneMap(args),
		invocations: invocations,
		timeoutMs:   maxRuleTimeout,
	}
	awaitDelta := s.buildHITLAwaitDelta(awaitingID, args, maxRuleTimeout)
	s.pending = append(s.pending, awaitDelta)
	if s.runControl != nil {
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	return true
}

func (s *llmRunStream) lookupPrecheckedHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	if invocation == nil || s.checker == nil {
		return hitl.InterceptResult{}
	}
	if invocation.precheckedHITL != nil {
		return *invocation.precheckedHITL
	}
	command := mapStringArg(invocation.args, "command")
	hitlLevel := 0
	if s.execCtx != nil {
		hitlLevel = s.execCtx.HITLLevel
	}
	result := s.checker.Check(command, hitlLevel)
	if result.Intercepted {
		cloned := result
		invocation.precheckedHITL = &cloned
	}
	return result
}

func (s *llmRunStream) lookupWorkspaceHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	return hitl.InterceptResult{}
}

func (s *llmRunStream) awaitHITLApprovalBatchAndContinue() error {
	batch := s.hitlPendingBatch
	if batch == nil || strings.TrimSpace(batch.awaitingID) == "" {
		s.hitlPendingBatch = nil
		return nil
	}
	defer func() {
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(batch.awaitingID)
		}
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		s.hitlPendingBatch = nil
	}()
	if s.runControl == nil {
		return ErrRunControlUnavailable
	}

	s.execCtx.CurrentToolID = batch.awaitingID
	s.execCtx.CurrentToolName = "bash"
	s.execCtx.RunLoopState = RunLoopStateWaitingSubmit
	s.runControl.TransitionState(RunLoopStateWaitingSubmit)

	submitResult, err := s.runControl.AwaitSubmitWithTimeout(
		s.ctx,
		batch.awaitingID,
		time.Duration(s.resolveHITLTimeoutWithRule(batch.timeoutMs))*time.Millisecond,
	)
	if err != nil {
		if errors.Is(err, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     hitlTimeoutAnswer("approval"),
		})
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "timeout", false)
			timeoutResult := hitlTimeoutToolResult(invocation)
			invocation.queuedResult = &timeoutResult
		}
		s.hitlPendingBatch = nil
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	s.runControl.TransitionState(RunLoopStateToolExecuting)
	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: batch.awaitingID,
		Params:     submitResult.Request.Params,
	})

	normalized, normalizeErr := s.normalizeHITLSubmit(batch.awaitArgs, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(batch.awaitArgs["mode"])), "invalid_submit", normalizeErr.Error()),
		})
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", normalizeErr.Error(), false)
			result := frontendSubmitInvalidPayloadResult(invocation, batch.awaitingID, submitResult.Request.Params, normalizeErr)
			invocation.queuedResult = &result
		}
		s.hitlPendingBatch = nil
		return nil
	}
	if len(normalized) > 0 {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     CloneMap(normalized),
		})
	}

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "user_dismissed", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
		s.hitlPendingBatch = nil
		return nil
	}

	approvals, _ := normalized["approvals"].([]map[string]any)
	for index, invocation := range batch.invocations {
		if index >= len(approvals) {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
			continue
		}
		normalizedDecision := strings.TrimSpace(AnyStringNode(approvals[index]["decision"]))
		reason := strings.TrimSpace(AnyStringNode(approvals[index]["reason"]))
		s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, normalizedDecision, reason, normalizedDecision != "reject")
		invocation.approvalDecision = normalizedDecision
		if strings.EqualFold(normalizedDecision, "reject") {
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
	}
	s.hitlPendingBatch = nil
	return nil
}

func (s *llmRunStream) buildHITLNoticeEntry(invocation *preparedToolInvocation) (hitlNoticeEntry, bool) {
	if invocation == nil || invocation.hitlDecision == nil {
		return hitlNoticeEntry{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(invocation.hitlDecision.Mode))
	if mode != "approval" && mode != "form" {
		return hitlNoticeEntry{}, false
	}
	command := ""
	writePlan := s.lookupFileWritePlan(invocation)
	decisionRuleKey := strings.TrimSpace(invocation.hitlDecision.RuleKey)
	if writePlan != nil && (decisionRuleKey == "" || decisionRuleKey == writePlan.RuleKey) {
		command = writePlan.CommandText
	} else if plan := s.lookupFileAccessPlan(invocation); plan != nil {
		command = plan.CommandText
	} else if writePlan != nil {
		command = writePlan.CommandText
	}
	if strings.TrimSpace(command) == "" {
		command = mapStringArg(invocation.args, "command")
	}
	return hitlNoticeEntry{
		toolID:      invocation.toolID,
		toolName:    invocation.toolName,
		command:     command,
		decision:    invocation.hitlDecision.Decision,
		ruleKey:     invocation.hitlDecision.RuleKey,
		reason:      invocation.hitlDecision.Reason,
		mode:        mode,
		formPayload: invocation.hitlDecision.FormPayload,
	}, true
}

func formatHITLFrontendSummary(entries []hitlNoticeEntry) string {
	if len(entries) == 0 {
		return ""
	}
	if len(entries) == 1 {
		return "[HITL] " + formatHITLSummaryLine(entries[0])
	}

	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "[HITL] 审批结果：")
	for index, entry := range entries {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, formatHITLSummaryLine(entry)))
	}
	return strings.Join(lines, "\n")
}

func formatHITLLLMNotice(entries []hitlNoticeEntry) string {
	if len(entries) == 0 {
		return ""
	}
	lines := make([]string, 0, len(entries)*2+3)
	lines = append(lines, "[System audit — HITL approval batch]")
	lines = append(lines, "The user reviewed the following tool call(s) and submitted decisions:")
	for index, entry := range entries {
		lines = append(lines, fmt.Sprintf(
			"%d. tool=%s command=\"%s\" decision=%s reason=\"%s\"",
			index+1,
			formatHITLLLMNoticeValue(entry.toolName, "unknown"),
			escapeHITLLLMQuotedValue(entry.command),
			formatHITLLLMNoticeValue(entry.decision, "unknown"),
			escapeHITLLLMQuotedValue(entry.reason),
		))
		if entry.mode == "form" && entry.formPayload != nil {
			if payloadJSON, err := json.Marshal(entry.formPayload); err == nil {
				payloadKey := "revised_payload"
				if strings.EqualFold(entry.decision, "approve") {
					payloadKey = "submitted_payload"
				}
				lines = append(lines, fmt.Sprintf("   %s=%s", payloadKey, string(payloadJSON)))
			}
		}
	}
	lines = append(lines, "The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.")
	return strings.Join(lines, "\n")
}

func formatHITLLLMNoticeValue(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func escapeHITLLLMQuotedValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\t", `\t`)
	return value
}

func formatHITLSummaryLine(entry hitlNoticeEntry) string {
	if entry.mode == "form" {
		return formatHITLFormSummaryLine(entry)
	}
	line := strings.TrimSpace(entry.command) + " → " + strings.TrimSpace(entry.decision)
	if reason := strings.TrimSpace(entry.reason); reason != "" {
		line += "（" + reason + "）"
	}
	return line
}

func formatHITLFormSummaryLine(entry hitlNoticeEntry) string {
	line := strings.TrimSpace(entry.command) + " → " + strings.TrimSpace(entry.decision)
	if reason := strings.TrimSpace(entry.reason); reason != "" {
		line += "（" + reason + "）"
	}
	if entry.formPayload != nil {
		if payloadJSON, err := json.Marshal(entry.formPayload); err == nil {
			if strings.EqualFold(entry.decision, "approve") {
				line += "\n  提交参数: " + string(payloadJSON)
			} else {
				line += "\n  修改参数: " + string(payloadJSON)
			}
		}
	}
	return line
}

func buildHITLBatchSummaryAndApproval(entries []hitlNoticeEntry) (string, *chat.StepApproval) {
	frontendSummary := formatHITLFrontendSummary(entries)
	llmNotice := formatHITLLLMNotice(entries)
	if frontendSummary == "" || llmNotice == "" {
		return "", nil
	}

	approval := &chat.StepApproval{
		Summary:   frontendSummary,
		LLMNotice: llmNotice,
		Decisions: make([]chat.StepApprovalDecision, 0, len(entries)),
	}
	for _, entry := range entries {
		approval.Decisions = append(approval.Decisions, chat.StepApprovalDecision{
			ToolID:   entry.toolID,
			Command:  entry.command,
			Decision: entry.decision,
			RuleKey:  strings.TrimSpace(entry.ruleKey),
			Reason:   entry.reason,
			Mode:     entry.mode,
			Payload:  entry.formPayload,
		})
	}
	return llmNotice, approval
}

func (s *llmRunStream) applyHITLDecision(invocation *preparedToolInvocation, result hitl.InterceptResult, awaitingID string, decision string, reason string, executed bool) {
	if invocation == nil {
		return
	}
	normalizedDecision := strings.ToLower(strings.TrimSpace(decision))
	if normalizedDecision == "" {
		normalizedDecision = "reject"
	}
	invocation.approvalDecision = normalizedDecision
	invocation.hitlDecision = &hitlDecisionState{
		AwaitingID: strings.TrimSpace(awaitingID),
		Decision:   normalizedDecision,
		Reason:     strings.TrimSpace(reason),
		RuleKey:    strings.TrimSpace(result.Rule.RuleKey),
		Scope:      hitlDecisionScope(normalizedDecision),
		Executed:   executed,
		Mode:       hitlDecisionMode(result),
	}
	if normalizedDecision == "approve_rule_run" {
		s.registerRuleWhitelist(result.Rule.RuleKey)
	}
}

func hitlDecisionScope(decision string) string {
	normalized := strings.ToLower(strings.TrimSpace(decision))
	if normalized == "approve_rule_run" {
		return "run_rule"
	}
	return ""
}

func hitlDecisionMode(result hitl.InterceptResult) string {
	if strings.EqualFold(strings.TrimSpace(result.Rule.ViewportType), "builtin") {
		return "approval"
	}
	return "form"
}

func (s *llmRunStream) isRuleWhitelisted(ruleKey string) bool {
	if strings.TrimSpace(ruleKey) == "" || len(s.hitlRuleWhitelist) == 0 {
		return false
	}
	_, ok := s.hitlRuleWhitelist[strings.TrimSpace(ruleKey)]
	return ok
}

func (s *llmRunStream) registerRuleWhitelist(ruleKey string) {
	ruleKey = strings.TrimSpace(ruleKey)
	if ruleKey == "" {
		return
	}
	if s.hitlRuleWhitelist == nil {
		s.hitlRuleWhitelist = map[string]struct{}{}
	}
	s.hitlRuleWhitelist[ruleKey] = struct{}{}
}
