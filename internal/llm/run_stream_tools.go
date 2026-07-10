package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	"agent-platform/internal/stream"
	"agent-platform/internal/toolpolicy"
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

	if s.readOnlyToolDenied(toolCall.Function.Name) {
		result := toolpolicy.DisabledResult(toolCall.Function.Name)
		deltas, message := preparedToolResultMessage(toolID, toolCall.Function.Name, result, result.Output)
		return nil, deltas, message
	}

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

func (s *llmRunStream) invokeQueuedToolCallsAndPostHook() error {
	if len(s.queuedToolCalls) == 0 {
		return nil
	}
	if s.prepareQueuedBashApprovalBatch() {
		return nil
	}
	s.queuedToolCalls = s.prioritizeAwaitingToolCalls(s.queuedToolCalls)
	if !s.canInvokeQueuedToolCallsConcurrently(s.queuedToolCalls) {
		s.activateNextToolCall()
		return nil
	}
	invocations := append([]*preparedToolInvocation(nil), s.queuedToolCalls...)
	s.queuedToolCalls = nil
	return s.invokeToolCallBatchAndPostHooks(invocations)
}

func (s *llmRunStream) prioritizeAwaitingToolCalls(invocations []*preparedToolInvocation) []*preparedToolInvocation {
	if len(invocations) < 2 {
		return invocations
	}
	awaiting := make([]*preparedToolInvocation, 0)
	ready := make([]*preparedToolInvocation, 0, len(invocations))
	for _, invocation := range invocations {
		if s.invocationMayAwaitBeforeResult(invocation) {
			awaiting = append(awaiting, invocation)
			continue
		}
		ready = append(ready, invocation)
	}
	if len(awaiting) == 0 {
		return invocations
	}
	out := make([]*preparedToolInvocation, 0, len(invocations))
	out = append(out, awaiting...)
	out = append(out, ready...)
	return out
}

func (s *llmRunStream) invocationMayAwaitBeforeResult(invocation *preparedToolInvocation) bool {
	if invocation == nil {
		return false
	}
	if strings.TrimSpace(invocation.approvalDecision) != "" {
		return false
	}
	if s.isFrontendTool(invocation.toolName) {
		return true
	}
	if isPlanningWriteTool(invocation.toolName) {
		return true
	}
	if request, ok := s.approvalRequestForInvocation(invocation); ok {
		if handled, _ := s.tryResolveApprovalFastPath(request, approvalFastPathSkipBatch); !handled {
			return true
		}
	}
	if isBashTool(invocation.toolName) {
		if result := s.lookupPrecheckedHITL(invocation); result.Intercepted {
			request := hitlApprovalRequest(invocation, result)
			if handled, _ := s.tryResolveHITLApprovalFastPath(request, approvalFastPathSkipBatch); !handled {
				return true
			}
		}
	}
	return false
}

func (s *llmRunStream) isFrontendTool(toolName string) bool {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return false
	}
	toolKind, _ := tool.Meta["kind"].(string)
	sourceType, _ := tool.Meta["sourceType"].(string)
	return !strings.EqualFold(strings.TrimSpace(sourceType), "mcp") &&
		strings.EqualFold(strings.TrimSpace(toolKind), "frontend")
}

func (s *llmRunStream) canInvokeQueuedToolCallsConcurrently(invocations []*preparedToolInvocation) bool {
	runnable := 0
	for _, invocation := range invocations {
		if invocation == nil {
			return false
		}
		if invocation.queuedResult != nil {
			continue
		}
		if !s.canInvokeToolConcurrently(invocation) {
			return false
		}
		runnable++
	}
	return runnable > 1
}

func (s *llmRunStream) canInvokeToolConcurrently(invocation *preparedToolInvocation) bool {
	if invocation == nil || invocation.awaitExternalResult || len(invocation.prelude) > 0 {
		return false
	}
	if strings.TrimSpace(invocation.approvalDecision) != "" {
		return false
	}
	if isPlanningWriteTool(invocation.toolName) || isPlanTool(invocation.toolName) {
		return false
	}
	if !s.isConcurrentToolName(invocation.toolName) {
		return false
	}
	if s.invocationMayConsumeOneShotApproval(invocation) {
		return false
	}
	if _, ok := s.approvalRequestForInvocation(invocation); ok {
		return false
	}
	if isBashTool(invocation.toolName) {
		if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewBlock {
			return false
		}
		if result := s.lookupPrecheckedHITL(invocation); result.Intercepted {
			return false
		}
	}
	return true
}

