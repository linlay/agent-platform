package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/api"
	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) prepareToolCall(toolCall openAIToolCall) (*preparedToolInvocation, []AgentDelta, *openAIMessage) {
	s.syncAccessLevelFromRunControl()
	toolID := toolCall.ID
	if strings.TrimSpace(toolID) == "" {
		return nil, []AgentDelta{DeltaError{Error: NewErrorPayload(
			"missing_tool_call_id",
			"provider tool call missing toolCallId",
			ErrorScopeModel,
			ErrorCategoryModel,
			nil,
		)}}, nil
	}

	args := map[string]any{}
	if strings.TrimSpace(toolCall.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			deltas, message := preparedToolErrorResult(toolID, toolCall.Function.Name, "invalid tool arguments: "+err.Error(), "invalid_tool_arguments")
			return nil, deltas, message
		}
	}
	expandedArgs, err := ExpandToolArgsTemplates(args, s.previousToolResult)
	if err != nil {
		deltas, message := preparedToolErrorResult(toolID, toolCall.Function.Name, err.Error(), "tool_args_template_missing_value")
		return nil, deltas, message
	}
	args, _ = expandedArgs.(map[string]any)

	if validationErr := s.validateFrontendToolArgs(toolCall.Function.Name, args); validationErr != nil {
		deltas, message := preparedToolErrorResult(toolID, toolCall.Function.Name, "invalid tool arguments: "+validationErr.Error(), "invalid_tool_arguments")
		return nil, deltas, message
	}
	if validationErr := validateBashToolArgs(toolCall.Function.Name, args); validationErr != nil {
		deltas, message := preparedToolErrorResult(toolID, toolCall.Function.Name, "invalid tool arguments: "+validationErr.Error(), "invalid_tool_arguments")
		return nil, deltas, message
	}
	if validationErr := validateWriteToolArgs(toolCall.Function.Name, args); validationErr != nil {
		deltas, message := preparedToolErrorResult(toolID, toolCall.Function.Name, "invalid tool arguments: "+validationErr.Error(), "invalid_tool_arguments")
		return nil, deltas, message
	}

	if strings.EqualFold(strings.TrimSpace(toolCall.Function.Name), InvokeAgentsToolName) {
		rawTasks, _ := args["tasks"].([]any)
		if len(rawTasks) < 1 || len(rawTasks) > MaxInvokeAgentTasks {
			message := fmt.Sprintf("invalid tool arguments: tasks must contain between 1 and %d items", MaxInvokeAgentTasks)
			deltas, openAIMessage := preparedToolErrorResult(toolID, toolCall.Function.Name, message, "invalid_tool_arguments")
			return nil, deltas, openAIMessage
		}
		tasks := make([]SubAgentTaskSpec, 0, len(rawTasks))
		for _, rawTask := range rawTasks {
			taskMap, _ := rawTask.(map[string]any)
			subAgentKey := strings.TrimSpace(mapStringArg(taskMap, "subAgentKey"))
			taskText := strings.TrimSpace(mapStringArg(taskMap, "task"))
			taskName := strings.TrimSpace(mapStringArg(taskMap, "taskName"))
			if taskName == "" {
				taskName = subAgentKey
			}
			if subAgentKey == "" || taskText == "" {
				message := "invalid tool arguments: every task requires subAgentKey and task"
				deltas, openAIMessage := preparedToolErrorResult(toolID, toolCall.Function.Name, message, "invalid_tool_arguments")
				return nil, deltas, openAIMessage
			}
			tasks = append(tasks, SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       mapStringListArg(taskMap, "files"),
			})
		}
		return &preparedToolInvocation{
			toolID:              toolID,
			toolName:            toolCall.Function.Name,
			args:                args,
			awaitExternalResult: true,
			prelude: []AgentDelta{DeltaInvokeSubAgents{
				MainToolID: toolID,
				Tasks:      tasks,
			}},
		}, nil, nil
	}

	invocation := &preparedToolInvocation{
		toolID:   toolID,
		toolName: toolCall.Function.Name,
		args:     args,
		prelude:  s.preToolInvocationDeltas(toolID, toolCall.Function.Name, args),
	}
	s.refreshAccessLevelForInvocation(invocation)
	if isBashTool(invocation.toolName) {
		review := s.reviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
		switch review.Decision {
		case bashsec.ReviewRequiresApproval:
			invocation.bashSecurityReview = &review
		case bashsec.ReviewBlock:
			invocation.bashSecurityReview = &review
			if result := s.lookupPrecheckedHITL(invocation); !result.Intercepted {
				blocked := bashSecurityBlockedToolResult(review)
				deltas, message := preparedToolResultMessage(toolID, toolCall.Function.Name, blocked, blocked.Output)
				return nil, deltas, message
			}
		}
	}
	if accessPlan, ok := s.buildFileAccessPlan(invocation); ok {
		invocation.fileAccessPlan = accessPlan
	}
	if isWriteTool(invocation.toolName) && s.engine.cfg.FileTools.RequireWriteApproval {
		if plan, err := s.buildFileWritePlan(invocation); err == nil && s.fileWritePlanNeedsApproval(plan) {
			invocation.fileWritePlan = &plan
		}
	}
	return invocation, nil, nil
}

