package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type frameOrchestrator struct {
	runCtx             context.Context
	request            api.QueryRequest
	session            contracts.QuerySession
	summary            chat.Summary
	agent              contracts.AgentEngine
	registry           catalog.Registry
	buildQuerySession  func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	chats              chat.Store
	resourceBaseURL    string
	resourceTickets    *ResourceTicketService
	prepareSystemInits func(api.QueryRequest, *contracts.QuerySession, bool) ([]chat.QueryLineSystemInit, error)
	buildChildSystems  func(api.QueryRequest, *contracts.QuerySession) []chat.QueryLineSystemInit
	systemInitMu       sync.Mutex
	mapper             contracts.StreamDeltaMapper
	emitDelta          func(contracts.AgentDelta)
	emitInputs         func(...stream.StreamInput)
	taskCounter        int
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
	spec       contracts.SubAgentTaskSpec
	agentDef   catalog.AgentDefinition
	taskID     string
	requestID  string
	subTaskID  string
	mainToolID string
}

func (o *frameOrchestrator) handleSubAgentBatch(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgents) error {
	main, ok := mainStream.(contracts.OrchestratableAgentStream)
	if !ok {
		return fmt.Errorf("main agent stream does not support sub-agent orchestration")
	}
	if o.registry == nil || o.buildQuerySession == nil || o.mapper == nil {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is not configured")
		return nil
	}
	if !canUseInvokeAgentsTool(o.session.Mode) {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is only supported for REACT/ONESHOT/CODER main agents")
		return nil
	}
	if len(invoke.Tasks) < 1 || len(invoke.Tasks) > contracts.MaxInvokeAgentTasks {
		o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("invalid agent_invoke call: tasks must contain between 1 and %d items", contracts.MaxInvokeAgentTasks))
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
		if !canUseInvokeAgentsTool(agentDef.Mode) && !isProxyAgentMode(agentDef.Mode) {
			o.injectMainToolError(main, invoke.MainToolID, "sub-agent must be REACT/ONESHOT/CODER/PROXY")
			return nil
		}
		if !catalog.AgentInvocable(agentDef) {
			o.injectMainToolError(main, invoke.MainToolID, "sub-agent is not invocable")
			return nil
		}
		if containsInvokeAgentsTool(agentDef.Tools) {
			o.injectMainToolError(main, invoke.MainToolID, "nested sub-agent invocation is not allowed")
			return nil
		}
		o.taskCounter++
		taskIndex := o.taskCounter
		parentReqID := strings.TrimSpace(o.session.RequestID)
		requestID := fmt.Sprintf("sub_%d", taskIndex)
		if parentReqID != "" {
			requestID = fmt.Sprintf("%s_sub_%d", parentReqID, taskIndex)
		}
		subTaskID := fmt.Sprintf("sub_%d", taskIndex)
		prepared = append(prepared, preparedSubTask{
			spec: contracts.SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       append([]string(nil), task.Files...),
			},
			agentDef:  agentDef,
			taskID:    fmt.Sprintf("%s_t_%d", strings.TrimSpace(o.session.RunID), taskIndex),
			requestID: requestID,
			subTaskID: subTaskID,
		})
	}

	for index := range prepared {
		prepared[index].mainToolID = invoke.MainToolID
	}

	for _, task := range prepared {
		o.emitDelta(contracts.DeltaTaskLifecycle{
			Kind:        "start",
			TaskID:      task.taskID,
			RunID:       o.session.RunID,
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
			terminalKind = "error"
		} else if routed.result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{
			Kind:   terminalKind,
			TaskID: routed.result.TaskID,
		}
		if terminalKind == "error" {
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
	subReq.RequestID = task.requestID
	subReq.RunID = o.session.RunID
	subReq.AgentKey = task.spec.SubAgentKey
	subReq.Message = task.spec.TaskText
	if strings.TrimSpace(subReq.Role) == "" {
		subReq.Role = "user"
	}
	references, err := prepareProxyReferences(o.chats, o.resourceTickets, proxyReferenceOptions{
		ChatID:          subReq.ChatID,
		RunID:           subReq.RunID,
		Subject:         o.session.Subject,
		ResourceBaseURL: o.resourceBaseURL,
		References:      subReq.References,
		Files:           task.spec.Files,
	})
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	subReq.References = references

	subSession, err := o.buildQuerySession(o.runCtx, subReq, o.summary, task.agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    false,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
		SubTaskID:         task.subTaskID,
		Principal:         principal,
	})
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	if len(subSession.RuntimeContext.References) > 0 {
		subReq.References = subSession.RuntimeContext.References
	}
	o.writeChildTaskQueryAndSystem(subReq, &subSession, task)

	if isProxyAgentMode(task.agentDef.Mode) {
		return o.runProxyChildTask(result, subReq, subSession.WorkspaceRoot, task, route)
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

	child, ok := subStream.(contracts.OrchestratableAgentStream)
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
	route(routeChildStreamInput(task.taskID, stream.ContentDelta{
		ContentID: task.taskID + ":final",
		TaskID:    task.taskID,
		Delta:     text,
	}))
	result.Text = text
	return result
}

func (o *frameOrchestrator) runProxyChildTask(result *childTaskResult, subReq api.QueryRequest, workspaceRoot string, task preparedSubTask, route func(stream.StreamInput)) *childTaskResult {
	proxy := task.agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent missing proxyConfig.baseUrl"
		result.Error = result.Text
		return result
	}

	targetURL := strings.TrimRight(proxy.BaseURL, "/") + "/api/query"
	payload := map[string]any{
		"agentKey":   proxyAgentKey(proxy, subReq.AgentKey),
		"message":    subReq.Message,
		"references": subReq.References,
	}
	if chatID := strings.TrimSpace(proxy.ChatID); chatID != "" {
		payload["chatId"] = chatID
	}
	if params := proxyForwardParams(subReq, workspaceRoot); params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}

	timeout := time.Duration(proxy.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(o.runCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(proxy.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(proxy.Token))
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		result.Status = "failed"
		result.Text = fmt.Sprintf("PROXY sub-agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		result.Error = result.Text
		return result
	}

	var assistantText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		event, ok := parseProxySSEDataLine(scanner.Text())
		if !ok {
			continue
		}
		switch event.Type {
		case "content.delta":
			delta, _ := event.Payload["delta"].(string)
			if delta == "" {
				continue
			}
			assistantText.WriteString(delta)
			contentID, _ := event.Payload["contentId"].(string)
			if strings.TrimSpace(contentID) == "" {
				contentID = task.taskID + ":proxy"
			} else {
				contentID = namespaceChildID(task.taskID, contentID)
			}
			route(routeChildStreamInput(task.taskID, stream.ContentDelta{
				ContentID: contentID,
				TaskID:    task.taskID,
				Delta:     delta,
			}))
		case "run.error":
			result.Status = "failed"
			result.Text = errorMessage(event.Payload)
			result.Error = result.Text
			return result
		case "run.cancel":
			result.Status = "cancelled"
			result.Text = "sub-agent cancelled"
			return result
		case "run.complete":
			result.Text = strings.TrimSpace(assistantText.String())
			if result.Text == "" {
				result.Status = "failed"
				result.Text = "PROXY sub-agent returned run.complete without assistant content"
				result.Error = result.Text
			}
			return result
		}
	}
	if err := scanner.Err(); err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	result.Text = strings.TrimSpace(assistantText.String())
	if result.Text == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent returned an empty SSE stream"
		result.Error = result.Text
	}
	return result
}

