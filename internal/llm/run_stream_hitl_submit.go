package llm

import (
	"errors"
	"log"
	"strings"

	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
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

	s.appendHITLRequestSubmit(awaitingID, submitResult)
	normalized, normalizeErr := s.normalizeHITLSubmitAndEmitAnswer(awaitingID, awaitArgs, submitResult)
	if normalizeErr != nil {
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", normalizeErr.Error(), false)
		s.appendOriginalToolResult(invocation, frontendSubmitInvalidPayloadResult(invocation, awaitingID, submitResult.Request.Params, normalizeErr))
		return nil
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
	if request, ok := s.approvalRequestForInvocation(invocation); ok {
		return s.executeApprovedApprovalRequest(request)
	}
	return s.executeOriginalBash(invocation)
}

func (s *llmRunStream) awaitHITLSubmitOrAccessLevel(invocation *preparedToolInvocation, match *hitl.InterceptResult, awaitingID string, awaitArgs map[string]any) (SubmitResult, error) {
	mode := strings.TrimSpace(AnyStringNode(awaitArgs["mode"]))
	return s.awaitHITLSubmitOrAccessLevelChange(hitlSubmitWaitConfig{
		awaitingID:  awaitingID,
		mode:        mode,
		ruleTimeout: int64(match.Rule.Timeout),
		onAccessLevelChange: func() (bool, error) {
			return s.tryResolvePendingAccessLevelApproval(invocation, *match, awaitingID)
		},
		onTimeout: func() {
			s.timeoutHITLSubmit(invocation, *match, awaitingID, mode)
		},
	})
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
	}
	item := map[string]any{
		"id":                  invocation.toolID,
		"command":             command,
		"description":         description,
		"options":             s.approvalOptionsForInvocation(invocation),
		"allowFreeText":       true,
		"freeTextPlaceholder": "拒绝，请告知如何调整",
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

func (s *llmRunStream) resolveHITLTimeoutWithItem(mode string, itemTimeout int64) int64 {
	budget := Budget{}
	if s != nil && s.execCtx != nil {
		budget = NormalizeBudget(s.execCtx.Budget)
	}
	return ResolveHITLTimeout(mode, itemTimeout, budget)
}
