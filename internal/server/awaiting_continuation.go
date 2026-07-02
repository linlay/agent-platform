package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func (s *Server) startAwaitingContinuation(deferred DeferredAwaiting, submitReq api.SubmitRequest, answer map[string]any) (bool, error) {
	if s == nil || s.deps.Runs == nil || s.deps.Chats == nil || s.deps.Agent == nil || s.deps.Registry == nil {
		return false, nil
	}
	mode := strings.ToLower(firstNonBlank(deferred.Mode, stringValue(answer["mode"])))
	if !isContinuableDeferredAwaitingMode(mode) {
		return false, nil
	}
	sourceRunID := strings.TrimSpace(submitReq.RunID)
	if sourceRunID == "" {
		sourceRunID = strings.TrimSpace(deferred.RunID)
	}
	if sourceRunID == "" {
		return false, fmt.Errorf("runId is required")
	}
	runID := firstNonBlank(submitReq.ContinuationRunID, sourceRunID)
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

	originalQuery, _ := s.deps.Chats.LoadRunQuery(chatID, sourceRunID)
	planMarkdown := s.awaitingContinuationPlanMarkdown(chatID, mode)
	planDecision := planContinuationDecision(mode, answer)
	planApprove := agentcoder.IsMode(agentDef.Mode) && planDecision == "approve"
	planReject := agentcoder.IsMode(agentDef.Mode) && planDecision == "reject"
	newExecutionRun := planApprove && strings.TrimSpace(runID) != strings.TrimSpace(sourceRunID)
	req := queryRequestForAwaitingContinuation(originalQuery, submitReq, *summary, agentDef, mode, answer, planMarkdown)
	if planApprove {
		req = queryRequestForPlanApproveContinuation(originalQuery, submitReq, *summary, agentDef, planMarkdown)
	} else if planReject {
		planningMode := true
		req.PlanningMode = &planningMode
	}
	session, err := s.BuildQuerySession(context.Background(), req, *summary, agentDef, querySessionBuildOptions{
		Created:           false,
		Locale:            submitReq.Locale,
		IncludeHistory:    true,
		IncludeMemory:     true,
		AllowInvokeAgents: canUseInvokeAgentsTool(agentDef.Mode),
	})
	if err != nil {
		return false, err
	}
	if !isProxyAgentMode(agentDef.Mode) {
		applyQueryModelOptionsToSession(req.Model, &session)
	}
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	if planApprove {
		if err := s.preparePlanApproveContinuation(req, originalQuery, &session); err != nil {
			log.Printf("[server][awaiting] prepare plan approve continuation failed chatId=%s runId=%s err=%v", chatID, runID, err)
			return false, err
		}
		if newExecutionRun {
			req.SyntheticQueryBootstrapped = true
		}
	} else {
		if systemInitLines, err := s.prepareSystemInitCache(req, &session, false); err == nil {
			_ = systemInitLines
		} else {
			log.Printf("[server][awaiting] prepare continuation system init failed chatId=%s runId=%s err=%v", chatID, runID, err)
		}
	}
	session.HistoryMessages = awaitingContinuationHistory(session.HistoryMessages, sourceRunID, submitReq.AwaitingID, mode, answer)

	prepared := preparedQuery{
		req:         req,
		summary:     *summary,
		created:     false,
		agentDef:    agentDef,
		session:     session,
		continueRun: !newExecutionRun,
		initialSeq:  s.continuationInitialSeq(chatID, sourceRunID, runID),
	}
	if newExecutionRun {
		prepared.syntheticBootstrap = coderPlanApproveSyntheticBootstrap(session)
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
		OnContinuation:     s.startRunContinuation,
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

func (s *Server) continuationInitialSeq(chatID string, sourceRunID string, runID string) int64 {
	if strings.TrimSpace(sourceRunID) == "" || strings.TrimSpace(runID) == "" ||
		strings.TrimSpace(sourceRunID) != strings.TrimSpace(runID) {
		return 0
	}
	return s.persistedRunLiveSeq(chatID, runID)
}

func planContinuationDecision(mode string, answer map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(mode), "plan") {
		return ""
	}
	plan := contracts.AnyMapNode(answer["plan"])
	return strings.ToLower(strings.TrimSpace(stringValue(plan["decision"])))
}

func (s *Server) startRunContinuation(continuation contracts.DeltaRunContinuation) (string, error) {
	runID := strings.TrimSpace(continuation.RunID)
	if runID == "" {
		return "", fmt.Errorf("continuation runId is required")
	}
	sourceRunID := strings.TrimSpace(continuation.SourceRunID)
	if sourceRunID == "" {
		return "", fmt.Errorf("source runId is required")
	}
	chatID := strings.TrimSpace(continuation.ChatID)
	if chatID == "" {
		return "", fmt.Errorf("chatId is required")
	}
	mode := firstNonBlank(continuation.Mode, stringValue(continuation.Answer["mode"]))
	submitReq := api.SubmitRequest{
		ChatID:            chatID,
		RunID:             sourceRunID,
		AgentKey:          strings.TrimSpace(continuation.AgentKey),
		AwaitingID:        strings.TrimSpace(continuation.AwaitingID),
		SubmitID:          strings.TrimSpace(continuation.SubmitID),
		Locale:            strings.TrimSpace(continuation.Locale),
		Params:            continuation.Params,
		ContinuationRunID: runID,
	}
	continued, err := s.startAwaitingContinuation(DeferredAwaiting{
		ChatID:     chatID,
		RunID:      sourceRunID,
		AwaitingID: submitReq.AwaitingID,
		Mode:       mode,
	}, submitReq, contracts.CloneMap(continuation.Answer))
	if err != nil {
		return "", err
	}
	if !continued {
		return "", fmt.Errorf("continuation was not started")
	}
	return runID, nil
}

func queryRequestForAwaitingContinuation(original *chat.QueryLine, submitReq api.SubmitRequest, summary chat.Summary, agentDef catalog.AgentDefinition, mode string, answer map[string]any, planMarkdown string) api.QueryRequest {
	var req api.QueryRequest
	if original != nil && len(original.Query) > 0 {
		data, _ := json.Marshal(original.Query)
		_ = json.Unmarshal(data, &req)
	}
	req.ChatID = firstNonBlank(req.ChatID, submitReq.ChatID, summary.ChatID)
	req.RunID = firstNonBlank(submitReq.ContinuationRunID, submitReq.RunID, req.RunID)
	req.RequestID = firstNonBlank(submitReq.SubmitID, req.RequestID, req.RunID)
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

func queryRequestForPlanApproveContinuation(original *chat.QueryLine, submitReq api.SubmitRequest, summary chat.Summary, agentDef catalog.AgentDefinition, planMarkdown string) api.QueryRequest {
	var req api.QueryRequest
	if original != nil && len(original.Query) > 0 {
		data, _ := json.Marshal(original.Query)
		_ = json.Unmarshal(data, &req)
	}
	originalMessage := strings.TrimSpace(req.Message)
	req.ChatID = firstNonBlank(req.ChatID, submitReq.ChatID, summary.ChatID)
	req.RunID = firstNonBlank(submitReq.ContinuationRunID, submitReq.RunID, req.RunID)
	req.RequestID = firstNonBlank(submitReq.SubmitID, req.RequestID, req.RunID)
	req.AgentKey = firstNonBlank(submitReq.AgentKey, req.AgentKey, summary.AgentKey, agentDef.Key)
	req.TeamID = firstNonBlank(req.TeamID, summary.TeamID)
	req.Role = api.QueryRoleSystem
	planningMode := false
	req.PlanningMode = &planningMode
	req.Message = agentcoder.PlanApproveExecutePrompt(originalMessage, planMarkdown)
	req.Params = agentcoder.MarkPlanApproveContinuationParams(contracts.CloneMap(req.Params))
	if strings.TrimSpace(req.AccessLevel) == "" {
		req.AccessLevel = contracts.AccessLevelDefault
	}
	return req
}

func (s *Server) preparePlanApproveContinuation(req api.QueryRequest, original *chat.QueryLine, session *contracts.QuerySession) error {
	if session == nil || s == nil || s.deps.SystemInits == nil || s.deps.Tools == nil {
		return nil
	}
	profileReq := req
	if original != nil && len(original.Query) > 0 {
		if message := strings.TrimSpace(stringValue(original.Query["message"])); message != "" {
			profileReq.Message = message
		}
	}
	planningSession := *session
	planningSession.PlanningMode = true
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		planningSession,
		profileReq,
		s.deps.Tools.Definitions(),
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	var executeSystem chat.QueryLineSystemInit
	for _, profile := range profiles {
		if strings.TrimSpace(profile.CacheKey) != "coder:execute" {
			continue
		}
		executeSystem = queryLineSystemInitFromProfile(profile)
		break
	}
	if strings.TrimSpace(executeSystem.CacheKey) == "" || strings.TrimSpace(executeSystem.Fingerprint) == "" {
		return fmt.Errorf("coder execute system init profile unavailable")
	}
	session.PlanningMode = false
	session.SystemInitCache = map[string]contracts.SystemInitSnapshot{
		executeSystem.CacheKey: systemInitSnapshotFromLine(executeSystem),
	}
	executeTools := agentcoder.PlanningExecuteToolsForStage(session.ResolvedStageSettings.Execute, session.ToolNames)
	session.ToolNames = append([]string(nil), executeTools...)
	if modelKey := strings.TrimSpace(session.ResolvedStageSettings.Execute.ModelKey); modelKey != "" {
		session.ModelKey = modelKey
	}
	session.CurrentMessages = []map[string]any{{
		"role":    api.QueryRoleUser,
		"content": req.Message,
	}}
	return nil
}

func coderPlanApproveSyntheticBootstrap(session contracts.QuerySession) *stream.SyntheticQuery {
	return &stream.SyntheticQuery{
		ChatID:   session.ChatID,
		Role:     api.QueryRoleUser,
		Message:  agentcoder.ExecuteSyntheticQueryMessage(session.Locale),
		Stage:    "coder-execute",
		Source:   "coder-plan-approve",
		Messages: cloneMessageMapsForSyntheticBootstrap(session.CurrentMessages),
		Systems:  systemPayloadsFromSessionCache(session.SystemInitCache, "coder:execute"),
	}
}

func systemPayloadsFromSessionCache(cache map[string]contracts.SystemInitSnapshot, cacheKeys ...string) []map[string]any {
	if len(cache) == 0 || len(cacheKeys) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(cacheKeys))
	for _, cacheKey := range cacheKeys {
		cacheKey = strings.TrimSpace(cacheKey)
		snapshot, ok := cache[cacheKey]
		if !ok || strings.TrimSpace(snapshot.Fingerprint) == "" {
			continue
		}
		out = append(out, map[string]any{
			"cacheKey":       cacheKey,
			"fingerprint":    snapshot.Fingerprint,
			"systemMessage":  cloneMap(snapshot.SystemMessage),
			"tools":          cloneAnySlice(snapshot.Tools),
			"model":          cloneMap(snapshot.Model),
			"toolChoice":     snapshot.ToolChoice,
			"requestOptions": cloneMap(snapshot.RequestOptions),
		})
	}
	return out
}

func cloneMessageMapsForSyntheticBootstrap(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, cloneMap(message))
	}
	return out
}

func (s *Server) persistedRunLiveSeq(chatID string, runID string) int64 {
	if s == nil || s.deps.Chats == nil {
		return 0
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return 0
	}
	return persistedLiveSeqCursor(detail.Events, runID)
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
	history, err := s.deps.Chats.LoadRawMessages(chatID, chat.DefaultHistoryRunWindow)
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