func (s *llmRunStream) isConcurrentToolName(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash":
		return s != nil && s.execCtx != nil &&
			!s.session.AgentHasRuntimeSandbox &&
			!s.execCtx.Session.AgentHasRuntimeSandbox
	case "datetime", "regex", "file_read", "file_grep", "file_glob", "web_fetch":
		return true
	default:
		return false
	}
}

func (s *llmRunStream) invocationMayConsumeOneShotApproval(invocation *preparedToolInvocation) bool {
	if s == nil || s.execCtx == nil || invocation == nil {
		return false
	}
	if isBashTool(invocation.toolName) {
		return len(s.execCtx.BashSecurityApprovals) > 0 || len(s.execCtx.AccessPolicyApprovals) > 0
	}
	switch strings.ToLower(strings.TrimSpace(invocation.toolName)) {
	case "file_read", "file_grep", "file_glob":
		return len(s.execCtx.FileReadApprovals) > 0
	case "file_write", "file_edit":
		return len(s.execCtx.FileAccessApprovals) > 0 || len(s.execCtx.FileWriteApprovals) > 0
	default:
		return false
	}
}

type batchToolCallResult struct {
	index      int
	invocation *preparedToolInvocation
	result     ToolExecutionResult
	execCtx    *ExecutionContext
	err        error
	received   bool
}

type activeToolBatch struct {
	invocations []*preparedToolInvocation
	results     []batchToolCallResult
	resultCh    chan batchToolCallResult
	remaining   int
}

func (s *llmRunStream) invokeToolCallBatchAndPostHooks(invocations []*preparedToolInvocation) error {
	return s.startToolCallBatch(invocations)
}

func (s *llmRunStream) startToolCallBatch(invocations []*preparedToolInvocation) error {
	results := make([]batchToolCallResult, len(invocations))
	resultCh := make(chan batchToolCallResult, len(invocations))
	remaining := 0

	for index, invocation := range invocations {
		result := batchToolCallResult{index: index, invocation: invocation}
		results[index].invocation = invocation
		if invocation == nil {
			continue
		}
		remaining++
		s.beginToolInvocation(invocation)
		if invocation.queuedResult != nil {
			result.result = *invocation.queuedResult
			invocation.queuedResult = nil
			resultCh <- result
			continue
		}
		if result := s.checkBudgetBeforeToolCall(invocation.toolName); result != nil {
			resultCh <- batchToolCallResult{index: index, invocation: invocation, result: *result}
			continue
		}
		s.recordAccessPolicyAutoApproval(invocation)
		execCtx := s.concurrentExecutionContext(invocation)
		results[index].execCtx = execCtx
		go func(index int, invocation *preparedToolInvocation, execCtx *ExecutionContext) {
			result, err := s.invokeToolForBatch(invocation, execCtx)
			resultCh <- batchToolCallResult{
				index:      index,
				invocation: invocation,
				result:     result,
				execCtx:    execCtx,
				err:        err,
			}
		}(index, invocation, execCtx)
	}
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}

	s.activeToolBatch = &activeToolBatch{
		invocations: append([]*preparedToolInvocation(nil), invocations...),
		results:     results,
		resultCh:    resultCh,
		remaining:   remaining,
	}
	return nil
}

