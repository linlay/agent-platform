package llm

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
	"agent-platform/internal/stream"
)

func (s *llmRunStream) awaitHITLSubmitAndExecute() error {
	invocation := s.hitlPendingCall
	match := s.hitlMatch
	awaitingID := s.hitlAwaitingID
	awaitArgs := CloneMap(s.hitlAwaitArgs)
	if invocation == nil || match == nil || awaitingID == "" {
		s.hitlPendingCall = nil
		s.hitlMatch = nil
		s.hitlAwaitingID = ""
		s.hitlAwaitArgs = nil
		return nil
	}
	defer func() {
		s.hitlPendingCall = nil
		s.hitlMatch = nil
		s.hitlAwaitingID = ""
		s.hitlAwaitArgs = nil
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(awaitingID)
		}
	}()
	if s.runControl == nil {
		return ErrRunControlUnavailable
	}

	s.execCtx.CurrentToolID = awaitingID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateWaitingSubmit
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateWaitingSubmit)
	}

	submitResult, err := s.awaitHITLSubmitOrAccessLevel(invocation, match, awaitingID, awaitArgs)
	if err != nil {
		return err
	}
	if submitResult.Status == "access_level_auto_approved" || submitResult.Status == "hitl_timeout" {
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}

	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: awaitingID,
		Params:     submitResult.Request.Params,
	})

	normalized, normalizeErr := s.normalizeHITLSubmit(awaitArgs, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"])), "invalid_submit", normalizeErr.Error()),
		})
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", normalizeErr.Error(), false)
		s.appendOriginalToolResult(invocation, frontendSubmitInvalidPayloadResult(invocation, awaitingID, submitResult.Request.Params, normalizeErr))
		return nil
	}
	if len(normalized) > 0 {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     CloneMap(normalized),
		})
	}

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", "user_dismissed", false)
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	}

	if strings.EqualFold(AnyStringNode(normalized["mode"]), "form") {
		selectedForm := firstAwaitItem(normalized["forms"])
		decision := strings.ToLower(strings.TrimSpace(AnyStringNode(selectedForm["decision"])))
		if decision == "approve" {
			formPayload := AnyMapNode(selectedForm["form"])
			rebuiltCommand, rebuildErr := reconstructCommandWithPayload(mapStringArg(invocation.args, "command"), formPayload)
			if rebuildErr != nil {
				payload := NewErrorPayload(
					"frontend_submit_invalid_payload",
					rebuildErr.Error(),
					ErrorScopeFrontendSubmit,
					ErrorCategoryTool,
					map[string]any{
						"awaitingId": awaitingID,
						"toolName":   invocation.toolName,
						"payload":    formPayload,
					},
				)
				result := ToolExecutionResult{
					Output:     marshalJSON(payload),
					Structured: payload,
					Error:      "frontend_submit_invalid_payload",
					ExitCode:   -1,
				}
				s.applyHITLDecision(invocation, *match, awaitingID, "reject", rebuildErr.Error(), false)
				s.appendOriginalToolResult(invocation, result)
				return nil
			}
			invocation.args["command"] = rebuiltCommand
			s.applyHITLDecision(invocation, *match, awaitingID, "approve", "", true)
			invocation.hitlDecision.FormPayload = formPayload
			return s.executeOriginalBash(invocation)
		}
		reason := strings.TrimSpace(AnyStringNode(selectedForm["reason"]))
		rejectedForm := AnyMapNode(selectedForm["form"])
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", reason, false)
		if len(rejectedForm) > 0 {
			invocation.hitlDecision.FormPayload = rejectedForm
		}
		s.appendOriginalToolResult(invocation, hitlRejectedFormToolResult(invocation, reason, rejectedForm))
		return nil
	}

	selectedApproval := firstAwaitItem(normalized["approvals"])
	selectedDecision := strings.TrimSpace(AnyStringNode(selectedApproval["decision"]))
	reason := strings.TrimSpace(AnyStringNode(selectedApproval["reason"]))
	s.applyHITLDecision(invocation, *match, awaitingID, selectedDecision, reason, selectedDecision != "reject")
	if strings.EqualFold(selectedDecision, "reject") {
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	}
	invocation.approvalDecision = selectedDecision
	if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		return s.executeApprovedFileAccessInvocation(invocation, *plan)
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) {
		return s.executeApprovedFileWriteInvocation(invocation, *plan)
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return s.executeApprovedBashSecurityInvocation(invocation, review)
	}
	if review := s.lookupBashAccessReview(invocation); review.RequiresApproval() {
		return s.executeApprovedBashAccessInvocation(invocation, review)
	}
	if result := s.lookupWorkspaceHITL(invocation); result.Intercepted {
		return s.executeApprovedBashInvocation(invocation, result)
	}
	return s.executeOriginalBash(invocation)
}

