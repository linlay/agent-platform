package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/bashsec"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
)

func (s *llmRunStream) prepareToolCall(toolCall openAIToolCall) (*preparedToolInvocation, []AgentDelta, *openAIMessage) {
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
			return nil, []AgentDelta{DeltaToolResult{
					ToolID:   toolID,
					ToolName: toolCall.Function.Name,
					Result: ToolExecutionResult{
						Output:   "invalid tool arguments: " + err.Error(),
						Error:    "invalid_tool_arguments",
						ExitCode: -1,
					},
				}}, &openAIMessage{
					Role:       "tool",
					ToolCallID: toolID,
					Name:       toolCall.Function.Name,
					Content:    "invalid tool arguments: " + err.Error(),
				}
		}
	}
	expandedArgs, err := ExpandToolArgsTemplates(args, s.previousToolResult)
	if err != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   err.Error(),
					Error:    "tool_args_template_missing_value",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    err.Error(),
			}
	}
	args, _ = expandedArgs.(map[string]any)

	if validationErr := s.validateFrontendToolArgs(toolCall.Function.Name, args); validationErr != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   "invalid tool arguments: " + validationErr.Error(),
					Error:    "invalid_tool_arguments",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + validationErr.Error(),
			}
	}
	if validationErr := validateBashToolArgs(toolCall.Function.Name, args); validationErr != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   "invalid tool arguments: " + validationErr.Error(),
					Error:    "invalid_tool_arguments",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + validationErr.Error(),
			}
	}
	if validationErr := validateWriteToolArgs(toolCall.Function.Name, args); validationErr != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   "invalid tool arguments: " + validationErr.Error(),
					Error:    "invalid_tool_arguments",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + validationErr.Error(),
			}
	}

	if strings.EqualFold(strings.TrimSpace(toolCall.Function.Name), InvokeAgentsToolName) {
		rawTasks, _ := args["tasks"].([]any)
		if len(rawTasks) < 1 || len(rawTasks) > 3 {
			message := "invalid tool arguments: tasks must contain between 1 and 3 items"
			return nil, []AgentDelta{DeltaToolResult{
					ToolID:   toolID,
					ToolName: toolCall.Function.Name,
					Result: ToolExecutionResult{
						Output:   message,
						Error:    "invalid_tool_arguments",
						ExitCode: -1,
					},
				}}, &openAIMessage{
					Role:       "tool",
					ToolCallID: toolID,
					Name:       toolCall.Function.Name,
					Content:    message,
				}
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
				return nil, []AgentDelta{DeltaToolResult{
						ToolID:   toolID,
						ToolName: toolCall.Function.Name,
						Result: ToolExecutionResult{
							Output:   message,
							Error:    "invalid_tool_arguments",
							ExitCode: -1,
						},
					}}, &openAIMessage{
						Role:       "tool",
						ToolCallID: toolID,
						Name:       toolCall.Function.Name,
						Content:    message,
					}
			}
			tasks = append(tasks, SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
			})
		}
		groupID := "group_" + toolID
		return &preparedToolInvocation{
			toolID:              toolID,
			toolName:            toolCall.Function.Name,
			args:                args,
			awaitExternalResult: true,
			prelude: []AgentDelta{DeltaInvokeSubAgents{
				MainToolID: toolID,
				GroupID:    groupID,
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
	if isBashTool(invocation.toolName) {
		review := s.reviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
		if review.Decision == bashsec.ReviewRequiresApproval {
			invocation.bashSecurityReview = &review
		}
	}
	if isWriteTool(invocation.toolName) && s.engine.cfg.FileTools.RequireWriteApproval {
		if plan, err := filetools.BuildWritePlan(s.engine.cfg.FileTools, invocation.args); err == nil {
			invocation.fileWritePlan = &plan
		}
	}
	return invocation, nil, nil
}

func (s *llmRunStream) validateFrontendToolArgs(toolName string, args map[string]any) error {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	toolKind, _ := tool.Meta["kind"].(string)
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
	return handler.ValidateArgs(args)
}

func validateBashToolArgs(toolName string, args map[string]any) error {
	if !isBashTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "command")) == "" {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "description")) == "" {
		return fmt.Errorf("description is required for bash tools")
	}
	return nil
}