func (s *llmRunStream) consumeActiveToolBatch() error {
	batch := s.activeToolBatch
	if batch == nil {
		return nil
	}
	result, err := s.awaitActiveToolBatchResult(batch)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if errors.Is(result.err, ErrRunInterrupted) {
		return s.handleInterruptIfNeeded()
	}
	if result.err != nil {
		return result.err
	}
	if result.index < 0 || result.index >= len(batch.results) {
		return fmt.Errorf("tool batch result index out of range: %d", result.index)
	}
	invocation := result.invocation
	if invocation == nil {
		batch.remaining--
		if batch.remaining == 0 {
			return s.finalizeActiveToolBatch(batch)
		}
		return nil
	}

	prepared := *result
	prepared.result = s.prepareToolResultForPublish(invocation, result.result)
	prepared.received = true
	batch.results[result.index] = prepared
	batch.remaining--

	s.appendFrontendSubmitDeltas(invocation, prepared.result)
	s.emitToolResultLive(invocation, prepared.result)
	appendKBaseSourcePublishDelta(&s.pending, s.session, invocation, prepared.result)
	appendPublishedArtifactDelta(&s.pending, s.session, prepared.result.Structured["publishedArtifacts"])

	if batch.remaining == 0 {
		return s.finalizeActiveToolBatch(batch)
	}
	return nil
}

func (s *llmRunStream) awaitActiveToolBatchResult(batch *activeToolBatch) (*batchToolCallResult, error) {
	if batch == nil || batch.remaining == 0 {
		return nil, nil
	}
	var done <-chan struct{}
	if s != nil && s.ctx != nil {
		done = s.ctx.Done()
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-batch.resultCh:
			return &result, nil
		case <-done:
			return nil, s.ctx.Err()
		case <-ticker.C:
			if err := s.handleInterruptIfNeeded(); err != nil || len(s.pending) > 0 {
				return nil, err
			}
		}
	}
}

func (s *llmRunStream) finalizeActiveToolBatch(batch *activeToolBatch) error {
	if batch == nil {
		return nil
	}
	for index, result := range batch.results {
		invocation := result.invocation
		if invocation == nil {
			continue
		}
		if !result.received {
			return fmt.Errorf("tool batch completed without result for %s", invocation.toolID)
		}
		s.mergeConcurrentExecutionContext(result.execCtx)
		s.queuedToolCalls = remainingInvocations(batch.invocations, index+1)
		s.appendToolResultMessageOrdered(invocation, result.result)
		if s.postToolHook != nil && s.postToolHook(invocation.toolName, invocation.toolID) == PostToolStop {
			s.stopAfterToolBatch = true
		}
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(invocation.toolID)
		}
	}
	s.queuedToolCalls = nil
	s.activeToolBatch = nil
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}
	return nil
}

func remainingInvocations(invocations []*preparedToolInvocation, start int) []*preparedToolInvocation {
	if start >= len(invocations) {
		return nil
	}
	out := make([]*preparedToolInvocation, 0, len(invocations)-start)
	for _, invocation := range invocations[start:] {
		if invocation != nil {
			out = append(out, invocation)
		}
	}
	return out
}

func (s *llmRunStream) invokeToolForBatch(invocation *preparedToolInvocation, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if invocation == nil {
		return ToolExecutionResult{}, nil
	}
	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return ToolExecutionResult{}, invokeErr
		}
		return ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}, nil
	}
	return result, nil
}

func (s *llmRunStream) concurrentExecutionContext(invocation *preparedToolInvocation) *ExecutionContext {
	if s == nil || s.execCtx == nil {
		return &ExecutionContext{
			CurrentToolID:   invocation.toolID,
			CurrentToolName: invocation.toolName,
			RunLoopState:    RunLoopStateToolExecuting,
		}
	}
	cloned := *s.execCtx
	cloned.CurrentToolID = invocation.toolID
	cloned.CurrentToolName = invocation.toolName
	cloned.RunLoopState = RunLoopStateToolExecuting
	cloned.RuntimeEnvOverrides = CloneStringMap(s.execCtx.RuntimeEnvOverrides)
	cloned.AccessPolicyApprovals = cloneIntMap(s.execCtx.AccessPolicyApprovals)
	cloned.AccessPolicyRuleApprovals = cloneBoolMap(s.execCtx.AccessPolicyRuleApprovals)
	cloned.BashSecurityApprovals = cloneIntMap(s.execCtx.BashSecurityApprovals)
	cloned.FileReadApprovals = cloneIntMap(s.execCtx.FileReadApprovals)
	cloned.FileReadRuleApprovals = cloneBoolMap(s.execCtx.FileReadRuleApprovals)
	cloned.FileAccessApprovals = cloneIntMap(s.execCtx.FileAccessApprovals)
	cloned.FileAccessRuleApprovals = cloneBoolMap(s.execCtx.FileAccessRuleApprovals)
	cloned.FileWriteApprovals = cloneIntMap(s.execCtx.FileWriteApprovals)
	cloned.FileWriteRuleApprovals = cloneBoolMap(s.execCtx.FileWriteRuleApprovals)
	cloned.ReadFileState = cloneReadFileState(s.execCtx.ReadFileState)
	return &cloned
}

