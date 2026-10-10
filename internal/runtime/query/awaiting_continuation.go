package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/adapter"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/runexec"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

func (s *Service) startAwaitingContinuation(deferred DeferredAwaiting, submitReq queryinput.SubmitRequest, answer map[string]any) (bool, error) {
	return s.startAwaitingContinuationWithAdmission(deferred, submitReq, answer, nil, nil)
}

func deferredAwaitingAnswerToolCall(step *chat.PersistedAwaitingStep, publicAwaitingID string) (chat.PersistedAwaitingToolCall, bool) {
	if step == nil {
		return chat.PersistedAwaitingToolCall{}, false
	}
	rawAwaitingID := strings.TrimSpace(publicAwaitingID)
	if taskID := strings.TrimSpace(step.TaskID); taskID != "" {
		rawAwaitingID = runexec.RawAwaitingIDForTask(taskID, rawAwaitingID)
	}
	for _, call := range step.ToolCalls {
		if strings.TrimSpace(call.ID) == rawAwaitingID && strings.TrimSpace(call.Name) != "" {
			return call, true
		}
	}
	if len(step.ToolCalls) == 1 && strings.TrimSpace(step.ToolCalls[0].Name) != "" {
		return step.ToolCalls[0], true
	}
	return chat.PersistedAwaitingToolCall{}, false
}

