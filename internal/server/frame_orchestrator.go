package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
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

		invoke, ok := delta.(contracts.DeltaInvokeSubAgent)
		if !ok {
			o.emitDelta(delta)
			continue
		}
		if err := o.handleSubAgent(mainStream, invoke); err != nil {
			return true, false, err
		}
	}
}

func (o *frameOrchestrator) handleSubAgent(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgent) error {
	main, ok := mainStream.(llm.OrchestratableAgentStream)
	if !ok {
		return fmt.Errorf("main agent stream does not support sub-agent orchestration")
	}
	if o.registry == nil || o.buildQuerySession == nil {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is not configured")
		return nil
	}

	subAgentKey := strings.TrimSpace(invoke.SubAgentKey)
	agentDef, found := o.registry.AgentDefinition(subAgentKey)
	if !found {
		o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent not found: %s", subAgentKey))
		return nil
	}
	if !canUseInvokeAgentTool(o.session.Mode) {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is only supported for REACT/ONESHOT main agents")
		return nil
	}
	if !canUseInvokeAgentTool(agentDef.Mode) {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent must be REACT/ONESHOT")
		return nil
	}
	if containsInvokeAgentTool(agentDef.Tools) {
		o.injectMainToolError(main, invoke.MainToolID, "nested sub-agent invocation is not allowed")
		return nil
	}

	o.taskCounter++
	taskID := fmt.Sprintf("t_%s_%d", strings.TrimSpace(o.session.RunID), o.taskCounter)
	if o.mapper != nil {
		o.mapper.Snapshot()
	}
	o.emitDelta(contracts.DeltaTaskLifecycle{
		Kind:        "start",
		TaskID:      taskID,
		RunID:       o.session.RunID,
		TaskName:    invoke.TaskName,
		Description: invoke.TaskText,
		SubAgentKey: subAgentKey,
		MainToolID:  invoke.MainToolID,
	})

	subReq := o.request
	subReq.RequestID = newRunID()
	subReq.RunID = o.session.RunID
	subReq.AgentKey = subAgentKey
	subReq.Message = invoke.TaskText

	var principal *Principal
	if strings.TrimSpace(o.session.Subject) != "" {
		principal = &Principal{Subject: o.session.Subject}
	}
	subSession, err := o.buildQuerySession(o.runCtx, subReq, o.summary, agentDef, querySessionBuildOptions{
		Created:          false,
		IncludeHistory:   false,
		IncludeMemory:    false,
		AllowInvokeAgent: false,
		Principal:        principal,
	})
	if err != nil {
		o.finishSubTask(main, taskID, invoke.MainToolID, "fail", "error", err.Error())
		return nil
	}

	subStream, err := o.agent.Stream(o.runCtx, subReq, subSession)
	if err != nil {
		o.finishSubTask(main, taskID, invoke.MainToolID, "fail", "error", err.Error())
		return nil
	}
	defer subStream.Close()

	terminalKind := "complete"
	terminalStatus := "completed"
	finalText := ""
	finalErrText := ""

	for {
		delta, nextErr := subStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			terminalKind = "cancel"
			terminalStatus = "cancelled"
			finalErrText = "sub-agent interrupted"
			break
		}
		if nextErr != nil {
			terminalKind = "fail"
			terminalStatus = "error"
			finalErrText = nextErr.Error()
			break
		}

		switch value := delta.(type) {
		case contracts.DeltaInvokeSubAgent:
			terminalKind = "fail"
			terminalStatus = "error"
			finalErrText = "nested sub-agent invocation is not allowed"
		case contracts.DeltaFinishReason, contracts.DeltaRunCancel:
			continue
		case contracts.DeltaError:
			terminalKind = "fail"
			terminalStatus = "error"
			finalErrText = errorMessage(value.Error)
		default:
			o.emitDelta(delta)
			continue
		}
		break
	}

	if terminalKind == "complete" {
		child, ok := subStream.(llm.OrchestratableAgentStream)
		if !ok {
			terminalKind = "fail"
			terminalStatus = "error"
			finalErrText = "sub-agent stream does not expose final assistant content"
		} else if text, ok := child.FinalAssistantContent(); ok && strings.TrimSpace(text) != "" {
			finalText = text
		} else {
			terminalKind = "fail"
			terminalStatus = "error"
			finalErrText = "sub-agent produced no final assistant content"
		}
	}

	if terminalKind == "complete" {
		o.finishSubTask(main, taskID, invoke.MainToolID, terminalKind, terminalStatus, finalText)
		return nil
	}
	o.finishSubTask(main, taskID, invoke.MainToolID, terminalKind, terminalStatus, finalErrText)
	return nil
}

func (o *frameOrchestrator) finishSubTask(main llm.OrchestratableAgentStream, taskID string, mainToolID string, terminalKind string, status string, text string) {
	lifecycle := contracts.DeltaTaskLifecycle{
		Kind:   terminalKind,
		TaskID: taskID,
		Status: status,
	}
	if terminalKind == "fail" {
		lifecycle.Error = contracts.NewErrorPayload("sub_agent_failed", text, contracts.ErrorScopeTask, contracts.ErrorCategorySystem, nil)
	}
	o.emitDelta(lifecycle)
	if o.mapper != nil {
		o.mapper.RestoreActive()
	}
	_ = main.InjectToolResult(mainToolID, text, terminalKind != "complete")
}

func (o *frameOrchestrator) injectMainToolError(main llm.OrchestratableAgentStream, toolID string, message string) {
	_ = main.InjectToolResult(toolID, message, true)
}

func containsInvokeAgentTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		if strings.EqualFold(strings.TrimSpace(toolName), contracts.InvokeAgentToolName) {
			return true
		}
	}
	return false
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