func (s *llmRunStream) awaitHITLSubmitOrAccessLevel(invocation *preparedToolInvocation, match *hitl.InterceptResult, awaitingID string, awaitArgs map[string]any) (SubmitResult, error) {
	timeout := time.Duration(s.resolveHITLTimeoutWithRule(match.Rule.TimeoutMs)) * time.Millisecond
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		_, version := s.runControl.AccessLevelSnapshot()
		wait := timeout
		if !deadline.IsZero() {
			wait = time.Until(deadline)
			if wait <= 0 {
				s.pending = append(s.pending, DeltaAwaitingAnswer{
					AwaitingID: awaitingID,
					Answer:     hitlTimeoutAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"]))),
				})
				s.applyHITLDecision(invocation, *match, awaitingID, "reject", "timeout", false)
				s.appendOriginalToolResult(invocation, hitlTimeoutToolResult(invocation))
				return SubmitResult{Status: "hitl_timeout"}, nil
			}
		}
		submitResult, accessChanged, err := s.runControl.AwaitSubmitWithTimeoutOrAccessLevelChange(s.ctx, awaitingID, wait, version)
		if accessChanged {
			resolved, resolveErr := s.tryResolvePendingAccessLevelApproval(invocation, *match, awaitingID)
			if resolveErr != nil || resolved {
				return SubmitResult{Status: "access_level_auto_approved"}, resolveErr
			}
			continue
		}
		if err != nil {
			if errors.Is(err, ErrRunInterrupted) {
				return SubmitResult{}, s.handleInterruptIfNeeded()
			}
			s.pending = append(s.pending, DeltaAwaitingAnswer{
				AwaitingID: awaitingID,
				Answer:     hitlTimeoutAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"]))),
			})
			s.applyHITLDecision(invocation, *match, awaitingID, "reject", "timeout", false)
			s.appendOriginalToolResult(invocation, hitlTimeoutToolResult(invocation))
			return SubmitResult{Status: "hitl_timeout"}, nil
		}
		return submitResult, nil
	}
}

func (s *llmRunStream) tryResolvePendingAccessLevelApproval(invocation *preparedToolInvocation, match hitl.InterceptResult, awaitingID string) (bool, error) {
	if !isAccessPolicyApprovalMatch(match) {
		return false, nil
	}
	s.refreshAccessLevelForInvocation(invocation)
	if s.invocationNeedsAccessPolicyApproval(invocation) {
		return false, nil
	}
	accessLevel := s.currentAccessLevel()
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     s.accessLevelAutoApprovalAnswer(awaitingID, invocation, match, accessLevel),
	})
	s.applyHITLDecision(invocation, match, awaitingID, "auto_approved", "accessLevel="+accessLevel, true)
	return true, s.executeOriginalBash(invocation)
}

func (s *llmRunStream) currentAccessLevel() string {
	if s == nil {
		return AccessLevelDefault
	}
	accessLevel := ""
	if s.execCtx != nil {
		accessLevel = s.execCtx.AccessLevel
		if strings.TrimSpace(accessLevel) == "" {
			accessLevel = s.execCtx.Session.AccessLevel
		}
	}
	if strings.TrimSpace(accessLevel) == "" {
		accessLevel = s.session.AccessLevel
	}
	normalized, ok := NormalizeAccessLevel(accessLevel)
	if !ok {
		return AccessLevelDefault
	}
	return normalized
}