func (s *llmRunStream) activateNextToolCall() {
	if s.activeToolCall != nil || len(s.queuedToolCalls) == 0 {
		return
	}
	s.activeToolCall = s.queuedToolCalls[0]
	s.queuedToolCalls = s.queuedToolCalls[1:]
	if len(s.activeToolCall.prelude) > 0 {
		s.pending = append(s.pending, s.activeToolCall.prelude...)
	}
}

func (s *llmRunStream) invokeActiveToolCall() error {
	invocation := s.activeToolCall
	if invocation == nil {
		return nil
	}
	s.beginToolInvocation(invocation)
	if result := s.checkBudgetBeforeToolCall(invocation.toolName); result != nil {
		s.appendOriginalToolResult(invocation, *result)
		return nil
	}

	keepActive := false
	defer func() {
		if !keepActive {
			s.finishToolInvocation(invocation)
		}
	}()

	if handled, keep, err := s.handleDeferredToolInvocation(invocation); handled {
		keepActive = keep
		return err
	}
	if handled, err := s.handleToolApprovalBeforeInvoke(invocation); handled {
		return err
	}
	return s.invokeToolAndPublishResult(invocation)
}

func (s *llmRunStream) beginToolInvocation(invocation *preparedToolInvocation) {
	s.refreshAccessLevelForInvocation(invocation)
	s.skipPostToolHook = false

	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}
	if !invocation.toolCallCounted {
		s.execCtx.ToolCalls++
		s.stageToolCalls++
		s.runToolCallCount++
		invocation.toolCallCounted = true
	}
}

func (s *llmRunStream) finishToolInvocation(invocation *preparedToolInvocation) {
	if s.runControl != nil {
		s.runControl.ClearExpectedSubmit(invocation.toolID)
	}
	s.execCtx.CurrentToolID = ""
	s.execCtx.CurrentToolName = ""
	s.activeToolCall = nil
}

func (s *llmRunStream) handleDeferredToolInvocation(invocation *preparedToolInvocation) (bool, bool, error) {
	if invocation.queuedResult != nil {
		s.appendOriginalToolResult(invocation, *invocation.queuedResult)
		invocation.queuedResult = nil
		return true, false, nil
	}
	if invocation.awaitExternalResult {
		return true, true, nil
	}
	return false, false, nil
}

