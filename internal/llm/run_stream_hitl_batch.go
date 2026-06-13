package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

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
	return s.emitApprovalRequestDeltas(hitlApprovalRequest(invocation, result))
}

func (s *llmRunStream) prepareQueuedBashApprovalBatch() bool {
	if len(s.queuedToolCalls) == 0 || s.hitlPendingBatch != nil || s.hitlPendingCall != nil {
		return false
	}

	approvals := make([]any, 0)
	invocations := make([]*preparedToolInvocation, 0)
	matches := make([]hitl.InterceptResult, 0)
	for _, invocation := range s.queuedToolCalls {
		candidate, ok := s.queuedBashApprovalCandidate(invocation)
		if !ok {
			continue
		}
		approvals = append(approvals, s.buildApprovalAskItem(invocation))
		invocations = append(invocations, candidate.invocation)
		matches = append(matches, candidate.match)
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
	for _, match := range matches {
		if match.Rule.Timeout > maxRuleTimeout {
			maxRuleTimeout = match.Rule.Timeout
		}
	}
	s.hitlPendingBatch = &pendingHITLApprovalBatch{
		awaitingID:  awaitingID,
		awaitArgs:   CloneMap(args),
		invocations: invocations,
		matches:     matches,
		timeout:     maxRuleTimeout,
	}
	awaitDelta := s.buildHITLAwaitDelta(awaitingID, args, maxRuleTimeout)
	s.pending = append(s.pending, awaitDelta)
	if s.runControl != nil {
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	return true
}

type queuedBashApprovalCandidate struct {
	invocation *preparedToolInvocation
	match      hitl.InterceptResult
}

func (s *llmRunStream) queuedBashApprovalCandidate(invocation *preparedToolInvocation) (queuedBashApprovalCandidate, bool) {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return queuedBashApprovalCandidate{}, false
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewBlock {
		return s.queuedGenericHITLApprovalCandidate(invocation)
	} else if review.Decision == bashsec.ReviewRequiresApproval {
		request := s.bashSecurityApprovalRequest(invocation, review)
		if handled, _ := s.tryResolveApprovalFastPath(request, approvalFastPathSkipBatch); handled {
			return queuedBashApprovalCandidate{}, false
		}
		return queuedBashApprovalCandidate{invocation: invocation, match: request.result}, true
	}
	if review := s.lookupBashAccessReview(invocation); review.RequiresApproval() {
		request := s.bashAccessApprovalRequest(invocation, review)
		if handled, _ := s.tryResolveApprovalFastPath(request, approvalFastPathSkipBatch); handled {
			return queuedBashApprovalCandidate{}, false
		}
		return queuedBashApprovalCandidate{invocation: invocation, match: request.result}, true
	}
	return s.queuedGenericHITLApprovalCandidate(invocation)
}

func (s *llmRunStream) queuedGenericHITLApprovalCandidate(invocation *preparedToolInvocation) (queuedBashApprovalCandidate, bool) {
	if s.checker == nil {
		return queuedBashApprovalCandidate{}, false
	}
	result := s.lookupPrecheckedHITL(invocation)
	if !result.Intercepted || !strings.EqualFold(result.Rule.ViewportType, "builtin") {
		return queuedBashApprovalCandidate{}, false
	}
	request := hitlApprovalRequest(invocation, result)
	if handled, _ := s.tryResolveHITLApprovalFastPath(request, approvalFastPathSkipBatch); handled {
		return queuedBashApprovalCandidate{}, false
	}
	return queuedBashApprovalCandidate{invocation: invocation, match: result}, true
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

	submitResult, err := s.awaitHITLBatchSubmitOrAccessLevel(batch)
	if err != nil {
		return err
	}
	if submitResult.Status == "access_level_auto_approved" || submitResult.Status == "hitl_timeout" {
		s.hitlPendingBatch = nil
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	s.runControl.TransitionState(RunLoopStateToolExecuting)

	s.appendHITLRequestSubmit(batch.awaitingID, submitResult)
	normalized, normalizeErr := s.normalizeHITLSubmitAndEmitAnswer(batch.awaitingID, batch.awaitArgs, submitResult)
	if normalizeErr != nil {
		for index, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, "reject", normalizeErr.Error(), false)
			result := frontendSubmitInvalidPayloadResult(invocation, batch.awaitingID, submitResult.Request.Params, normalizeErr)
			invocation.queuedResult = &result
		}
		s.hitlPendingBatch = nil
		return nil
	}

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		for index, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, "reject", "user_dismissed", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
		s.hitlPendingBatch = nil
		return nil
	}

	approvals, _ := normalized["approvals"].([]map[string]any)
	for index, invocation := range batch.invocations {
		if index >= len(approvals) {
			s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, "reject", "", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
			continue
		}
		normalizedDecision := strings.TrimSpace(AnyStringNode(approvals[index]["decision"]))
		reason := strings.TrimSpace(AnyStringNode(approvals[index]["reason"]))
		s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, normalizedDecision, reason, normalizedDecision != "reject")
		invocation.approvalDecision = normalizedDecision
		if strings.EqualFold(normalizedDecision, "reject") {
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
	}
	s.hitlPendingBatch = nil
	return nil
}

func (s *llmRunStream) awaitHITLBatchSubmitOrAccessLevel(batch *pendingHITLApprovalBatch) (SubmitResult, error) {
	return s.awaitHITLSubmitOrAccessLevelChange(hitlSubmitWaitConfig{
		awaitingID:  batch.awaitingID,
		mode:        "approval",
		ruleTimeout: int64(batch.timeout),
		onAccessLevelChange: func() (bool, error) {
			return s.tryResolvePendingAccessLevelBatch(batch)
		},
		onTimeout: func() {
			s.timeoutHITLBatch(batch)
		},
	})
}

