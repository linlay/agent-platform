package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) startAwaitingContinuation(deferred DeferredAwaiting, submitReq api.SubmitRequest, answer map[string]any) (bool, error) {
	if s == nil || s.deps.Runs == nil || s.deps.Chats == nil || s.deps.Agent == nil || s.deps.Registry == nil {
		return false, nil
	}
	mode := strings.ToLower(firstNonBlank(deferred.Mode, stringValue(answer["mode"])))
	if !isContinuableDeferredAwaitingMode(mode) {
		return false, nil
	}
	runID := strings.TrimSpace(submitReq.RunID)
	if runID == "" {
		runID = strings.TrimSpace(deferred.RunID)
	}
	if runID == "" {
		return false, fmt.Errorf("runId is required")
	}
	if _, ok := s.deps.Runs.RunStatus(runID); ok {
		return true, nil
	}
	chatID := firstNonBlank(submitReq.ChatID, deferred.ChatID)
	if chatID == "" {
		return false, fmt.Errorf("chatId is required")
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return false, err
	}
	if summary == nil {
		return false, chat.ErrChatNotFound
	}
	agentKey := firstNonBlank(submitReq.AgentKey, summary.AgentKey)
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return false, fmt.Errorf("agent not found: %s", agentKey)
	}

	originalQuery, _ := s.deps.Chats.LoadRunQuery(chatID, runID)
	req := queryRequestForAwaitingContinuation(originalQuery, submitReq, *summary, agentDef, mode, answer, s.awaitingContinuationPlanMarkdown(chatID, mode))
	session, err := s.BuildQuerySession(context.Background(), req, *summary, agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    true,
		IncludeMemory:     true,
		AllowInvokeAgents: canUseInvokeAgentsTool(agentDef.Mode),
	})
	if err != nil {
		return false, err
	}
	applyQueryModelOptionsToSession(req.Model, &session)
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	if systemInitLines, err := s.prepareSystemInitCache(req, &session, false); err == nil {
		_ = systemInitLines
	} else {
		log.Printf("[server][awaiting] prepare continuation system init failed chatId=%s runId=%s err=%v", chatID, runID, err)
	}
	session.HistoryMessages = awaitingContinuationHistory(session.HistoryMessages, runID, submitReq.AwaitingID, mode, answer)

	prepared := preparedQuery{
		req:         req,
		summary:     *summary,
		created:     false,
		agentDef:    agentDef,
		session:     session,
		continueRun: true,
	}
	registered, statusErr := s.registerQueryRun(context.Background(), prepared)
	if statusErr != nil {
		return false, fmt.Errorf("%s", statusErr.message)
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(runID)
	if !ok {
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		return false, fmt.Errorf("run event bus unavailable")
	}
	s.broadcast("run.started", map[string]any{
		"runId":    runID,
		"chatId":   chatID,
		"agentKey": agentKey,
	})

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, chatID, runID, agentDef.Mode)
	startedAt := int64(0)
	if parsed, ok := chat.ParseRunIDMillis(runID); ok {
		startedAt = parsed
	}
	StartRunExecutor(RunExecutorParams{
		RunCtx:             runCtx,
		Request:            req,
		Session:            session,
		StartedAtMillis:    startedAt,
		Summary:            *summary,
		Agent:              s.deps.Agent,
		Registry:           s.deps.Registry,
		Assembler:          assembler,
		Mapper:             mapper,
		Stream:             s.deps.Config.Stream,
		Billing:            s.deps.Config.Billing,
		StepWriter:         stepWriter,
		EventBus:           eventBus,
		Chats:              s.deps.Chats,
		Models:             s.deps.Models,
		RunControl:         control,
		ResourceBaseURL:    "",
		ResourceTickets:    s.ticketService,
		BuildQuerySession:  s.BuildQuerySession,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnComplete: func(doneRunID string) {
			s.deps.Runs.Finish(doneRunID)
			s.broadcast("run.finished", map[string]any{
				"runId":  doneRunID,
				"chatId": chatID,
			})
		},
	})
	return true, nil
}

func queryRequestForAwaitingContinuation(original *chat.QueryLine, submitReq api.SubmitRequest, summary chat.Summary, agentDef catalog.AgentDefinition, mode string, answer map[string]any, planMarkdown string) api.QueryRequest {
	var req api.QueryRequest
	if original != nil && len(original.Query) > 0 {
		data, _ := json.Marshal(original.Query)
		_ = json.Unmarshal(data, &req)
	}
	req.ChatID = firstNonBlank(req.ChatID, submitReq.ChatID, summary.ChatID)
	req.RunID = firstNonBlank(submitReq.RunID, req.RunID)
	req.RequestID = firstNonBlank(req.RequestID, submitReq.SubmitID, req.RunID)
	req.AgentKey = firstNonBlank(submitReq.AgentKey, req.AgentKey, summary.AgentKey, agentDef.Key)
	req.TeamID = firstNonBlank(req.TeamID, summary.TeamID)
	req.Role = api.QueryRoleSystem
	if strings.EqualFold(mode, "plan") {
		planningMode := false
		req.PlanningMode = &planningMode
	}
	req.Message = awaitingContinuationPrompt(mode, submitReq.AwaitingID, answer, planMarkdown)
	if strings.TrimSpace(req.AccessLevel) == "" {
		req.AccessLevel = contracts.AccessLevelDefault
	}
	return req
}