func (s *llmRunStream) handleToolApprovalBeforeInvoke(invocation *preparedToolInvocation) (bool, error) {
	if handled, err := s.handleFileApprovalBeforeInvoke(invocation); handled {
		return true, err
	}
	if handled, err := s.handleBashSecurityApprovalBeforeInvoke(invocation); handled {
		return true, err
	}
	if invocation.precheckedHITL != nil && invocation.precheckedHITL.Intercepted {
		return true, s.handleHITLApproval(invocation, *invocation.precheckedHITL, hitlApprovalOptions{
			allowExistingDecision: true,
		})
	}
	if s.checker != nil && isBashTool(invocation.toolName) {
		command := mapStringArg(invocation.args, "command")
		if result := s.checker.Check(command, s.execCtx.HITLLevel); result.Intercepted {
			return true, s.handleHITLApproval(invocation, result, hitlApprovalOptions{
				skipPostToolHookImmediately: true,
			})
		}
	}
	if s.handleBashSecurityBlockBeforeInvoke(invocation) {
		return true, nil
	}
	if handled, err := s.handleBashAccessApprovalBeforeInvoke(invocation); handled {
		return true, err
	}
	return false, nil
}

func (s *llmRunStream) handleFileApprovalBeforeInvoke(invocation *preparedToolInvocation) (bool, error) {
	if accessPlan := s.lookupFileAccessPlan(invocation); accessPlan != nil && s.fileAccessPlanNeedsApproval(*accessPlan) {
		return s.handleBuiltInApprovalRequest(s.fileAccessApprovalRequest(invocation, *accessPlan))
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) {
		return s.handleBuiltInApprovalRequest(s.fileWriteApprovalRequest(invocation, *plan))
	}
	return false, nil
}

func (s *llmRunStream) handleBashSecurityApprovalBeforeInvoke(invocation *preparedToolInvocation) (bool, error) {
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return s.handleBuiltInApprovalRequest(s.bashSecurityApprovalRequest(invocation, review))
	}
	return false, nil
}

func (s *llmRunStream) handleBashAccessApprovalBeforeInvoke(invocation *preparedToolInvocation) (bool, error) {
	if review := s.lookupBashAccessReview(invocation); review.Decision == accesspolicy.DecisionRequiresApproval {
		return s.handleBuiltInApprovalRequest(s.bashAccessApprovalRequest(invocation, review))
	}
	return false, nil
}

func (s *llmRunStream) handleBuiltInApprovalRequest(request approvalRequest) (bool, error) {
	if handled, err := s.tryResolveApprovalFastPath(request, approvalFastPathExecuteNow); handled {
		return true, err
	}
	s.skipPostToolHook = true
	return true, s.emitApprovalRequestDeltas(request)
}

func (s *llmRunStream) handleBashSecurityBlockBeforeInvoke(invocation *preparedToolInvocation) bool {
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewBlock {
		s.appendOriginalToolResult(invocation, bashSecurityBlockedToolResult(review))
		return true
	}
	return false
}

type hitlApprovalOptions struct {
	allowExistingDecision       bool
	skipPostToolHookImmediately bool
}

func (s *llmRunStream) handleHITLApproval(invocation *preparedToolInvocation, result hitl.InterceptResult, options hitlApprovalOptions) error {
	if options.skipPostToolHookImmediately {
		s.skipPostToolHook = true
	}
	request := hitlApprovalRequest(invocation, result)
	if strings.EqualFold(result.Rule.ViewportType, "builtin") {
		if options.allowExistingDecision && request.hasApprovalDecision() {
			return s.executeApprovedApprovalRequest(request)
		}
		if handled, err := s.tryResolveHITLApprovalFastPath(request, approvalFastPathExecuteNow); handled {
			return err
		}
	}
	if !options.skipPostToolHookImmediately {
		s.skipPostToolHook = true
	}
	return s.emitApprovalRequestDeltas(request)
}