func (s *llmRunStream) mergeConcurrentExecutionContext(execCtx *ExecutionContext) {
	if s == nil || s.execCtx == nil || execCtx == nil || len(execCtx.ReadFileState) == 0 {
		return
	}
	if s.execCtx.ReadFileState == nil {
		s.execCtx.ReadFileState = map[string]ReadFileSnapshot{}
	}
	for path, snapshot := range execCtx.ReadFileState {
		s.execCtx.ReadFileState[path] = snapshot
	}
}

func cloneIntMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneReadFileState(values map[string]ReadFileSnapshot) map[string]ReadFileSnapshot {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]ReadFileSnapshot, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
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
	appendKBaseSourcePublishDelta(&s.pending, s.session, invocation, result)
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

func appendKBaseSourcePublishDelta(pending *[]AgentDelta, session QuerySession, invocation *preparedToolInvocation, result ToolExecutionResult) {
	if pending == nil || invocation == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(invocation.toolName), "kbase_search") {
		return
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Error) != "" {
		return
	}

	hits := kbaseSearchHits(result.Structured["results"])
	if len(hits) == 0 {
		return
	}
	sources := kbaseSourcePublishSources(hits)
	if len(sources) == 0 {
		return
	}

	query := strings.TrimSpace(AnyStringNode(result.Structured["query"]))
	if query == "" {
		query = strings.TrimSpace(mapStringArg(invocation.args, "query"))
	}

	*pending = append(*pending, DeltaSourcePublish{
		RunID:   session.RunID,
		ToolID:  invocation.toolID,
		Kind:    "kbase",
		Query:   query,
		Sources: sources,
	})
}

func kbaseSearchHits(raw any) []kbase.SearchHit {
	switch typed := raw.(type) {
	case []kbase.SearchHit:
		hits := make([]kbase.SearchHit, 0, len(typed))
		for _, hit := range typed {
			if normalized, ok := normalizeKBaseSearchHit(hit); ok {
				hits = append(hits, normalized)
			}
		}
		return hits
	case []map[string]any:
		hits := make([]kbase.SearchHit, 0, len(typed))
		for _, item := range typed {
			if hit, ok := kbaseSearchHitFromMap(item); ok {
				hits = append(hits, hit)
			}
		}
		return hits
	case []any:
		hits := make([]kbase.SearchHit, 0, len(typed))
		for _, item := range typed {
			if hit, ok := kbaseSearchHitFromAny(item); ok {
				hits = append(hits, hit)
			}
		}
		return hits
	default:
		if hit, ok := kbaseSearchHitFromAny(raw); ok {
			return []kbase.SearchHit{hit}
		}
		return nil
	}
}

func kbaseSearchHitFromAny(raw any) (kbase.SearchHit, bool) {
	switch typed := raw.(type) {
	case kbase.SearchHit:
		return normalizeKBaseSearchHit(typed)
	case map[string]any:
		return kbaseSearchHitFromMap(typed)
	default:
		return kbase.SearchHit{}, false
	}
}

func kbaseSearchHitFromMap(item map[string]any) (kbase.SearchHit, bool) {
	if len(item) == 0 {
		return kbase.SearchHit{}, false
	}
	return normalizeKBaseSearchHit(kbase.SearchHit{
		ChunkID:    strings.TrimSpace(AnyStringNode(item["chunkId"])),
		Path:       strings.TrimSpace(AnyStringNode(item["path"])),
		Heading:    strings.TrimSpace(AnyStringNode(item["heading"])),
		StartLine:  AnyIntNode(item["startLine"]),
		EndLine:    AnyIntNode(item["endLine"]),
		PageStart:  AnyIntNode(item["pageStart"]),
		PageEnd:    AnyIntNode(item["pageEnd"]),
		SlideStart: AnyIntNode(item["slideStart"]),
		SlideEnd:   AnyIntNode(item["slideEnd"]),
		SourceType: strings.TrimSpace(AnyStringNode(item["sourceType"])),
		Snippet:    strings.TrimSpace(AnyStringNode(item["snippet"])),
		Score:      anyFloat64(item["score"]),
		MatchType:  strings.TrimSpace(AnyStringNode(item["matchType"])),
	})
}