func (s *llmRunStream) timeoutHITLBatch(batch *pendingHITLApprovalBatch) {
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: batch.awaitingID,
		Answer:     hitlTimeoutAnswer("approval"),
	})
	for index, invocation := range batch.invocations {
		s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, "reject", "timeout", false)
		timeoutResult := hitlTimeoutToolResult(invocation)
		invocation.queuedResult = &timeoutResult
	}
	s.hitlPendingBatch = nil
}

func (s *llmRunStream) tryResolvePendingAccessLevelBatch(batch *pendingHITLApprovalBatch) (bool, error) {
	if batch == nil || len(batch.invocations) == 0 {
		return false, nil
	}
	entries := make([]map[string]any, 0, len(batch.invocations))
	for index, invocation := range batch.invocations {
		match := batch.matchAt(index)
		if !isAccessPolicyApprovalMatch(match) {
			return false, nil
		}
		s.refreshAccessLevelForInvocation(invocation)
		if s.invocationNeedsAccessPolicyApproval(invocation) {
			return false, nil
		}
		item := s.buildApprovalAskItem(invocation)
		command := accessLevelApprovalCommand(s, invocation, match)
		if strings.TrimSpace(command) == "" {
			command = AnyStringNode(item["command"])
		}
		entries = append(entries, map[string]any{
			"id":       approvalAnswerID(batch.awaitingID, invocation),
			"command":  command,
			"decision": "auto_approved",
			"reason":   "accessLevel=" + s.currentAccessLevel(),
		})
	}
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: batch.awaitingID,
		Answer:     accessLevelAutoApprovalBatchAnswer(batch, entries),
	})
	for index, invocation := range batch.invocations {
		s.applyHITLDecision(invocation, batch.matchAt(index), batch.awaitingID, "auto_approved", "accessLevel="+s.currentAccessLevel(), true)
	}
	return true, nil
}

func (batch *pendingHITLApprovalBatch) matchAt(index int) hitl.InterceptResult {
	if batch == nil || index < 0 || index >= len(batch.matches) {
		return hitl.InterceptResult{}
	}
	return batch.matches[index]
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
		command = s.fileToolApprovalDisplayCommand(invocation, nil, writePlan)
	} else if plan := s.lookupFileAccessPlan(invocation); plan != nil {
		command = s.fileToolApprovalDisplayCommand(invocation, plan, nil)
	} else if writePlan != nil {
		command = s.fileToolApprovalDisplayCommand(invocation, nil, writePlan)
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
	if allHITLNoticeEntriesAutoApproved(entries) {
		if len(entries) == 1 {
			return "[AUTO] " + formatHITLSummaryLine(entries[0])
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, "[AUTO] 自动审批结果：")
		for index, entry := range entries {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, formatHITLSummaryLine(entry)))
		}
		return strings.Join(lines, "\n")
	}
	if anyHITLNoticeEntryAutoApproved(entries) {
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, "[Approval] 审批结果：")
		for index, entry := range entries {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, formatHITLSummaryLine(entry)))
		}
		return strings.Join(lines, "\n")
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
	allAutoApproved := allHITLNoticeEntriesAutoApproved(entries)
	anyAutoApproved := anyHITLNoticeEntryAutoApproved(entries)
	switch {
	case allAutoApproved:
		lines = append(lines, "[System audit — auto approval]")
		if allHITLNoticeEntriesAutoApprovedByAccessLevel(entries) {
			lines = append(lines, "The system auto-approved the following tool call(s) because accessLevel=auto_approve applies automatic approval to reviewable access-policy checks:")
		} else {
			lines = append(lines, "The system auto-approved the following tool call(s) according to configured automatic approval policy:")
		}
	case anyAutoApproved:
		lines = append(lines, "[System audit — approval batch]")
		lines = append(lines, "The following tool call approval decisions were applied:")
	default:
		lines = append(lines, "[System audit — HITL approval batch]")
		lines = append(lines, "The user reviewed the following tool call(s) and submitted decisions:")
	}
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
	if allAutoApproved {
		lines = append(lines, "The tool results above already reflect these automatic approvals; do not re-prompt for approval.")
	} else {
		lines = append(lines, "The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.")
	}
	lines = append(lines, "Approval decisions only record authorization/review decisions and do not mean tool execution succeeded; inspect each tool result's error and exitCode before claiming success.")
	return strings.Join(lines, "\n")
}

func allHITLNoticeEntriesAutoApproved(entries []hitlNoticeEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !isAutoApprovedHITLNoticeEntry(entry) {
			return false
		}
	}
	return true
}

func allHITLNoticeEntriesAutoApprovedByAccessLevel(entries []hitlNoticeEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.decision), "auto_approved") {
			return false
		}
		if !strings.HasPrefix(strings.TrimSpace(entry.reason), "accessLevel=auto_approve") {
			return false
		}
	}
	return true
}

func anyHITLNoticeEntryAutoApproved(entries []hitlNoticeEntry) bool {
	for _, entry := range entries {
		if isAutoApprovedHITLNoticeEntry(entry) {
			return true
		}
	}
	return false
}

func isAutoApprovedHITLNoticeEntry(entry hitlNoticeEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.decision), "auto_approved")
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
		Notice:    llmNotice,
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