func (s *Service) startAwaitingContinuationWithAdmission(
	deferred DeferredAwaiting,
	submitReq queryinput.SubmitRequest,
	answer map[string]any,
	admission *awaitingContinuationAdmission,
	recovered *contracts.RecoveredAwaitingRun,
) (bool, error) {
	if s == nil || s.deps.Runs == nil || s.deps.Chats == nil || s.deps.Agent == nil || s.deps.Registry == nil {
		return false, nil
	}
	mode := strings.ToLower(firstNonBlank(deferred.Mode, sessionbuild.StringValue(answer["mode"])))
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
	if recovered == nil {
		if _, ok := s.deps.Runs.RunStatus(runID); ok {
			return true, nil
		}
	} else if strings.TrimSpace(recovered.Run.RunID) != sourceRunID || strings.TrimSpace(recovered.AwaitingID) != strings.TrimSpace(submitReq.AwaitingID) {
		return false, fmt.Errorf("recovered awaiting claim does not match continuation")
	}
	if recovered != nil && recovered.Control == nil {
		return false, fmt.Errorf("recovered awaiting control is unavailable")
	}
	if recovered != nil && recovered.EventBus == nil {
		return false, fmt.Errorf("recovered awaiting event bus is unavailable")
	}
	if recovered != nil && recovered.Control.Interrupted() {
		return false, contracts.ErrRunInterrupted
	}
	if recovered != nil && recovered.Control.Finished() {
		return false, contracts.ErrRunFinished
	}
	if recovered == nil {
		// New registrations keep the existing continuation behavior.
	} else if _, ok := s.deps.Runs.RunStatus(sourceRunID); !ok {
		return false, fmt.Errorf("recovered awaiting run is unavailable")
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
	} else if admission.Summary.ChatID != "" && strings.TrimSpace(admission.Summary.ChatID) != chatID {
		return false, fmt.Errorf("continuation admission chatId does not match")
	}
	summary := admission.Summary
	teamID := admission.TeamID
	agentKey := admission.AgentKey
	teamSnapshot := admission.TeamSnapshot
	agentDef := admission.AgentDef
	var releaseRuntime func()
	var runtimeFound bool
	if admission.Frozen {
		if admission.TeamSnapshot != nil {
			frozen, release, ok := catalogview.AcquireTeamSnapshot(s.deps.Registry, *admission.TeamSnapshot)
			teamSnapshot = &frozen
			releaseRuntime = release
			runtimeFound = ok
		} else {
			agentDef, releaseRuntime, runtimeFound = catalogview.AcquireAgentSnapshot(s.deps.Registry, agentDef)
		}
	} else if admission.TeamSnapshot != nil {
		leasedTeam, release, ok := catalogview.AcquireTeam(s.deps.Registry, admission.TeamSnapshot.TeamID)
		releaseRuntime, runtimeFound = release, ok
		if ok {
			teamSnapshot = &leasedTeam
			agentDef, runtimeFound = leasedTeam.AgentDefinition(agentDef.Key)
		}
	} else {
		agentDef, releaseRuntime, runtimeFound = catalogview.AcquireAgent(s.deps.Registry, agentDef.Key)
	}
	transferredRuntime := false
	defer func() {
		if !transferredRuntime {
			releaseQuery(releaseRuntime)
		}
	}()
	if !runtimeFound {
		return false, fmt.Errorf("Agent runtime is unavailable")
	}

	originalQuery, err := s.deps.Chats.LoadRunQuery(chatID, sourceRunID)
	if err != nil {
		return false, err
	}
	continuationStartedAt := int64(0)
	if strings.TrimSpace(runID) == strings.TrimSpace(sourceRunID) {
		continuationStartedAt, err = s.persistedContinuationStartedAt(chatID, sourceRunID)
		if err != nil {
			if statusErr := timeContractStatusError(err); statusErr != nil {
				return false, statusErr
			}
			return false, err
		}
	}
	planningMarkdown := s.awaitingContinuationPlanningMarkdown(chatID, mode)
	planningDecision := agentbuiltin.PlanContinuationDecision(mode, answer)
	nativePlanning := agentbuiltin.NativePlanning(agentDef.Mode, agentDef.ACPBridgeID)
	planningApprove := nativePlanning && planningDecision == "approve"
	planningReject := nativePlanning && planningDecision == "reject"
	newExecutionRun := planningApprove && strings.TrimSpace(runID) != strings.TrimSpace(sourceRunID)
	continuationInput := planContinuationRequestInput(originalQuery, submitReq, summary, agentDef, mode, answer, planningMarkdown)
	req := adapter.QueryCommand(agentbuiltin.BuildPlanContinuationRequest(continuationInput))
	if planningApprove {
		req = adapter.QueryCommand(agentbuiltin.BuildConfirmedPlanRequest(continuationInput))
	} else if planningReject {
		planningMode := true
		req.PlanningMode = &planningMode
	}
	// The chat's team is fixed and the selected member definition was frozen
	// above. Do not allow persisted query fields to reintroduce a stale team or
	// agent after admission.
	req.TeamID = teamID
	req.AgentKey = agentKey
	// Prefer private state; older Runs may still have a query snapshot.
	frozen, restoreErr := s.RestoredInteractionPolicy(sourceRunID, agentDef.Mode, originalQuery)
	if restoreErr != nil {
		return false, restoreErr
	}
	if frozen != nil {
		agentDef.InteractionConfig = frozen
	}
	if !newExecutionRun {
		if err := s.RestoreRunConnectors(sourceRunID, &agentDef); err != nil {
			return false, err
		}
	}
	locale := submitReq.Locale
	if newExecutionRun {
		var err error
		locale, err = s.deps.Sessions.RunPromptLocale(sourceRunID)
		if err != nil {
			return false, err
		}
	}
	session, err := s.deps.Sessions.BuildQuerySession(context.Background(), req, summary, agentDef, sessionbuild.Options{
		DisableSkillScriptGrants: !newExecutionRun,
		Created:                  false,
		Locale:                   locale,
		IncludeHistory:           true,
		IncludeMemory:            true,
		AllowInvokeAgents:        sessionbuild.ResolvedModeCapabilities(agentDef).InvokeChildren,
	})
	if err != nil {
		return false, err
	}
	if !sessionbuild.IsProxyAgentMode(agentDef.Mode) {
		ApplyQueryModelOptionsToSession(req.Model, &session)
	}
	if agentbuiltin.IsCoderACPBackend(agentDef.Mode, agentDef.ACPBridgeID) {
		req.Model = s.acpCoderModelOptions(session, req.Model)
	}
	if continuationStartedAt != 0 {
		session.StartedAtMillis = continuationStartedAt
	}
	if newExecutionRun {
		session.WebClientTarget = resolveRunWebClientTarget(s.deps.Runs, sourceRunID)
	}
	var continuationSystem *chat.QueryLineSystem
	var confirmedPlanBootstrap *stream.SyntheticQuery
	if planningApprove {
		confirmedPlanSystem, err := s.prepareConfirmedPlanRun(req, originalQuery, &session)
		if err != nil {
			log.Printf("[server][awaiting] prepare confirmed plan run failed chatId=%s runId=%s err=%v", chatID, runID, err)
			return false, err
		}
		if newExecutionRun {
			req.SyntheticQueryBootstrapped = true
			confirmedPlanBootstrap = confirmedPlanSyntheticBootstrap(session, confirmedPlanSystem)
		} else if confirmedPlanSystem != nil {
			// Without a new Run the engine writes the synthetic query itself and
			// needs the initial system-init left pending.
			if session.PendingSystemInitKeys == nil {
				session.PendingSystemInitKeys = map[string]bool{}
			}
			session.PendingSystemInitKeys[confirmedPlanSystem.CacheKey] = true
		}
	} else {
		if systemInitLine, err := s.deps.Sessions.PrepareSystemInitCache(req, &session, false); err == nil {
			continuationSystem = systemInitLine
		} else {
			log.Printf("[server][awaiting] prepare continuation system init failed chatId=%s runId=%s err=%v", chatID, runID, err)
			return false, err
		}
	}
	if mode == "wait" {
		checkpoint, err := decodeWaitCheckpoint(answer["waitCheckpoint"])
		if err != nil {
			return false, err
		}
		session.WaitResume = &checkpoint
		session.ResolvedBudget = checkpoint.Budget
		if scope, err := s.runControlScopes().Load(sourceRunID); err == nil {
			session.Subject = scope.Subject
		}
		if status, ok := s.deps.Runs.RunStatus(sourceRunID); ok {
			session.RunOrigin = status.RunOrigin
		}
		session.CurrentMessages = nil
	}
	session.HistoryMessages = awaitingContinuationHistory(session.HistoryMessages, sourceRunID, submitReq.AwaitingID, answer)

	initialSeq := s.continuationInitialSeq(chatID, sourceRunID, runID)
	if recovered != nil && strings.TrimSpace(runID) == sourceRunID {
		initialSeq = recovered.EventBus.LatestSeq()
	}
	prepared := preparedQuery{
		Req:          req,
		Summary:      summary,
		Created:      false,
		AgentDef:     agentDef,
		TeamSnapshot: teamSnapshot,
		Session:      session,
		ContinueRun:  !newExecutionRun,
		InitialSeq:   initialSeq,
	}
	if newExecutionRun {
		prepared.SyntheticBootstrap = confirmedPlanBootstrap
	} else if continuationSystem != nil {
		prepared.SyntheticBootstrap = systemInitSyntheticBootstrap(session.ChatID, *continuationSystem)
	}
	var registered registeredQueryRun
	var eventBus *stream.RunEventBus
	if recovered != nil && !newExecutionRun {
		status, ok := s.deps.Runs.RunStatus(sourceRunID)
		if !ok {
			return false, fmt.Errorf("recovered awaiting status is unavailable")
		}
		registered = registeredQueryRun{
			RunCtx: recovered.Context, Control: recovered.Control, Managed: true, StartedAtMillis: status.StartedAt,
		}
		eventBus = recovered.EventBus
		if runs, ok := s.deps.Runs.(contracts.RecoveredAwaitingRunService); !ok || !runs.ActivateRecoveredAwaiting(sourceRunID, submitReq.AwaitingID) {
			return false, fmt.Errorf("activate recovered awaiting run")
		}
	} else {
		if recovered != nil && newExecutionRun {
			if err := s.completeRecoveredPlanningRun(deferred, recovered); err != nil {
				return false, err
			}
		}
		var statusErr *statusError
		owner, ownerErr := s.runControlScopes().Load(sourceRunID)
		if ownerErr != nil {
			return false, ownerErr
		}
		registered, statusErr = s.RegisterPreparedQuery(controlscope.WithContext(context.Background(), owner), prepared)
		if statusErr != nil {
			return false, statusErr
		}
		var eventBusOK bool
		eventBus, eventBusOK = s.deps.Runs.EventBus(runID)
		if !eventBusOK {
			s.deps.Runs.Interrupt(serverSetupInterruptRequest(req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
			s.FinishRegisteredQuery(prepared, registered)
			return false, fmt.Errorf("run event bus unavailable")
		}
		s.broadcast("run.started", runStartedPushPayload(runID, chatID, agentKey, registered.StartedAtMillis))
	}
	runCtx, control := registered.RunCtx, registered.Control

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, chatID, runID, agentDef.Mode)
	transferredRuntime = true
	runexec.StartNative(runexec.NativeOptions{
		RunCtx:            runCtx,
		Request:           req,
		Session:           session,
		StartedAtMillis:   registered.StartedAtMillis,
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
		BuildQuerySession: s.deps.Sessions.BuildQuerySession,
		PrepareSystemInit: s.deps.Sessions.PrepareSystemInitCache,
		Notifications:     s.deps.Notifications,
		OnContinuation: func(c contracts.DeltaRunContinuation) (string, error) {
			c.ContinuationState = &awaitingContinuationAdmission{Summary: summary, TeamID: teamID, AgentKey: agentKey, TeamSnapshot: teamSnapshot, AgentDef: agentDef, Frozen: true}
			return s.startRunContinuation(c)
		},
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		Release: releaseRuntime,
		OnComplete: func(completion chat.RunCompletion) {
			s.finishRunConnectorPins(completion.RunID, chatID)
			s.deps.Runs.Finish(completion.RunID)
			s.broadcast("run.finished", runFinishedPushPayload(
				completion.RunID,
				chatID,
				completion.FinishReason,
				completion.UpdatedAtMillis,
			))
		},
	})
	return true, nil
}

func (s *Service) persistedContinuationStartedAt(chatID string, runID string) (int64, error) {
	if s == nil || s.deps.Chats == nil {
		return 0, &timecontract.Violation{
			Field:    "startedAt",
			Location: "awaiting.continuation.startedAt",
			Reason:   "registered run start is required",
		}
	}
	reader, ok := s.deps.Chats.(chat.RunStartReader)
	if !ok || reader == nil {
		return 0, &timecontract.Violation{
			Field:    "startedAt",
			Location: "awaiting.continuation.startedAt",
			Reason:   "persisted run start reader is required",
		}
	}
	startedAt, err := reader.LoadRunStartedAt(chatID, runID)
	if errors.Is(err, chat.ErrRunNotFound) {
		return 0, &timecontract.Violation{
			Field:    "startedAt",
			Location: "awaiting.continuation.startedAt",
			Reason:   "registered run start is required",
		}
	}
	if err != nil {
		return 0, err
	}
	if err := timecontract.ValidateEpochMillis(startedAt, "startedAt", "awaiting.continuation.startedAt"); err != nil {
		return 0, err
	}
	return startedAt, nil
}

func toolCallNameFromMap(call map[string]any, awaitingID string) string {
	if call == nil || strings.TrimSpace(sessionbuild.StringValue(call["id"])) != awaitingID {
		return ""
	}
	fn, _ := call["function"].(map[string]any)
	return strings.TrimSpace(sessionbuild.StringValue(fn["name"]))
}

func (s *Service) awaitingContinuationPlanningMarkdown(chatID string, mode string) string {
	if !strings.EqualFold(strings.TrimSpace(mode), "planning") || s == nil || s.deps.Chats == nil {
		return ""
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil || detail.Planning == nil {
		return ""
	}
	return strings.TrimSpace(detail.Planning.Markdown)
}

// prepareConfirmedPlanRun finishes the session of a Run started from a
// confirmed plan. BuildQuerySession already made it an ordinary Run with the
// execution tool exclusions; this prepares its system-init and makes the
// confirmed plan its only current message.
func (s *Service) prepareConfirmedPlanRun(req runtimetypes.QueryCommand, original *chat.QueryLine, session *contracts.QuerySession) (*chat.QueryLineSystem, error) {
	if session == nil || s == nil {
		return nil, nil
	}
	// The system prompt renders the user's original request, not the long
	// execution message that embeds the whole plan.
	profileReq := req
	if original != nil && len(original.Query) > 0 {
		if message := strings.TrimSpace(sessionbuild.StringValue(original.Query["message"])); message != "" {
			profileReq.Message = message
		}
	}
	system, err := s.deps.Sessions.PrepareSystemInitCache(profileReq, session, false)
	if err != nil {
		return nil, err
	}
	session.CurrentMessages = []map[string]any{{
		"role":    queryinput.QueryRoleUser,
		"content": req.Message,
	}}
	return system, nil
}

func systemInitSyntheticBootstrap(chatID string, system chat.QueryLineSystem) *stream.SyntheticQuery {
	stage := ""
	if _, parsedStage, ok := strings.Cut(strings.TrimSpace(system.CacheKey), ":"); ok {
		stage = strings.TrimSpace(parsedStage)
	}
	return &stream.SyntheticQuery{
		ChatID: chatID,
		Role:   queryinput.QueryRoleSystem,
		System: map[string]any{
			"agentKey":       system.AgentKey,
			"cacheKey":       system.CacheKey,
			"fingerprint":    system.Fingerprint,
			"systemMessage":  sessionbuild.CloneMap(system.SystemMessage),
			"tools":          sessionbuild.CloneAnySlice(system.Tools),
			"model":          sessionbuild.CloneMap(system.Model),
			"toolChoice":     system.ToolChoice,
			"requestOptions": sessionbuild.CloneMap(system.RequestOptions),
		},
		Kind:   "system-init",
		Stage:  stage,
		Hidden: true,
	}
}

func historyHasToolResult(history []map[string]any, awaitingID string) bool {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return false
	}
	for _, item := range history {
		if strings.TrimSpace(sessionbuild.StringValue(item["role"])) != "tool" {
			continue
		}
		if strings.TrimSpace(sessionbuild.StringValue(item["tool_call_id"])) == awaitingID {
			return true
		}
	}
	return false
}

func (s *Service) startRunContinuation(continuation contracts.DeltaRunContinuation) (string, error) {
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
	mode := firstNonBlank(continuation.Mode, sessionbuild.StringValue(continuation.Answer["mode"]))
	submitReq := queryinput.SubmitRequest{
		ChatID:            chatID,
		RunID:             sourceRunID,
		AgentKey:          strings.TrimSpace(continuation.AgentKey),
		TeamID:            strings.TrimSpace(continuation.TeamID),
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
	}, submitReq, contracts.CloneMap(continuation.Answer), admission, nil)
	if err != nil {
		return "", err
	}
	if !continued {
		return "", fmt.Errorf("continuation was not started")
	}
	return runID, nil
}