func awaitingContinuationHistory(history []map[string]any, runID string, awaitingID string, mode string, answer map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(history)+1)
	for _, item := range history {
		out = append(out, contracts.CloneMap(item))
	}
	if historyHasToolResult(out, awaitingID) {
		return out
	}
	toolName := toolCallNameForAwaiting(out, awaitingID)
	if toolName == "" {
		return out
	}
	content, _ := json.Marshal(answer)
	out = append(out, map[string]any{
		"runId":        runID,
		"role":         "tool",
		"tool_call_id": awaitingID,
		"name":         toolName,
		"content":      string(content),
	})
	return out
}

func (s *Server) persistDeferredAwaitingToolAnswer(chatID string, runID string, awaitingID string, answer map[string]any, resolvedAt int64) error {
	if s == nil || s.deps.Chats == nil {
		return nil
	}
	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	awaitingID = strings.TrimSpace(awaitingID)
	if chatID == "" || runID == "" || awaitingID == "" || len(answer) == 0 {
		return nil
	}
	history, err := s.deps.Chats.LoadRawMessages(chatID, s.deps.Config.ChatStorage.K)
	if err != nil {
		return err
	}
	if historyHasToolResult(history, awaitingID) {
		return nil
	}
	toolName := toolCallNameForAwaiting(history, awaitingID)
	if toolName == "" {
		return nil
	}
	content, _ := json.Marshal(answer)
	return s.deps.Chats.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: resolvedAt,
		Type:      chat.StepLineTypeReactTool,
		Messages: []chat.StoredMessage{{
			Role:       "tool",
			Name:       toolName,
			ToolCallID: awaitingID,
			Content: []chat.ContentPart{{
				Type: "text",
				Text: string(content),
			}},
			Ts: &resolvedAt,
		}},
	})
}

func historyHasToolResult(history []map[string]any, awaitingID string) bool {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return false
	}
	for _, item := range history {
		if strings.TrimSpace(stringValue(item["role"])) != "tool" {
			continue
		}
		if strings.TrimSpace(stringValue(item["tool_call_id"])) == awaitingID {
			return true
		}
	}
	return false
}

func toolCallNameForAwaiting(history []map[string]any, awaitingID string) string {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return ""
	}
	for idx := len(history) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(stringValue(history[idx]["role"])) != "assistant" {
			continue
		}
		switch calls := history[idx]["tool_calls"].(type) {
		case []any:
			if name := toolCallNameFromAnySlice(calls, awaitingID); name != "" {
				return name
			}
		case []map[string]any:
			for _, call := range calls {
				if name := toolCallNameFromMap(call, awaitingID); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func toolCallNameFromAnySlice(calls []any, awaitingID string) string {
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		if name := toolCallNameFromMap(call, awaitingID); name != "" {
			return name
		}
	}
	return ""
}

func toolCallNameFromMap(call map[string]any, awaitingID string) string {
	if call == nil || strings.TrimSpace(stringValue(call["id"])) != awaitingID {
		return ""
	}
	fn, _ := call["function"].(map[string]any)
	return strings.TrimSpace(stringValue(fn["name"]))
}

func awaitingContinuationPrompt(mode string, awaitingID string, answer map[string]any, planMarkdown string) string {
	answerJSON, _ := json.MarshalIndent(answer, "", "  ")
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "plan":
		decision := strings.ToLower(strings.TrimSpace(stringValue(contracts.AnyMapNode(answer["plan"])["decision"])))
		prefix := "继续处理刚收到的计划确认结果，不要再次请求同一个计划确认。"
		if decision == "approve" {
			prefix = "用户已经批准计划。请基于已确认计划继续执行，不要再次请求确认。"
		} else if decision == "reject" {
			prefix = "用户已经拒绝计划。请根据反馈修订方案或给出下一步，不要执行被拒绝的计划。"
		}
		if strings.TrimSpace(planMarkdown) != "" {
			return strings.TrimSpace(prefix + "\n\nAwaiting ID: " + awaitingID + "\n\nPlan:\n" + planMarkdown + "\n\nAnswer:\n" + string(answerJSON))
		}
		return strings.TrimSpace(prefix + "\n\nAwaiting ID: " + awaitingID + "\n\nAnswer:\n" + string(answerJSON))
	default:
		return strings.TrimSpace("继续处理刚收到的等待项答案。不要重复提问同一个问题，直接根据答案继续完成原请求。\n\nAwaiting ID: " + awaitingID + "\n\nAnswer:\n" + string(answerJSON))
	}
}

func (s *Server) awaitingContinuationPlanMarkdown(chatID string, mode string) string {
	if !strings.EqualFold(strings.TrimSpace(mode), "plan") || s == nil || s.deps.Chats == nil {
		return ""
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil || detail.Planning == nil {
		return ""
	}
	return strings.TrimSpace(detail.Planning.Markdown)
}
