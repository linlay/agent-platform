package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
)

type frameOrchestrator struct {
	runCtx            context.Context
	request           api.QueryRequest
	session           contracts.QuerySession
	summary           chat.Summary
	agent             contracts.AgentEngine
	registry          catalog.Registry
	buildQuerySession func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	mapper            *llm.DeltaMapper
	emitDelta         func(contracts.AgentDelta)
	emitInputs        func(...stream.StreamInput)
	taskCounter       int
}

func (o *frameOrchestrator) Run(mainStream contracts.AgentStream) (bool, bool, error) {
	for {
		delta, nextErr := mainStream.Next()
		if errors.Is(nextErr, io.EOF) {
			return false, false, nil
		}
		if contracts.IsRunInterrupted(nextErr) {
			return false, true, nil
		}
		if nextErr != nil {
			return true, false, nextErr
		}

		invoke, ok := delta.(contracts.DeltaInvokeSubAgents)
		if !ok {
			o.emitDelta(delta)
			continue
		}
		if err := o.handleSubAgentBatch(mainStream, invoke); err != nil {
			return true, false, err
		}
	}
}

type childTaskResult struct {
	Index       int    `json:"-"`
	TaskID      string `json:"taskId"`
	TaskName    string `json:"taskName"`
	SubAgentKey string `json:"subAgentKey"`
	Status      string `json:"status"`
	Text        string `json:"text"`
	Error       string `json:"error,omitempty"`
}

type childRouteEvent struct {
	input  stream.StreamInput
	result *childTaskResult
}

type preparedSubTask struct {
	spec     contracts.SubAgentTaskSpec
	agentDef catalog.AgentDefinition
	taskID   string
}

func (o *frameOrchestrator) handleSubAgentBatch(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgents) error {
	main, ok := mainStream.(llm.OrchestratableAgentStream)
	if !ok {
		return fmt.Errorf("main agent stream does not support sub-agent orchestration")
	}
	if o.registry == nil || o.buildQuerySession == nil || o.mapper == nil {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is not configured")
		return nil
	}
	if !canUseInvokeAgentsTool(o.session.Mode) {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is only supported for REACT/ONESHOT main agents")
		return nil
	}
	if len(invoke.Tasks) < 1 || len(invoke.Tasks) > 3 {
		o.injectMainToolError(main, invoke.MainToolID, "invalid agent_invoke call: tasks must contain between 1 and 3 items")
		return nil
	}
	prepared := make([]preparedSubTask, 0, len(invoke.Tasks))
	for _, task := range invoke.Tasks {
		subAgentKey := strings.TrimSpace(task.SubAgentKey)
		taskText := strings.TrimSpace(task.TaskText)
		taskName := strings.TrimSpace(task.TaskName)
		if taskName == "" {
			taskName = subAgentKey
		}
		if subAgentKey == "" || taskText == "" {
			o.injectMainToolError(main, invoke.MainToolID, "invalid agent_invoke call: every task requires subAgentKey and task")
			return nil
		}
		agentDef, found := o.registry.AgentDefinition(subAgentKey)
		if !found {
			o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent not found: %s", subAgentKey))
			return nil
		}
		if !canUseInvokeAgentsTool(agentDef.Mode) {
			o.injectMainToolError(main, invoke.MainToolID, "sub-agent must be REACT/ONESHOT")
			return nil
		}
		if containsInvokeAgentsTool(agentDef.Tools) {
			o.injectMainToolError(main, invoke.MainToolID, "nested sub-agent invocation is not allowed")
			return nil
		}
		o.taskCounter++
		prepared = append(prepared, preparedSubTask{
			spec: contracts.SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
			},
			agentDef: agentDef,
			taskID:   fmt.Sprintf("t_%s_%d", strings.TrimSpace(o.session.RunID), o.taskCounter),
		})
	}

	groupID := strings.TrimSpace(invoke.GroupID)
	if groupID == "" {
		groupID = "group_" + strings.TrimSpace(invoke.MainToolID)
	}

	for _, task := range prepared {
		o.emitDelta(contracts.DeltaTaskLifecycle{
			Kind:        "start",
			TaskID:      task.taskID,
			RunID:       o.session.RunID,
			GroupID:     groupID,
			TaskName:    task.spec.TaskName,
			Description: task.spec.TaskText,
			SubAgentKey: task.spec.SubAgentKey,
			MainToolID:  invoke.MainToolID,
		})
	}

	var principal *Principal
	if strings.TrimSpace(o.session.Subject) != "" {
		principal = &Principal{Subject: o.session.Subject}
	}

	results := make([]childTaskResult, len(prepared))
	routedCh := make(chan childRouteEvent, 32)
	var wg sync.WaitGroup

	for index, task := range prepared {
		wg.Add(1)
		go func(index int, task preparedSubTask) {
			defer wg.Done()
			routedCh <- childRouteEvent{result: o.runChildTask(index, task, principal, func(input stream.StreamInput) {
				routedCh <- childRouteEvent{input: input}
			})}
		}(index, task)
	}
	go func() {
		wg.Wait()
		close(routedCh)
	}()

	for routed := range routedCh {
		if routed.input != nil && o.emitInputs != nil {
			o.emitInputs(routed.input)
		}
		if routed.result == nil {
			continue
		}
		results[routed.result.Index] = *routed.result
		terminalKind := "complete"
		if routed.result.Status == "failed" {
			terminalKind = "fail"
		} else if routed.result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{
			Kind:    terminalKind,
			TaskID:  routed.result.TaskID,
			GroupID: groupID,
			Status:  routed.result.Status,
		}
		if terminalKind == "fail" {
			lifecycle.Error = contracts.NewErrorPayload("sub_agent_failed", firstNonEmpty(routed.result.Error, routed.result.Text), contracts.ErrorScopeTask, contracts.ErrorCategorySystem, nil)
		}
		o.emitDelta(lifecycle)
	}

	aggregated, err := json.Marshal(results)
	if err != nil {
		o.injectMainToolError(main, invoke.MainToolID, err.Error())
		return nil
	}
	anyFailed := false
	for _, result := range results {
		if result.Status != "completed" {
			anyFailed = true
			break
		}
	}
	_ = main.InjectToolResult(invoke.MainToolID, string(aggregated), anyFailed)
	return nil
}