func (s *Service) PersistDeferredAwaitingToolAnswer(chatID string, runID string, awaitingID string, answer map[string]any, resolvedAt int64) error {
	if s == nil || s.deps.Chats == nil {
		return nil
	}
	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	awaitingID = strings.TrimSpace(awaitingID)
	if chatID == "" || runID == "" || awaitingID == "" || len(answer) == 0 {
		return nil
	}
	if reader, ok := s.deps.Chats.(chat.AwaitingRecoveryReader); ok {
		step, err := reader.LoadAwaitingStep(chatID, awaitingID)
		if err != nil {
			return err
		}
		if step != nil {
			call, ok := deferredAwaitingAnswerToolCall(step, awaitingID)
			if ok {
				if step.ResultToolIDs[call.ID] {
					return nil
				}
				content, _ := json.Marshal(answer)
				ts := resolvedAt
				return s.deps.Chats.AppendStepLine(chatID, chat.StepLine{
					ChatID:          chatID,
					RunID:           runID,
					UpdatedAt:       resolvedAt,
					TaskID:          step.TaskID,
					TaskStatus:      step.TaskStatus,
					TaskSubAgentKey: step.TaskSubAgentKey,
					TeamID:          step.TeamID,
					Presentation:    step.Presentation,
					Stage:           step.Stage,
					Seq:             step.Seq,
					Type:            chat.StepLineTypeReactTool,
					Messages: []chat.StoredMessage{{
						Role:       "tool",
						Name:       call.Name,
						ToolCallID: call.ID,
						ToolID:     call.ID,
						Content: []chat.ContentPart{{
							Type: "text",
							Text: string(content),
						}},
						Ts: &ts,
					}},
				})
			}
		}
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

func (s *Service) continuationInitialSeq(chatID string, sourceRunID string, runID string) int64 {
	if strings.TrimSpace(sourceRunID) == "" || strings.TrimSpace(runID) == "" ||
		strings.TrimSpace(sourceRunID) != strings.TrimSpace(runID) {
		return 0
	}
	return s.PersistedRunLiveSeq(chatID, runID)
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

func awaitingContinuationHistory(history []map[string]any, runID string, awaitingID string, answer map[string]any) []map[string]any {
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

func (s *Service) PersistedRunLiveSeq(chatID string, runID string) int64 {
	if s == nil || s.deps.Chats == nil {
		return 0
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return 0
	}
	return persistedLiveSeqCursor(detail.Events, runID)
}

func (s *Service) resolveAwaitingContinuationAdmission(chatID string, requestedAgentKey string) (awaitingContinuationAdmission, error) {
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
	teamID, agentKey, teamSnapshot, teamErr := ResolveQueryTeam(
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
		Summary:      *summary,
		TeamID:       teamID,
		AgentKey:     agentKey,
		TeamSnapshot: teamSnapshot,
		AgentDef:     agentDef,
	}, nil
}

func planContinuationRequestInput(original *chat.QueryLine, submitReq queryinput.SubmitRequest, summary chat.Summary, agentDef catalog.AgentDefinition, mode string, answer map[string]any, planningMarkdown string) agentbuiltin.PlanContinuationRequestInput {
	var originalRequest runtimetypes.QueryCommand
	if original != nil && len(original.Query) > 0 {
		data, _ := json.Marshal(original.Query)
		_ = json.Unmarshal(data, &originalRequest)
	}
	return agentbuiltin.PlanContinuationRequestInput{
		Original:           adapter.QueryRequest(originalRequest),
		Submit:             submitReq,
		SummaryChatID:      summary.ChatID,
		SummaryTeamID:      summary.TeamID,
		SummaryAgentKey:    summary.AgentKey,
		DefinitionAgentKey: agentDef.Key,
		Mode:               mode,
		Answer:             answer,
		PlanningMarkdown:   planningMarkdown,
	}
}

// confirmedPlanSyntheticBootstrap is the first persisted query of the new Run:
// a short visible message, the confirmed plan as the model message, and the
// Run's system-init when it is not already stored for this Chat.
func confirmedPlanSyntheticBootstrap(session contracts.QuerySession, system *chat.QueryLineSystem) *stream.SyntheticQuery {
	bootstrap := &stream.SyntheticQuery{
		ChatID:   session.ChatID,
		Role:     queryinput.QueryRoleUser,
		Message:  agentbuiltin.PlanExecuteSyntheticQueryMessage(session.Locale),
		Messages: cloneMessageMapsForSyntheticBootstrap(session.CurrentMessages),
	}
	if system != nil {
		bootstrap.System = systemInitSyntheticBootstrap(session.ChatID, *system).System
	}
	return bootstrap
}

func cloneMessageMapsForSyntheticBootstrap(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, sessionbuild.CloneMap(message))
	}
	return out
}

type awaitingContinuationAdmission struct {
	Frozen       bool
	Summary      chat.Summary
	TeamID       string
	AgentKey     string
	TeamSnapshot *catalog.TeamSnapshot
	AgentDef     catalog.AgentDefinition
}

func toolCallNameForAwaiting(history []map[string]any, awaitingID string) string {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return ""
	}
	for idx := len(history) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(sessionbuild.StringValue(history[idx]["role"])) != "assistant" {
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