func (s *llmRunStream) executeOriginalBash(invocation *preparedToolInvocation) error {
	s.refreshAccessLevelForInvocation(invocation)
	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}

	s.recordAccessPolicyAutoApproval(invocation)
	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	s.appendOriginalToolResult(invocation, result)
	s.execCtx.CurrentToolID = ""
	s.execCtx.CurrentToolName = ""
	return nil
}

func (s *llmRunStream) buildHITLArgs(invocation *preparedToolInvocation, result hitl.InterceptResult) map[string]any {
	command := mapStringArg(invocation.args, "command")
	if strings.EqualFold(result.Rule.ViewportType, "html") {
		return s.buildFormApprovalArgs(command, result)
	}
	return s.buildConfirmApprovalArgs(invocation, result)
}

func (s *llmRunStream) buildConfirmApprovalArgs(invocation *preparedToolInvocation, result hitl.InterceptResult) map[string]any {
	viewportType := strings.TrimSpace(result.Rule.ViewportType)
	if viewportType == "" {
		viewportType = "builtin"
	}
	viewportKey := strings.TrimSpace(result.Rule.ViewportKey)
	if viewportKey == "" {
		viewportKey = "approval"
	}
	return map[string]any{
		"mode":         "approval",
		"viewportType": viewportType,
		"viewportKey":  viewportKey,
		"approvals": []any{
			s.buildApprovalAskItem(invocation),
		},
	}
}

func (s *llmRunStream) buildFormApprovalArgs(command string, result hitl.InterceptResult) map[string]any {
	args := map[string]any{
		"mode":         "form",
		"viewportType": result.Rule.ViewportType,
		"viewportKey":  result.Rule.ViewportKey,
	}
	form := map[string]any{
		"id":      "form-1",
		"command": command,
	}
	if title := strings.TrimSpace(result.Rule.Title); title != "" {
		form["title"] = title
	}
	if payload := extractCommandPayload(result.ParsedCommand); len(payload) > 0 {
		form["form"] = payload
		args["forms"] = []any{form}
		return args
	}
	if payload := extractPayloadFromOriginalCommand(result.OriginalCommand); len(payload) > 0 {
		form["form"] = payload
		args["forms"] = []any{form}
		return args
	}
	args["forms"] = []any{form}
	log.Printf("[llm][run:%s][hitl][warning] missing html approval payload viewportKey=%s command=%q",
		s.session.RunID,
		result.Rule.ViewportKey,
		result.OriginalCommand,
	)
	return args
}

