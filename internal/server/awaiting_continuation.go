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

type awaitingContinuationAdmission struct {
	summary      chat.Summary
	teamID       string
	agentKey     string
	teamSnapshot *catalog.TeamSnapshot
	agentDef     catalog.AgentDefinition
}

func (s *Server) resolveAwaitingContinuationAdmission(chatID string, requestedAgentKey string) (awaitingContinuationAdmission, error) {
	chatID = strings.TrimSpace(chatID)
	if s == nil || s.deps.Chats == nil || s.deps.Registry == nil {
		return awaitingContinuationAdmission{}, fmt.Errorf("continuation admission is not configured")
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return awaitingContinuationAdmission{}, err
	}
	if summary == nil {
		return awaitingContinuationAdmission{}, chat.ErrChatNotFound
	}
	teamID, agentKey, teamSnapshot, teamErr := resolveQueryTeam(
		s.deps.Registry,
		strings.TrimSpace(summary.TeamID),
		strings.TrimSpace(requestedAgentKey),
		summary,
	)
	if teamErr != nil {
		return awaitingContinuationAdmission{}, teamErr
	}
	var agentDef catalog.AgentDefinition
	var ok bool
	if teamSnapshot != nil {
		agentDef, ok = teamSnapshot.AgentDefinition(agentKey)
	} else {
		agentKey = firstNonBlank(agentKey, summary.AgentKey)
		agentDef, ok = s.deps.Registry.AgentDefinition(agentKey)
	}
	if !ok {
		return awaitingContinuationAdmission{}, fmt.Errorf("agent not found: %s", agentKey)
	}
	return awaitingContinuationAdmission{
		summary:      *summary,
		teamID:       teamID,
		agentKey:     agentKey,
		teamSnapshot: teamSnapshot,
		agentDef:     agentDef,
	}, nil
}

func (s *Server) startAwaitingContinuation(deferred DeferredAwaiting, submitReq api.SubmitRequest, answer map[string]any) (bool, error) {
	return s.startAwaitingContinuationWithAdmission(deferred, submitReq, answer, nil)
}