func (s *llmRunStream) invokeToolAndPublishResult(invocation *preparedToolInvocation) error {
	s.recordAccessPolicyAutoApproval(invocation)
	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	if isPlanningWriteTool(invocation.toolName) && result.ExitCode == 0 {
		if s.execCtx != nil && s.execCtx.PlanningState != nil {
			s.execCtx.PlanningState.ToolCallID = invocation.toolID
			s.execCtx.PlanningState.ToolName = invocation.toolName
		}
		s.appendFinalPlanningDeltas(invocation.toolID, result)
		return nil
	}
	s.appendFrontendSubmitDeltas(invocation, result)
	s.appendOriginalToolResult(invocation, result)
	if isPlanTool(invocation.toolName) && s.execCtx != nil && s.execCtx.PlanState != nil && len(s.execCtx.PlanState.Tasks) > 0 {
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   PlanTasksArray(s.execCtx.PlanState),
		})
	}
	appendPublishedArtifactDelta(&s.pending, s.session, result.Structured["publishedArtifacts"])
	return nil
}

func (s *llmRunStream) appendFrontendSubmitDeltas(invocation *preparedToolInvocation, result ToolExecutionResult) {
	if result.SubmitInfo != nil {
		s.pending = append(s.pending, DeltaRequestSubmit{
			RequestID:  s.session.RequestID,
			ChatID:     s.session.ChatID,
			RunID:      s.session.RunID,
			AwaitingID: result.SubmitInfo.AwaitingID,
			SubmitID:   result.SubmitInfo.SubmitID,
			Params:     result.SubmitInfo.Params,
		})
		if answer := frontendSubmitAwaitingAnswer(invocation, result); len(answer) > 0 {
			if result.SubmitInfo.SubmitID != "" {
				answer["submitId"] = result.SubmitInfo.SubmitID
			}
			s.pending = append(s.pending, DeltaAwaitingAnswer{
				AwaitingID: result.SubmitInfo.AwaitingID,
				Answer:     CloneMap(answer),
			})
		}
	} else if len(result.Structured) > 0 {
		if answer := frontendSubmitAwaitingAnswer(invocation, result); len(answer) > 0 {
			s.pending = append(s.pending, DeltaAwaitingAnswer{
				AwaitingID: invocation.toolID,
				Answer:     CloneMap(answer),
			})
		}
	}
}

func preparedToolErrorResult(toolID, toolName, output, errorCode string) ([]AgentDelta, *openAIMessage) {
	return preparedToolResultMessage(toolID, toolName, ToolExecutionResult{
		Output:   output,
		Error:    errorCode,
		ExitCode: -1,
	}, output)
}

func preparedToolResultMessage(toolID, toolName string, result ToolExecutionResult, content string) ([]AgentDelta, *openAIMessage) {
	return []AgentDelta{DeltaToolResult{
			ToolID:   toolID,
			ToolName: toolName,
			Result:   result,
		}}, &openAIMessage{
			Role:       "tool",
			ToolCallID: toolID,
			Name:       toolName,
			Content:    content,
		}
}

func (s *llmRunStream) checkBudgetBeforeToolCall(toolName string) *ToolExecutionResult {
	if s == nil || s.execCtx == nil {
		return nil
	}
	budget := NormalizeBudget(s.execCtx.Budget)
	if budget.Tool.MaxCalls > 0 && s.execCtx.ToolCalls > budget.Tool.MaxCalls {
		payload := NewErrorPayload(
			"tool_calls_exceeded",
			"tool call budget exceeded",
			ErrorScopeTool,
			ErrorCategoryTool,
			map[string]any{
				"toolCalls":  s.execCtx.ToolCalls,
				"limitValue": budget.Tool.MaxCalls,
				"limitName":  "budget.tool.maxCalls",
				"toolName":   toolName,
			},
		)
		return &ToolExecutionResult{Output: MarshalJSON(payload), Structured: payload, Error: "tool_calls_exceeded", ExitCode: -1}
	}
	if limit := s.stageToolCallLimit(budget); limit > 0 && s.stageToolCalls > limit {
		limitName := "budget.stages." + s.budgetStage + ".tool.maxCalls"
		payload := NewErrorPayload(
			"tool_calls_exceeded",
			"stage tool call budget exceeded",
			ErrorScopeTool,
			ErrorCategoryTool,
			map[string]any{
				"toolCalls":  s.stageToolCalls,
				"limitValue": limit,
				"limitName":  limitName,
				"stage":      s.budgetStage,
				"toolName":   toolName,
			},
		)
		return &ToolExecutionResult{Output: MarshalJSON(payload), Structured: payload, Error: "tool_calls_exceeded", ExitCode: -1}
	}
	return nil
}