func (o *frameOrchestrator) runChildTask(index int, task preparedSubTask, principal *Principal, route func(stream.StreamInput)) *childTaskResult {
	result := &childTaskResult{
		Index:       index,
		TaskID:      task.taskID,
		TaskName:    task.spec.TaskName,
		SubAgentKey: task.spec.SubAgentKey,
		Status:      "completed",
	}

	subReq := o.request
	subReq.RequestID = newRunID()
	subReq.RunID = o.session.RunID
	subReq.AgentKey = task.spec.SubAgentKey
	subReq.Message = task.spec.TaskText

	subSession, err := o.buildQuerySession(o.runCtx, subReq, o.summary, task.agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    false,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
		Principal:         principal,
	})
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}

	subStream, err := o.agent.Stream(o.runCtx, subReq, subSession)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer subStream.Close()

	childMapper := o.mapper.CloneIsolated(task.taskID, o.session.ChatID)
	if childMapper == nil {
		result.Status = "failed"
		result.Text = "sub-agent delta mapper is unavailable"
		result.Error = result.Text
		return result
	}

	for {
		delta, nextErr := subStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			result.Status = "cancelled"
			result.Text = "sub-agent interrupted"
			return result
		}
		if nextErr != nil {
			result.Status = "failed"
			result.Text = nextErr.Error()
			result.Error = nextErr.Error()
			return result
		}

		switch value := delta.(type) {
		case contracts.DeltaInvokeSubAgents:
			result.Status = "failed"
			result.Text = "nested sub-agent invocation is not allowed"
			result.Error = result.Text
			return result
		case contracts.DeltaFinishReason, contracts.DeltaRunCancel:
			continue
		case contracts.DeltaError:
			result.Status = "failed"
			result.Text = errorMessage(value.Error)
			result.Error = result.Text
			return result
		default:
			for _, input := range childMapper.Map(delta) {
				route(routeChildStreamInput(task.taskID, input))
			}
		}
	}

	child, ok := subStream.(llm.OrchestratableAgentStream)
	if !ok {
		result.Status = "failed"
		result.Text = "sub-agent stream does not expose final assistant content"
		result.Error = result.Text
		return result
	}
	text, ok := child.FinalAssistantContent()
	if !ok || strings.TrimSpace(text) == "" {
		result.Status = "failed"
		result.Text = "sub-agent produced no final assistant content"
		result.Error = result.Text
		return result
	}
	result.Text = text
	return result
}

func (o *frameOrchestrator) injectMainToolError(main llm.OrchestratableAgentStream, toolID string, message string) {
	_ = main.InjectToolResult(toolID, message, true)
}

func containsInvokeAgentsTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		if strings.EqualFold(strings.TrimSpace(toolName), contracts.InvokeAgentsToolName) {
			return true
		}
	}
	return false
}

func routeChildStreamInput(taskID string, input stream.StreamInput) stream.StreamInput {
	switch value := input.(type) {
	case stream.ReasoningDelta:
		value.TaskID = taskID
		return value
	case stream.ContentDelta:
		value.TaskID = taskID
		return value
	case stream.ToolArgs:
		value.TaskID = taskID
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		if value.AwaitAsk != nil {
			awaitCopy := *value.AwaitAsk
			awaitCopy.AwaitingID = namespaceChildID(taskID, awaitCopy.AwaitingID)
			value.AwaitAsk = &awaitCopy
		}
		return value
	case stream.ToolEnd:
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.ToolResult:
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.ActionArgs:
		value.TaskID = taskID
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.ActionEnd:
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.ActionResult:
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.SourcePublish:
		value.TaskID = taskID
		value.PublishID = namespaceChildID(taskID, value.PublishID)
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.AwaitAsk:
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.RequestSubmit:
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.AwaitingAnswer:
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
		return value
	default:
		return input
	}
}

func namespaceChildID(taskID string, rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return ""
	}
	return taskID + ":" + rawID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errorMessage(payload map[string]any) string {
	if payload == nil {
		return "sub-agent failed"
	}
	if message, _ := payload["message"].(string); strings.TrimSpace(message) != "" {
		return message
	}
	return "sub-agent failed"
}