func (s *Server) startAwaitingContinuationWithAdmission(
	deferred DeferredAwaiting,
	submitReq api.SubmitRequest,
	answer map[string]any,
	admission *awaitingContinuationAdmission,
) (bool, error) {
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
	if admission == nil {
		resolved, err := s.resolveAwaitingContinuationAdmission(chatID, submitReq.AgentKey)
		if err != nil {
			return false, err
		}
		admission = &resolved
	} else if admission.summary.ChatID != "" && strings.TrimSpace(admission.summary.ChatID) != chatID {
		return false, fmt.Errorf("continuation admission chatId does not match")
	}
	summary := admission.summary
	teamID := admission.teamID
	agentKey := admission.agentKey
	teamSnapshot := admission.teamSnapshot
	agentDef := admission.agentDef

	originalQuery, _ := s.deps.Chats.LoadRunQuery(chatID, sourceRunID)
	planMarkdown := s.awaitingContinuationPlanMarkdown(chatID, mode)
	planDecision := agentcoder.PlanContinuationDecision(mode, answer)
	planApprove := agentcoder.IsMode(agentDef.Mode) && planDecision == "approve"
	planReject := agentcoder.IsMode(agentDef.Mode) && planDecision == "reject"
	newExecutionRun := planApprove && strings.TrimSpace(runID) != strings.TrimSpace(sourceRunID)
	continuationInput := coderContinuationRequestInput(originalQuery, submitReq, summary, agentDef, mode, answer, planMarkdown)
	req := agentcoder.BuildContinuationRequest(continuationInput)
	if planApprove {
		req = agentcoder.BuildPlanApproveContinuationRequest(continuationInput)
	} else if planReject {
		planningMode := true
		req.PlanningMode = &planningMode
	}
	// The chat's team is fixed and the selected member definition was frozen
	// above. Do not allow persisted query fields to reintroduce a stale team or
	// agent after admission.
	req.TeamID = teamID
	req.AgentKey = agentKey
	session, err := s.BuildQuerySession(context.Background(), req, summary, agentDef, querySessionBuildOptions{
		Created:           false,
		Locale:            submitReq.Locale,
		IncludeHistory:    true,
		IncludeMemory:     true,
		AllowInvokeAgents: resolvedModeCapabilities(agentDef).InvokeChildren,
	})
	if err != nil {
		return false, err
	}
	if !isProxyAgentMode(agentDef.Mode) {
		applyQueryModelOptionsToSession(req.Model, &session)
	}
	if agentcoder.IsACPBackend(agentDef.Mode, agentDef.ACPBridgeID) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	var continuationSystem *chat.QueryLineSystemInit
	if planApprove {
		if err := s.preparePlanApproveContinuation(req, originalQuery, &session); err != nil {
			log.Printf("[server][awaiting] prepare plan approve continuation failed chatId=%s runId=%s err=%v", chatID, runID, err)
			return false, err
		}
		if newExecutionRun {
			req.SyntheticQueryBootstrapped = true
		}
	} else {
		if systemInitLine, err := s.prepareSystemInitCache(req, &session, false); err == nil {
			continuationSystem = systemInitLine
		} else {
			log.Printf("[server][awaiting] prepare continuation system init failed chatId=%s runId=%s err=%v", chatID, runID, err)
		}
	}
	session.HistoryMessages = awaitingContinuationHistory(session.HistoryMessages, sourceRunID, submitReq.AwaitingID, mode, answer)

	prepared := preparedQuery{
		req:          req,
		summary:      summary,
		created:      false,
		agentDef:     agentDef,
		teamSnapshot: teamSnapshot,
		session:      session,
		continueRun:  !newExecutionRun,
		initialSeq:   s.continuationInitialSeq(chatID, sourceRunID, runID),
	}
	if newExecutionRun {
		prepared.syntheticBootstrap = coderPlanApproveSyntheticBootstrap(session)
	} else if continuationSystem != nil {
		prepared.syntheticBootstrap = systemInitSyntheticBootstrap(session.ChatID, *continuationSystem)
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
		RunCtx:            runCtx,
		Request:           req,
		Session:           session,
		StartedAtMillis:   startedAt,
		Summary:           summary,
		Agent:             s.deps.Agent,
		Registry:          s.deps.Registry,
		TeamSnapshot:      teamSnapshot,
		Assembler:         assembler,
		Mapper:            mapper,
		Billing:           s.deps.Config.Billing,
		StepWriter:        stepWriter,
		EventBus:          eventBus,
		Chats:             s.deps.Chats,
		Models:            s.deps.Models,
		RunControl:        control,
		ResourceBaseURL:   "",
		ResourceTickets:   s.ticketService,
		BuildQuerySession: s.BuildQuerySession,
		PrepareSystemInit: s.prepareSystemInitCache,
		Notifications:     s.deps.Notifications,
		OnContinuation:    s.startRunContinuation,
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

func systemInitSyntheticBootstrap(chatID string, system chat.QueryLineSystemInit) *stream.SyntheticQuery {
	stage := ""
	if _, parsedStage, ok := strings.Cut(strings.TrimSpace(system.CacheKey), ":"); ok {
		stage = strings.TrimSpace(parsedStage)
	}
	return &stream.SyntheticQuery{
		ChatID: chatID,
		Role:   api.QueryRoleSystem,
		System: map[string]any{
			"agentKey":       system.AgentKey,
			"cacheKey":       system.CacheKey,
			"fingerprint":    system.Fingerprint,
			"systemMessage":  cloneMap(system.SystemMessage),
			"tools":          cloneAnySlice(system.Tools),
			"model":          cloneMap(system.Model),
			"toolChoice":     system.ToolChoice,
			"requestOptions": cloneMap(system.RequestOptions),
		},
		Kind:   "system-init",
		Stage:  stage,
		Hidden: true,
	}
}

func (s *Server) continuationInitialSeq(chatID string, sourceRunID string, runID string) int64 {
	if strings.TrimSpace(sourceRunID) == "" || strings.TrimSpace(runID) == "" ||
		strings.TrimSpace(sourceRunID) != strings.TrimSpace(runID) {
		return 0
	}
	return s.persistedRunLiveSeq(chatID, runID)
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
	admission, _ := continuation.ContinuationState.(*awaitingContinuationAdmission)
	continued, err := s.startAwaitingContinuationWithAdmission(DeferredAwaiting{
		ChatID:     chatID,
		RunID:      sourceRunID,
		AwaitingID: submitReq.AwaitingID,
		Mode:       mode,
	}, submitReq, contracts.CloneMap(continuation.Answer), admission)
	if err != nil {
		return "", err
	}
	if !continued {
		return "", fmt.Errorf("continuation was not started")
	}
	return runID, nil
}

func coderContinuationRequestInput(original *chat.QueryLine, submitReq api.SubmitRequest, summary chat.Summary, agentDef catalog.AgentDefinition, mode string, answer map[string]any, planMarkdown string) agentcoder.ContinuationRequestInput {
	var originalRequest api.QueryRequest
	if original != nil && len(original.Query) > 0 {
		data, _ := json.Marshal(original.Query)
		_ = json.Unmarshal(data, &originalRequest)
	}
	return agentcoder.ContinuationRequestInput{
		Original:           originalRequest,
		Submit:             submitReq,
		SummaryChatID:      summary.ChatID,
		SummaryTeamID:      summary.TeamID,
		SummaryAgentKey:    summary.AgentKey,
		DefinitionAgentKey: agentDef.Key,
		Mode:               mode,
		Answer:             answer,
		PlanMarkdown:       planMarkdown,
	}
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
	profiles, err := s.deps.SystemInits.BuildSystemInitProfiles(contracts.SystemInitBuildInput{
		Session: planningSession, Request: profileReq, ToolDefinitions: s.deps.Tools.Definitions(),
	})
	if err != nil {
		return err
	}
	var executeSystem chat.QueryLineSystemInit
	for _, profile := range profiles {
		if strings.TrimSpace(profile.CacheKey) != agentcoder.ExecuteCacheKey {
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
	session.PendingSystemInitKeys = map[string]bool{executeSystem.CacheKey: true}
	if s.deps.Chats != nil {
		if systemInits, err := s.deps.Chats.LoadAllSystemInits(req.ChatID); err == nil {
			if existing := systemInits.Lookup(executeSystem.AgentKey, executeSystem.CacheKey); existing != nil && sameSystemInitPayload(existing, executeSystem) {
				session.PendingSystemInitKeys = nil
			}
		}
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
		Messages: cloneMessageMapsForSyntheticBootstrap(session.CurrentMessages),
		System:   contracts.TakePendingSystemInitPayload(&session, agentcoder.ExecuteCacheKey),
	}
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