func (s *llmRunStream) stageToolCallLimit(budget Budget) int {
	stage := normalizeBudgetStageName(s.budgetStage)
	if stageBudget, ok := budget.Stages[stage]; ok && stageBudget.Tool.MaxCalls > 0 {
		return stageBudget.Tool.MaxCalls
	}
	if s.maxSteps > 0 {
		return s.maxSteps * 2
	}
	return 0
}

func appendPublishedArtifactDelta(pending *[]AgentDelta, session QuerySession, raw any) {
	published := publishedArtifactMaps(raw)
	if len(published) == 0 {
		return
	}
	*pending = append(*pending, DeltaArtifactPublish{
		ChatID:        session.ChatID,
		RunID:         session.RunID,
		ArtifactCount: len(published),
		Artifacts:     published,
	})
}

func publishedArtifactMaps(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if len(item) == 0 {
				continue
			}
			items = append(items, CloneMap(item))
		}
		return items
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, rawItem := range typed {
			item, _ := rawItem.(map[string]any)
			if len(item) == 0 {
				continue
			}
			items = append(items, CloneMap(item))
		}
		return items
	default:
		return nil
	}
}

func (s *llmRunStream) appendOriginalToolResult(invocation *preparedToolInvocation, result ToolExecutionResult) {
	result = applyHITLMetadata(result, invocation)
	result = s.maybeSpillToolResult(invocation, result)
	s.previousToolResult = structuredOrOutput(result)
	content := s.toolResultContent(invocation.toolName, result)
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
	s.messages = append(s.messages, openAIMessage{
		Role:       "tool",
		ToolCallID: invocation.toolID,
		Name:       invocation.toolName,
		Content:    content,
	})
	if s.lastTrace != nil {
		s.lastTrace.appendToolResult(invocation, content, result)
	}
	if entry, ok := s.buildHITLNoticeEntry(invocation); ok {
		s.pendingHITLNotices = append(s.pendingHITLNotices, entry)
	}
	if len(s.queuedToolCalls) == 0 && len(s.pendingHITLNotices) > 0 {
		notice, approval := buildHITLBatchSummaryAndApproval(s.pendingHITLNotices)
		if notice != "" {
			s.messages = append(s.messages, openAIMessage{
				Role:    "user",
				Content: notice,
			})
		}
		if s.onApprovalSummary != nil && approval != nil {
			s.onApprovalSummary(*approval)
		}
		s.pendingHITLNotices = nil
	}
}

func (s *llmRunStream) recordAccessPolicyAutoApproval(invocation *preparedToolInvocation) {
	if invocation == nil || invocation.hitlDecision != nil {
		return
	}
	if plan := s.lookupFileAccessPlan(invocation); plan != nil && plan.AutoApproved {
		invocation.hitlDecision = &hitlDecisionState{
			Decision: "auto_approved",
			Reason:   "accessLevel=auto_approve",
			RuleKey:  strings.TrimSpace(plan.RuleKey),
			Executed: true,
			Mode:     "approval",
		}
		return
	}
	if review := s.lookupBashAccessReview(invocation); review.AutoApproved() {
		invocation.hitlDecision = &hitlDecisionState{
			Decision: "auto_approved",
			Reason:   "accessLevel=auto_approve",
			RuleKey:  strings.TrimSpace(review.RuleKey),
			Executed: true,
			Mode:     "approval",
		}
	}
}