func (s *llmRunStream) buildApprovalAskItem(invocation *preparedToolInvocation) map[string]any {
	command := mapStringArg(invocation.args, "command")
	combinedAccessPlan, combinedWritePlan, combinedWriteApproval := s.combinedFileWriteApprovalPlans(invocation)
	if combinedWriteApproval {
		command = s.fileToolApprovalDisplayCommand(invocation, combinedAccessPlan, combinedWritePlan)
	} else if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		command = s.fileToolApprovalDisplayCommand(invocation, plan, nil)
	} else if plan := s.lookupFileWritePlan(invocation); plan != nil {
		command = s.fileToolApprovalDisplayCommand(invocation, nil, plan)
	} else if result := s.lookupWorkspaceHITL(invocation); result.Intercepted && strings.TrimSpace(result.MatchedCommand) != "" {
		command = result.MatchedCommand + " (" + mapStringArg(invocation.args, "command") + ")"
	}
	description := approvalDescription(invocation)
	if combinedWriteApproval {
		description = strings.TrimSpace(description)
		if description == "" {
			description = fileMutationApprovalFallback(combinedWritePlan)
		}
		description += "（路径超出允许目录）"
	} else if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		if plan.Mode == filetools.ReadAccess {
			description = "read超出允许目录"
		} else if strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_edit") {
			description = "edit超出允许目录"
		} else {
			description = "write超出允许目录"
		}
	} else if result := s.lookupWorkspaceHITL(invocation); result.Intercepted {
		description = "命令访问超出 workspace"
	}
	item := map[string]any{
		"id":                  invocation.toolID,
		"command":             command,
		"description":         description,
		"options":             s.approvalOptionsForInvocation(invocation),
		"allowFreeText":       true,
		"freeTextPlaceholder": "可选：填写理由",
	}
	result := hitl.InterceptResult{}
	if combinedWriteApproval {
		result = fileWriteInterceptResult(*combinedWritePlan)
	} else if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		result = fileAccessInterceptResult(*plan)
	} else if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) {
		result = fileWriteInterceptResult(*plan)
	} else if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		result = bashSecurityInterceptResult(invocation, review)
	} else if workspace := s.lookupWorkspaceHITL(invocation); workspace.Intercepted {
		result = workspace
	} else if invocation != nil && invocation.precheckedHITL != nil {
		result = *invocation.precheckedHITL
	} else if s.checker != nil {
		result = s.lookupPrecheckedHITL(invocation)
	}
	if result.Intercepted {
		if ruleKey := strings.TrimSpace(result.Rule.RuleKey); ruleKey != "" {
			item["ruleKey"] = ruleKey
		}
	}
	return item
}

func (s *llmRunStream) fileToolApprovalDisplayCommand(invocation *preparedToolInvocation, accessPlan *filetools.AccessPlan, writePlan *filetools.WritePlan) string {
	fallback := ""
	if writePlan != nil {
		fallback = writePlan.CommandText
	} else if accessPlan != nil {
		fallback = accessPlan.CommandText
	}
	if accessPlan == nil && writePlan == nil {
		return ""
	}
	toolLabel := ""
	if invocation != nil {
		if tool, ok := s.lookupToolDefinition(invocation.toolName); ok {
			toolLabel = strings.TrimSpace(tool.Label)
		}
	}
	if toolLabel == "" {
		return fallback
	}
	path := ""
	if writePlan != nil {
		path = strings.TrimSpace(writePlan.FilePath)
	}
	if path == "" && accessPlan != nil {
		path = strings.TrimSpace(accessPlan.Path)
	}
	if path == "" && accessPlan != nil {
		path = strings.TrimSpace(accessPlan.RawPath)
	}
	if path == "" {
		return toolLabel
	}
	return toolLabel + " " + path
}

func fileMutationApprovalFallback(plan *filetools.WritePlan) string {
	if plan != nil && (strings.EqualFold(strings.TrimSpace(plan.Operation), "edit") || strings.EqualFold(strings.TrimSpace(plan.ToolName), "file_edit")) {
		return "编辑文件"
	}
	return "写入文件"
}

func (s *llmRunStream) approvalOptionsForInvocation(invocation *preparedToolInvocation) []any {
	if _, _, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
		return buildApprovalOptions()
	}
	if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		return buildFileAccessApprovalOptions()
	}
	return buildApprovalOptions()
}

func buildApprovalOptions() []any {
	return []any{
		map[string]any{
			"label":       "同意",
			"decision":    "approve",
			"description": "只本次放行这条命令",
		},
		map[string]any{
			"label":       "同意（本次运行同规则都放行）",
			"decision":    "approve_rule_run",
			"description": "本次 run 内所有同一拦截规则命中的命令自动放行，不再询问",
		},
		map[string]any{
			"label":       "拒绝",
			"decision":    "reject",
			"description": "终止这条命令",
		},
	}
}

func buildFileAccessApprovalOptions() []any {
	return []any{
		map[string]any{
			"label":       "同意",
			"decision":    "approve",
			"description": "只本次放行这条路径",
		},
		map[string]any{
			"label":       "同意（本次运行同规则都放行）",
			"decision":    "approve_rule_run",
			"description": "本次 run 内同一拦截规则命中的文件访问自动放行，不再询问",
		},
		map[string]any{
			"label":       "拒绝",
			"decision":    "reject",
			"description": "终止这次文件访问",
		},
	}
}