func validateWriteToolArgs(toolName string, args map[string]any) error {
	if !isWriteTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "file_path")) == "" {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "description")) == "" {
		return fmt.Errorf("description is required for write tools")
	}
	return nil
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
	s.skipPostToolHook = false

	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}
	if !invocation.toolCallCounted {
		s.execCtx.ToolCalls++
		invocation.toolCallCounted = true
	}
	keepActive := false
	defer func() {
		if keepActive {
			return
		}
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(invocation.toolID)
		}
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		s.activeToolCall = nil
	}()
	if invocation.queuedResult != nil {
		s.appendOriginalToolResult(invocation, *invocation.queuedResult)
		invocation.queuedResult = nil
		return nil
	}
	if invocation.awaitExternalResult {
		keepActive = true
		return nil
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.engine.cfg.FileTools.RequireWriteApproval {
		if strings.TrimSpace(invocation.approvalDecision) != "" {
			return s.executeApprovedFileWriteInvocation(invocation, *plan)
		}
		if filetools.HasWriteApproval(s.execCtx, *plan) {
			return s.executeOriginalBash(invocation)
		}
		s.skipPostToolHook = true
		return s.emitFileWriteApprovalDeltas(invocation, *plan)
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		if strings.TrimSpace(invocation.approvalDecision) != "" {
			return s.executeApprovedBashSecurityInvocation(invocation, review)
		}
		if s.isRuleWhitelisted(review.RuleKey) {
			s.applyHITLDecision(invocation, bashSecurityInterceptResult(invocation, review), "", "approve_prefix_run", "", true)
			s.registerBashSecurityApproval(review.Fingerprint)
			return s.executeOriginalBash(invocation)
		}
		if s.shouldAutoApproveBashSecurity(review) {
			s.registerBashSecurityApproval(review.Fingerprint)
			return s.executeOriginalBash(invocation)
		}
		if s.hasBashSecurityApproval(review.Fingerprint) {
			return s.executeOriginalBash(invocation)
		}
		s.skipPostToolHook = true
		return s.emitBashSecurityApprovalDeltas(invocation, review)
	}
	if invocation.precheckedHITL != nil && invocation.precheckedHITL.Intercepted {
		result := *invocation.precheckedHITL
		if strings.EqualFold(result.Rule.ViewportType, "builtin") {
			if strings.TrimSpace(invocation.approvalDecision) != "" {
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.isRuleWhitelisted(result.Rule.RuleKey) {
				s.applyHITLDecision(invocation, result, "", "approve_prefix_run", "", true)
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.shouldAutoApproveHITL(result) {
				return s.executeOriginalBash(invocation)
			}
		}
		s.skipPostToolHook = true
		return s.emitHITLConfirmDeltas(invocation, result)
	}
	if s.checker != nil && isBashTool(invocation.toolName) {
		command := mapStringArg(invocation.args, "command")
		if result := s.checker.Check(command, s.execCtx.HITLLevel); result.Intercepted {
			s.skipPostToolHook = true
			if strings.EqualFold(result.Rule.ViewportType, "builtin") && s.isRuleWhitelisted(result.Rule.RuleKey) {
				s.applyHITLDecision(invocation, result, "", "approve_prefix_run", "", true)
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.shouldAutoApproveHITL(result) {
				return s.executeOriginalBash(invocation)
			}
			return s.emitHITLConfirmDeltas(invocation, result)
		}
	}

	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	if result.SubmitInfo != nil {
		s.pending = append(s.pending, DeltaRequestSubmit{
			RequestID:  s.session.RequestID,
			ChatID:     s.session.ChatID,
			RunID:      s.session.RunID,
			AwaitingID: result.SubmitInfo.AwaitingID,
			Params:     result.SubmitInfo.Params,
		})
		if answer := frontendSubmitAwaitingAnswer(invocation, result); len(answer) > 0 {
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

func (s *llmRunStream) lookupBashSecurityReview(invocation *preparedToolInvocation) bashsec.ReviewResult {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return bashsec.ReviewResult{Decision: bashsec.ReviewAllow}
	}
	if invocation.bashSecurityReview != nil {
		return *invocation.bashSecurityReview
	}
	review := s.reviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
	if review.Decision == bashsec.ReviewRequiresApproval {
		cloned := review
		invocation.bashSecurityReview = &cloned
	}
	return review
}

func (s *llmRunStream) lookupFileWritePlan(invocation *preparedToolInvocation) *filetools.WritePlan {
	if invocation == nil || !isWriteTool(invocation.toolName) {
		return nil
	}
	if invocation.fileWritePlan != nil {
		return invocation.fileWritePlan
	}
	plan, err := filetools.BuildWritePlan(s.engine.cfg.FileTools, invocation.args)
	if err != nil {
		return nil
	}
	invocation.fileWritePlan = &plan
	return &plan
}

func (s *llmRunStream) reviewBashSecurity(command string) bashsec.ReviewResult {
	if s == nil || s.execCtx == nil || len(s.execCtx.RuntimeEnvOverrides) == 0 {
		return bashsec.ReviewBashSecurity(command)
	}
	return bashsec.ReviewBashSecurityWithKnownVariables(command, s.execCtx.RuntimeEnvOverrides)
}

func (s *llmRunStream) executeApprovedFileWriteInvocation(invocation *preparedToolInvocation, plan filetools.WritePlan) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_prefix_run":
		filetools.RegisterRuleWriteApproval(s.execCtx, plan.RuleKey)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	case "approve":
		filetools.RegisterExactWriteApproval(s.execCtx, plan.Fingerprint)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.emitFileWriteApprovalDeltas(invocation, plan)
	}
}

func (s *llmRunStream) executeApprovedBashSecurityInvocation(invocation *preparedToolInvocation, review bashsec.ReviewResult) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_prefix_run":
		s.registerRuleWhitelist(review.RuleKey)
		invocation.approvalDecision = ""
		s.registerBashSecurityApproval(review.Fingerprint)
		return s.executeOriginalBash(invocation)
	case "approve":
		invocation.approvalDecision = ""
		s.registerBashSecurityApproval(review.Fingerprint)
		return s.executeOriginalBash(invocation)
	default:
		return s.emitBashSecurityApprovalDeltas(invocation, review)
	}
}

func (s *llmRunStream) appendOriginalToolResult(invocation *preparedToolInvocation, result ToolExecutionResult) {
	result = applyHITLMetadata(result, invocation)
	s.previousToolResult = structuredOrOutput(result)
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
	s.messages = append(s.messages, openAIMessage{
		Role:       "tool",
		ToolCallID: invocation.toolID,
		Name:       invocation.toolName,
		Content:    s.toolResultContent(invocation.toolName, result),
	})
	if entry, ok := buildHITLNoticeEntry(invocation); ok {
		s.pendingHITLNotices = append(s.pendingHITLNotices, entry)
	}
	if len(s.queuedToolCalls) == 0 && len(s.pendingHITLNotices) > 0 {
		summary, approval := buildHITLBatchSummaryAndApproval(s.pendingHITLNotices)
		if summary != "" {
			s.messages = append(s.messages, openAIMessage{
				Role:    "user",
				Content: summary,
			})
		}
		if s.onApprovalSummary != nil && approval != nil {
			s.onApprovalSummary(*approval)
		}
		s.pendingHITLNotices = nil
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

func isBashTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "simple-bash":
		return true
	default:
		return false
	}
}

func isWriteTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "write")
}

func mapStringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
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
	if clientVisible, ok := tool.Meta["clientVisible"].(bool); ok && !clientVisible {
		return nil
	}
	if s.engine.frontend == nil {
		return nil
	}
	handler, ok := s.engine.frontend.Handler(toolName)
	if !ok {
		return nil
	}
	toolTimeout := s.resolveHITLTimeout()
	awaitAsk := handler.BuildInitialAwaitAsk(toolID, s.session.RunID, tool, payload, 0, toolTimeout)
	if s.runControl != nil && awaitAsk != nil {
		s.runControl.ExpectSubmit(awaitingContextFromStreamAsk(awaitAsk))
	}
	return nil
}

func cloneAgentDeltas(input []AgentDelta) []AgentDelta {
	if len(input) == 0 {
		return nil
	}
	out := make([]AgentDelta, 0, len(input))
	for _, delta := range input {
		switch value := delta.(type) {
		case DeltaAwaitAsk:
			cloned := value
			cloned.Questions = append([]any(nil), value.Questions...)
			cloned.Approvals = append([]any(nil), value.Approvals...)
			cloned.Forms = append([]any(nil), value.Forms...)
			out = append(out, cloned)
		default:
			out = append(out, delta)
		}
	}
	return out
}

func (s *llmRunStream) lookupToolDefinition(toolName string) (api.ToolDetailResponse, bool) {
	if s.checker != nil {
		if tool, ok := s.checker.Tool(toolName); ok {
			return tool, true
		}
	}
	for _, tool := range applyToolOverrides(s.engine.tools.Definitions(), s.execCtx.ToolOverrides) {
		if strings.EqualFold(strings.TrimSpace(tool.Name), strings.TrimSpace(toolName)) {
			return tool, true
		}
		if strings.EqualFold(strings.TrimSpace(tool.Key), strings.TrimSpace(toolName)) {
			return tool, true
		}
	}
	return api.ToolDetailResponse{}, false
}