func normalizeKBaseSearchHit(hit kbase.SearchHit) (kbase.SearchHit, bool) {
	hit.ChunkID = strings.TrimSpace(hit.ChunkID)
	hit.Path = strings.TrimSpace(hit.Path)
	hit.Heading = strings.TrimSpace(hit.Heading)
	hit.SourceType = strings.TrimSpace(hit.SourceType)
	hit.Snippet = strings.TrimSpace(hit.Snippet)
	hit.MatchType = strings.TrimSpace(hit.MatchType)
	if hit.ChunkID == "" && hit.Path == "" && hit.Snippet == "" {
		return kbase.SearchHit{}, false
	}
	return hit, true
}

func kbaseSourcePublishSources(hits []kbase.SearchHit) []stream.Source {
	if len(hits) == 0 {
		return nil
	}

	sourceIndexes := map[string]int{}
	sources := make([]stream.Source, 0, len(hits))
	for index, hit := range hits {
		key := hit.Path
		if key == "" {
			key = hit.ChunkID
		}
		if strings.TrimSpace(key) == "" {
			continue
		}

		sourceIndex, ok := sourceIndexes[key]
		if !ok {
			name := filepath.Base(hit.Path)
			if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
				name = key
			}
			title := hit.Path
			if title == "" {
				title = hit.Heading
			}
			sources = append(sources, stream.Source{
				ID:             "kbase:" + key,
				Name:           name,
				Title:          title,
				Icon:           "kbase",
				CollectionName: "KBASE",
			})
			sourceIndex = len(sources) - 1
			sourceIndexes[key] = sourceIndex
		}

		content := hit.Snippet
		if strings.TrimSpace(content) == "" {
			content = hit.Heading
		}
		sources[sourceIndex].Chunks = append(sources[sourceIndex].Chunks, stream.SourceChunk{
			ChunkID:    hit.ChunkID,
			Index:      index + 1,
			Content:    content,
			Score:      hit.Score,
			Path:       hit.Path,
			Heading:    hit.Heading,
			StartLine:  hit.StartLine,
			EndLine:    hit.EndLine,
			PageStart:  hit.PageStart,
			PageEnd:    hit.PageEnd,
			SlideStart: hit.SlideStart,
			SlideEnd:   hit.SlideEnd,
			SourceType: hit.SourceType,
			MatchType:  hit.MatchType,
		})
	}
	return sources
}

func anyFloat64(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		number, _ := typed.Float64()
		return number
	case string:
		number, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number
	default:
		return 0
	}
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
	result = s.prepareToolResultForPublish(invocation, result)
	s.emitToolResultLive(invocation, result)
	s.appendToolResultMessageOrdered(invocation, result)
}

func (s *llmRunStream) prepareToolResultForPublish(invocation *preparedToolInvocation, result ToolExecutionResult) ToolExecutionResult {
	result = applyHITLMetadata(result, invocation)
	return s.maybeSpillToolResult(invocation, result)
}

func (s *llmRunStream) emitToolResultLive(invocation *preparedToolInvocation, result ToolExecutionResult) {
	if invocation == nil {
		return
	}
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
}

func (s *llmRunStream) appendToolResultMessageOrdered(invocation *preparedToolInvocation, result ToolExecutionResult) {
	if invocation == nil {
		return
	}
	s.previousToolResult = structuredOrOutput(result)
	content := s.toolResultContent(invocation.toolName, result)
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
	return IsPlanTaskToolName(name)
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

func (s *llmRunStream) readOnlyToolDenied(toolName string) bool {
	if s == nil || s.execCtx == nil || !IsReadOnlyToolExecutionPolicy(s.execCtx.ToolExecutionPolicy) {
		return false
	}
	def, found := s.lookupToolDefinition(toolName)
	return !toolpolicy.AllowsReadOnly(def, found)
}