func approvalDescription(invocation *preparedToolInvocation) string {
	description := strings.TrimSpace(mapStringArg(invocation.args, "description"))
	if description != "" {
		return description
	}
	command := strings.TrimSpace(mapStringArg(invocation.args, "command"))
	if len(command) <= 60 {
		return command
	}
	return command[:60]
}

func (s *llmRunStream) resolveHITLTimeout() int64 {
	if s != nil && s.execCtx != nil {
		budget := NormalizeBudget(s.execCtx.Budget)
		if budget.Hitl.TimeoutMs > 0 {
			return int64(budget.Hitl.TimeoutMs)
		}
	}
	if s.engine.cfg.BashHITL.DefaultTimeoutMs > 0 {
		return int64(s.engine.cfg.BashHITL.DefaultTimeoutMs)
	}
	return 120000
}

func (s *llmRunStream) resolveHITLTimeoutWithRule(ruleTimeoutMs int) int64 {
	if ruleTimeoutMs > 0 {
		return int64(ruleTimeoutMs)
	}
	return s.resolveHITLTimeout()
}

func buildHITLApprovalPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	return payload
}

func buildHITLFormPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"mode":     "form",
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	if decision.FormPayload != nil {
		payload["submittedPayload"] = decision.FormPayload
	}
	return payload
}

func buildHITLAwaitingID(toolID string) string {
	return "await_" + strings.TrimSpace(toolID)
}

func buildHITLBatchAwaitingID(runID string, turnStep int) string {
	return fmt.Sprintf("await_batch_%s_%d", strings.TrimSpace(runID), turnStep)
}

func hitlTimeoutAnswer(mode string) map[string]any {
	return AwaitingErrorAnswer(mode, "timeout", "等待项已超时")
}

func frontendSubmitAwaitingAnswer(invocation *preparedToolInvocation, result ToolExecutionResult) map[string]any {
	if len(result.Structured) == 0 {
		return nil
	}
	if result.Error == "" {
		return result.Structured
	}
	mode := strings.TrimSpace(AnyStringNode(invocation.args["mode"]))
	switch result.Error {
	case "frontend_submit_timeout":
		return AwaitingErrorAnswer(mode, "timeout", AnyStringNode(AnyMapNode(result.Structured["error"])["message"]))
	case "frontend_submit_invalid_payload":
		return AwaitingErrorAnswer(mode, "invalid_submit", AnyStringNode(result.Structured["message"]))
	default:
		return nil
	}
}

func hitlRejectedToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_rejected",
		"User rejected this command. Do NOT retry with a different command. End the turn now.",
		ErrorScopeTool,
		ErrorCategorySystem,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	payload["final"] = true
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected", "User rejected this command. Do NOT retry with a different command. End the turn now."),
		Structured: payload,
		Error:      "user_rejected",
		ExitCode:   -1,
	}
}

func hitlRejectedFormToolResult(invocation *preparedToolInvocation, reason string, form map[string]any) ToolExecutionResult {
	reason = strings.TrimSpace(reason)
	if len(form) == 0 && reason == "" {
		return hitlRejectedToolResult(invocation)
	}
	feedback := map[string]any{
		"status": "rejected_with_feedback",
		"toolId": invocation.toolID,
	}
	if reason != "" {
		feedback["reason"] = reason
	}
	if len(form) > 0 {
		feedback["revisedForm"] = form
	}
	msg := "User rejected this command with feedback. Review the reason and revised form, then try again with corrections."
	payload := NewErrorPayload(
		"hitl_rejected_with_feedback",
		msg,
		ErrorScopeTool,
		ErrorCategorySystem,
		feedback,
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected_with_feedback", msg),
		Structured: payload,
		Error:      "user_rejected_with_feedback",
		ExitCode:   -1,
	}
}

func hitlTimeoutToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_timeout",
		"command execution timed out while waiting for user approval",
		ErrorScopeTool,
		ErrorCategoryTimeout,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("hitl_timeout", "command execution timed out while waiting for user approval"),
		Structured: payload,
		Error:      "hitl_timeout",
		ExitCode:   -1,
	}
}

func frontendSubmitInvalidPayloadResult(invocation *preparedToolInvocation, awaitingID string, params any, err error) ToolExecutionResult {
	payload := NewErrorPayload(
		"frontend_submit_invalid_payload",
		err.Error(),
		ErrorScopeFrontendSubmit,
		ErrorCategoryTool,
		map[string]any{
			"awaitingId": awaitingID,
			"toolName":   invocation.toolName,
			"params":     params,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("frontend_submit_invalid_payload", err.Error()),
		Structured: payload,
		Error:      "frontend_submit_invalid_payload",
		ExitCode:   -1,
	}
}

func (s *llmRunStream) buildHITLAwaitDelta(awaitingID string, args map[string]any, ruleTimeoutMs int) DeltaAwaitAsk {
	timeout := s.resolveHITLTimeoutWithRule(ruleTimeoutMs)
	await := DeltaAwaitAsk{
		AwaitingID: awaitingID,
		Mode:       strings.ToLower(strings.TrimSpace(AnyStringNode(args["mode"]))),
		Timeout:    timeout,
		RunID:      s.session.RunID,
	}
	await.ViewportType = strings.TrimSpace(AnyStringNode(args["viewportType"]))
	await.ViewportKey = strings.TrimSpace(AnyStringNode(args["viewportKey"]))
	switch await.Mode {
	case "question":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "question"
		}
	case "approval":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "approval"
		}
	case "form":
		if await.ViewportType == "" {
			await.ViewportType = "html"
		}
	case "plan":
		if await.ViewportType == "" {
			await.ViewportType = "builtin"
		}
		if await.ViewportKey == "" {
			await.ViewportKey = "plan"
		}
	}
	if questions := cloneAnySlice(args["questions"]); len(questions) > 0 {
		await.Questions = questions
	}
	if approvals := cloneAnySlice(args["approvals"]); len(approvals) > 0 {
		await.Approvals = approvals
	}
	if forms := cloneAnySlice(args["forms"]); len(forms) > 0 {
		await.Forms = sanitizeAwaitAskForms(forms)
	}
	if plan := AnyMapNode(args["plan"]); len(plan) > 0 {
		await.Plan = CloneMap(plan)
	}
	return await
}

func sanitizeAwaitAskForms(forms []any) []any {
	cloned := make([]any, 0, len(forms))
	for _, item := range forms {
		form := AnyMapNode(item)
		if len(form) == 0 {
			continue
		}
		entry := CloneMap(form)
		delete(entry, "command")
		cloned = append(cloned, entry)
	}
	return cloned
}

func cloneAnySlice(raw any) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case map[string]any:
			cloned = append(cloned, CloneMap(value))
		default:
			cloned = append(cloned, value)
		}
	}
	return cloned
}

func firstAwaitItem(raw any) map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		for _, item := range typed {
			if len(item) > 0 {
				return item
			}
		}
	case []any:
		for _, item := range typed {
			entry := AnyMapNode(item)
			if len(entry) > 0 {
				return entry
			}
		}
	}
	return nil
}

func (s *llmRunStream) normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return normalizeHITLSubmit(args, params)
}

func awaitingContextFromStreamAsk(awaitAsk *stream.AwaitAsk) AwaitingSubmitContext {
	if awaitAsk == nil {
		return AwaitingSubmitContext{}
	}
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Plan),
	}
}

func awaitingContextFromDeltaAsk(awaitAsk DeltaAwaitAsk) AwaitingSubmitContext {
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms, awaitAsk.Plan),
	}
}

func awaitItemCount(mode string, questions []any, approvals []any, forms []any, plan map[string]any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form":
		return len(forms)
	case "plan":
		if len(plan) > 0 {
			return 1
		}
		return 0
	default:
		return 0
	}
}