func parseProxySSEDataLine(line string) (stream.EventData, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return stream.EventData{}, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == stream.DoneSentinel {
		return stream.EventData{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return stream.EventData{}, false
	}
	if nested, ok := raw["event"].(map[string]any); ok {
		raw = nested
	}
	event := stream.EventDataFromMap(raw)
	return event, strings.TrimSpace(event.Type) != ""
}

func (o *frameOrchestrator) writeChildTaskQueryAndSystem(subReq api.QueryRequest, subSession *contracts.QuerySession, task preparedSubTask) {
	if o.chats == nil {
		return
	}
	var systems []chat.QueryLineSystemInit
	if subSession != nil && (o.prepareSystemInits != nil || o.buildChildSystems != nil) {
		o.systemInitMu.Lock()
		defer o.systemInitMu.Unlock()
		if o.prepareSystemInits != nil {
			_, _ = o.prepareSystemInits(subReq, subSession, false)
		}
		if o.buildChildSystems != nil {
			systems = o.buildChildSystems(subReq, subSession)
		}
	}
	_ = o.chats.AppendQueryLine(o.summary.ChatID, chat.QueryLine{
		Type:        "query",
		ChatID:      o.summary.ChatID,
		RunID:       o.session.RunID,
		UpdatedAt:   time.Now().UnixMilli(),
		TaskID:      task.taskID,
		TaskName:    task.spec.TaskName,
		TaskToolID:  task.mainToolID,
		SubAgentKey: task.spec.SubAgentKey,
		Query: map[string]any{
			"message":   task.spec.TaskText,
			"agentKey":  task.spec.SubAgentKey,
			"chatId":    o.summary.ChatID,
			"runId":     o.session.RunID,
			"requestId": task.requestID,
			"role":      "user",
		},
		Messages: currentMessagesFromSession(subSession),
		Systems:  systems,
	})
}

func currentMessagesFromSession(session *contracts.QuerySession) []map[string]any {
	if session == nil {
		return nil
	}
	return session.CurrentMessages
}

func (o *frameOrchestrator) injectMainToolError(main contracts.OrchestratableAgentStream, toolID string, message string) {
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

func isProxyAgentMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "PROXY")
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
	case stream.InputDebugLLMChat:
		value.TaskID = taskID
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
	if message := firstPayloadString(payload, "message", "error", "reason", "detail", "msg"); message != "" {
		return message
	}
	for _, key := range []string{"error", "rawEvent"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if message := firstPayloadString(nested, "message", "error", "reason", "detail", "msg"); message != "" {
				return message
			}
		}
	}
	if data, err := json.Marshal(payload); err == nil && len(data) > 0 {
		return "sub-agent failed: " + string(data)
	}
	return "sub-agent failed"
}

func firstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := payload[key].(string)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