func applyHITLMetadata(result ToolExecutionResult, invocation *preparedToolInvocation) ToolExecutionResult {
	if invocation == nil || invocation.hitlDecision == nil {
		return result
	}
	switch strings.ToLower(strings.TrimSpace(invocation.hitlDecision.Mode)) {
	case "approval":
		result.HITL = buildHITLApprovalPayload(invocation.hitlDecision)
	case "form":
		result.HITL = buildHITLFormPayload(invocation.hitlDecision)
	}
	return result
}

func (s *llmRunStream) toolResultContent(toolName string, result ToolExecutionResult) string {
	toolDef, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return result.Output
	}
	return formatSubmitResultForLLM(toolDef, s.engine.frontend, result)
}

func bashSecurityBlockedToolResult(review bashsec.ReviewResult) ToolExecutionResult {
	reason := strings.TrimSpace(review.Reason)
	if reason == "" {
		reason = "bash command blocked by security review"
	}
	payload := map[string]any{
		"error":    "bash_security_blocked",
		"exitCode": -1,
		"output":   reason,
	}
	if ruleKey := strings.TrimSpace(review.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if fingerprint := strings.TrimSpace(review.Fingerprint); fingerprint != "" {
		payload["fingerprint"] = fingerprint
	}
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		Error:      "bash_security_blocked",
		ExitCode:   -1,
	}
}

func isBashTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "simple-bash":
		return true
	default:
		return false
	}
}

func isWriteTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "file_write", "file_edit":
		return true
	default:
		return false
	}
}

func fileMutationDeniedCode(invocation *preparedToolInvocation) string {
	if invocation != nil && strings.EqualFold(strings.TrimSpace(invocation.toolName), "file_edit") {
		return "file_edit_denied"
	}
	return "file_write_denied"
}

func mapStringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func mapStringListArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   0,
	}
}

func isPlanTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "plan_add_tasks", "plan_update_task":
		return true
	default:
		return false
	}
}

func isPlanningWriteTool(name string) bool {
	return IsFinalizePlanningToolName(name)
}

func (s *llmRunStream) preToolInvocationDeltas(toolID string, toolName string, payload map[string]any) []AgentDelta {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	toolKind, _ := tool.Meta["kind"].(string)
	sourceType, _ := tool.Meta["sourceType"].(string)
	if strings.EqualFold(strings.TrimSpace(sourceType), "mcp") {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(toolKind), "frontend") {
		return nil
	}
	if s.engine.frontend == nil {
		return nil
	}
	handler, ok := s.engine.frontend.Handler(toolName)
	if !ok {
		return nil
	}
	toolTimeout := resolveFrontendAwaitTimeout(toolName, tool, payload, s.execCtx.Budget)
	awaitAsk := handler.BuildInitialAwaitAsk(toolID, s.session.RunID, tool, payload, 0, toolTimeout)
	if s.runControl != nil && awaitAsk != nil {
		s.runControl.ExpectSubmit(awaitingContextFromStreamAsk(awaitAsk))
	}
	return nil
}

func (s *llmRunStream) lookupToolDefinition(toolName string) (api.ToolDetailResponse, bool) {
	if s.checker != nil {
		if tool, ok := s.checker.Tool(toolName); ok {
			return tool, true
		}
	}
	useSandboxBash := s.session.AgentHasRuntimeSandbox || (s.execCtx != nil && s.execCtx.Session.AgentHasRuntimeSandbox)
	for _, tool := range effectiveToolDefinitions(s.engine.tools.Definitions(), nil, useSandboxBash) {
		if strings.EqualFold(strings.TrimSpace(tool.Name), strings.TrimSpace(toolName)) {
			return tool, true
		}
		if strings.EqualFold(strings.TrimSpace(tool.Key), strings.TrimSpace(toolName)) {
			return tool, true
		}
	}
	return api.ToolDetailResponse{}, false
}
