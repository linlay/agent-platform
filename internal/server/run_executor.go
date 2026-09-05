package server

import (
	"context"
	"log"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

type RunExecutorParams struct {
	RunCtx            context.Context
	Request           api.QueryRequest
	Session           contracts.QuerySession
	StartedAtMillis   int64
	Summary           chat.Summary
	Agent             contracts.AgentEngine
	Registry          catalog.Registry
	TeamSnapshot      *catalog.TeamSnapshot
	Assembler         *stream.StreamEventAssembler
	Mapper            contracts.StreamDeltaMapper
	Billing           config.BillingConfig
	StepWriter        *chat.StepWriter
	EventBus          *stream.RunEventBus
	Chats             chat.Store
	Models            *models.ModelRegistry
	RunControl        *contracts.RunControl
	ResourceBaseURL   string
	ResourceTickets   *ResourceTicketService
	BuildQuerySession func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	PrepareSystemInit func(api.QueryRequest, *contracts.QuerySession, bool) (*chat.QueryLineSystem, error)
	Notifications     contracts.NotificationSink
	OnUnreadChanged   func(chat.Summary)
	OnPersisted       func(chat.RunCompletion)
	OnContinuation    func(contracts.DeltaRunContinuation) (string, error)
	// OnComplete receives the same terminal record used for persistence. Callers
	// use it for run.finished so its time and status cannot drift from the run.
	OnComplete func(chat.RunCompletion)
}

type awaitingTracker struct {
	pendingAwaitingID string
	pendingMode       string
}

func completeCompactControl(runControl *contracts.RunControl, data stream.EventData) {
	if runControl == nil || (data.Type != "context.compact.complete" && data.Type != "context.compact.failed") {
		return
	}
	status := "failed"
	if data.Type == "context.compact.complete" {
		status = "completed"
	}
	compactionUsage, _ := data.Value("compactionUsage").(map[string]any)
	response := api.CompactResponse{
		Accepted:                   data.Type == "context.compact.complete",
		Status:                     status,
		RequestID:                  data.String("requestId"),
		ChatID:                     data.String("chatId"),
		RunID:                      data.String("runId"),
		CompactID:                  data.String("compactId"),
		Trigger:                    data.String("trigger"),
		Scope:                      data.String("scope"),
		Level:                      data.String("level"),
		SummarySource:              data.String("summarySource"),
		PreCompactEstimatedTokens:  contracts.AnyIntNode(data.Value("preCompactEstimatedTokens")),
		PostCompactEstimatedTokens: contracts.AnyIntNode(data.Value("postCompactEstimatedTokens")),
		CompressionRatio:           compactFloat64(data.Value("compressionRatio")),
		RemainingRatio:             compactFloat64(data.Value("remainingRatio")),
		ReleasedRatio:              compactFloat64(data.Value("releasedRatio")),
		TokensFreed:                contracts.AnyIntNode(data.Value("tokensFreed")),
		ToolsCleared:               contracts.AnyIntNode(data.Value("toolsCleared")),
		ToolsKept:                  contracts.AnyIntNode(data.Value("toolsKept")),
		CompactionUsage:            contracts.CloneMap(compactionUsage),
		Detail:                     data.String("detail"),
		Retryable:                  data.Value("retryable") == true,
	}
	runControl.CompleteCompact(response.RequestID, response)
}

func compactFloat64(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func shouldPublishClientEvent(data stream.EventData) bool {
	return stream.IsClientVisibleEventData(data)
}

func clientVisibleEventData(data stream.EventData) stream.EventData {
	if len(data.Payload) == 0 {
		return data
	}
	if data.Type != "request.query" && !strings.HasPrefix(data.Type, "context.compact.") {
		return data
	}
	payload := make(map[string]any, len(data.Payload))
	for key, value := range data.Payload {
		if key == "messages" || key == "system" || key == "checkpointMessages" || key == "previousRunState" || key == "awaitingId" {
			continue
		}
		payload[key] = value
	}
	data.Payload = payload
	return data
}

func StartRunExecutor(params RunExecutorParams) {
	go runExecutor(params)
}

func runExecutor(params RunExecutorParams) {
	tracker := &awaitingTracker{}
	var (
		persisted    bool
		completion   chat.RunCompletion
		continuation *contracts.DeltaRunContinuation
	)
	defer func() {
		maybeBroadcastInterruptedAwaiting(params, tracker)
		if params.StepWriter != nil {
			params.StepWriter.Flush()
		}
		if params.EventBus != nil {
			params.EventBus.FreezeAndWait()
		}
		if params.OnComplete != nil {
			params.OnComplete(completion)
		}
		if shouldStartRunContinuation(persisted, completion, continuation) && params.OnContinuation != nil {
			if _, err := params.OnContinuation(*continuation); err != nil {
				log.Printf("[server][run] start continuation failed sourceRunId=%s continuationRunId=%s err=%v", params.Session.RunID, continuation.RunID, err)
			}
		}
		if persisted {
			broadcastRunCompletion(params, completion)
		}
	}()
	if err := timecontract.ValidateEpochMillis(params.StartedAtMillis, "startedAt", "run.executor"); err != nil {
		// This is a local platform failure, so its error event is allowed to use
		// the platform's actual error time.  Crucially, we do not repair the
		// invalid start time or continue the run with a guessed value.
		completion = chat.RunCompletion{
			ChatID:          params.Session.ChatID,
			RunID:           params.Session.RunID,
			FinishReason:    "error",
			StartedAtMillis: params.StartedAtMillis,
			UpdatedAtMillis: time.Now().UnixMilli(),
		}
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		publishLocalTimeContractRunError(params, err)
		return
	}
	// Bind the assembler to the single timestamp captured by run registration.
	// Bootstrap must never call its own clock for run.start: Desktop compares
	// that event with activeRun.startedAt and the run.started notification.
	if params.Assembler != nil {
		params.Assembler.SetRunStartedAtMillis(params.StartedAtMillis)
	}

	var (
		assistantText strings.Builder
		runUsage      chat.UsageData
		chatUsage     chat.UsageData
	)
	if params.Summary.Usage != nil {
		chatUsage = *params.Summary.Usage
	}
	processor := runexec.NewProcessor(runexec.ProcessorOptions{
		AssistantText: &assistantText, StepWriter: params.StepWriter,
		Billing: params.Billing, Models: params.Models, ChatUsage: chatUsage, RunUsage: &runUsage,
		AggregateUsageByTask: params.Session.TeamRuntime != nil, RunControl: params.RunControl,
		RunID: params.Session.RunID, ChatID: params.Session.ChatID,
		AgentKey:       contracts.ResolveRunOwner(params.Session.RunOwner).AgentKey,
		OnCompactEvent: func(data stream.EventData) { completeCompactControl(params.RunControl, data) },
	})

	runCtx := params.RunCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancelExecution := context.WithCancel(runCtx)
	defer cancelExecution()
	if params.StepWriter != nil {
		runCtx = chat.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	var processingErr error
	publishEmissions := func(emissions []stream.EventEmission) error {
		if processingErr != nil {
			return processingErr
		}
		if len(emissions) == 0 {
			return nil
		}
		for _, emission := range emissions {
			event := emission.Event
			// Storage records the public coverage boundary. Hidden events reuse
			// the latest cursor but never reserve or publish that sequence.
			event.Seq = emission.Cursor
			data, _, err := processor.Consume(event)
			if err != nil {
				processingErr = err
				handleCompactCheckpointPersistenceFailure(params, processor, data)
				// Stop the producer as soon as the bad event is observed. The
				// subsequent local run.error is platform-owned and is published
				// below instead of repairing this event.
				cancelExecution()
				return err
			}
			handleAwaitingLifecycle(params, data, tracker)
			if emission.Visible && params.EventBus != nil {
				params.EventBus.Publish(clientVisibleEventData(data))
			}
		}
		return nil
	}
	failProcessing := func(err error) {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		publishLocalRunProcessingError(params, err)
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
	}

	if err := publishEmissions(params.Assembler.BootstrapEmissions()); err != nil {
		failProcessing(err)
		return
	}

	agentStream, err := params.Agent.Stream(runCtx, params.Request, params.Session)
	if err != nil {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishEmissions(params.Assembler.FailEmissions(err)); publishErr != nil {
			failProcessing(publishErr)
			return
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		if processingErr != nil {
			return
		}
		// The TEAM coordinator is a hidden runtime actor. Its reasoning is part of
		// the routing implementation, not user-visible conversation content.
		// Child-agent reasoning is routed through emitInputs below and remains
		// task-scoped, so this only suppresses the coordinator's own reasoning.
		if params.Session.TeamRuntime != nil {
			if _, ok := delta.(contracts.DeltaReasoning); ok {
				return
			}
		}
		if value, ok := delta.(contracts.DeltaRunContinuation); ok {
			cloned := value
			cloned.Answer = contracts.CloneMap(value.Answer)
			continuation = &cloned
			return
		}
		inputs := params.Mapper.Map(delta)
		for _, input := range inputs {
			if processingErr != nil {
				return
			}
			if content, ok := input.(stream.ContentDelta); ok && params.Session.TeamRuntime != nil {
				content.ActorType = "team"
				content.TeamID = strings.TrimSpace(params.Session.TeamID)
				content.AgentKey = ""
				content.Presentation = "reply"
				input = content
			}
			processor.ApplyModelTurnControl(input)
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishEmissions(params.Assembler.ConsumeEmissions(input)); err != nil {
				return
			}
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			if processingErr != nil {
				return
			}
			processor.ApplyModelTurnControl(input)
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishEmissions(params.Assembler.ConsumeEmissions(input)); err != nil {
				return
			}
		}
	}

	orchestrator := &frameOrchestrator{
		runCtx:            runCtx,
		request:           params.Request,
		session:           params.Session,
		summary:           params.Summary,
		agent:             params.Agent,
		registry:          params.Registry,
		teamSnapshot:      params.TeamSnapshot,
		buildQuerySession: params.BuildQuerySession,
		chats:             params.Chats,
		resourceBaseURL:   params.ResourceBaseURL,
		resourceTickets:   params.ResourceTickets,
		prepareSystemInit: params.PrepareSystemInit,
		mapper:            params.Mapper,
		emitDelta:         emitDelta,
		emitInputs:        emitInputs,
		currentLiveSeq:    params.Assembler.CurrentSeq,
	}

	streamFailed, streamInterrupted, orchestrateErr := orchestrator.Run(agentStream)
	if processingErr != nil {
		failProcessing(processingErr)
		return
	}
	if orchestrateErr != nil {
		streamFailed = true
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishEmissions(params.Assembler.FailEmissions(orchestrateErr)); publishErr != nil {
			failProcessing(publishErr)
			return
		}
	}

	terminalFinishReason := processor.TerminalFinishReason()
	if terminalFinishReason == "error" {
		streamFailed = true
		streamInterrupted = false
	} else if terminalFinishReason == "cancel" {
		streamInterrupted = true
		streamFailed = false
	}
	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, finishReason, false)
		return
	}

	if err := publishEmissions(params.Assembler.CompleteEmissions()); err != nil {
		failProcessing(err)
		return
	}
	persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "complete", true)
}

func publishLocalTimeContractRunError(params RunExecutorParams, err error) {
	if params.EventBus == nil {
		return
	}
	params.EventBus.Publish(localTimeContractRunErrorEvent(
		params.EventBus.LatestSeq()+1,
		params.Session.RunID,
		params.Session.ChatID,
		err,
	))
}

func publishLocalRunProcessingError(params RunExecutorParams, err error) {
	if isTimeContractViolation(err) {
		publishLocalTimeContractRunError(params, err)
		return
	}
	if params.EventBus == nil {
		return
	}
	payload := apperrors.Payload(
		apperrors.CodeStorageFailed,
		"run event persistence failed",
		apperrors.WithScope(apperrors.ScopeRun),
		apperrors.WithRetryable(true),
	)
	params.EventBus.Publish(stream.EventData{
		Seq:       params.EventBus.LatestSeq() + 1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"runId":   params.Session.RunID,
			"chatId":  params.Session.ChatID,
			"message": "run event persistence failed",
			"error":   payload,
		},
	})
}

func compactCheckpointPersistenceFailedEvent(data stream.EventData) stream.EventData {
	payload := map[string]any{
		"requestId": data.String("requestId"),
		"compactId": data.String("compactId"),
		"chatId":    data.String("chatId"),
		"runId":     data.String("runId"),
		"trigger":   data.String("trigger"),
		"level":     data.String("level"),
		"scope":     data.String("scope"),
		"detail":    "compact_persist_failed",
		"retryable": true,
	}
	return stream.EventData{
		Seq:       data.Seq,
		Type:      "context.compact.failed",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func handleCompactCheckpointPersistenceFailure(params RunExecutorParams, processor any, data stream.EventData) {
	if data.Type != "context.compact.complete" {
		return
	}
	failed := compactCheckpointPersistenceFailedEvent(data)
	runControl := params.RunControl
	if runControl == nil {
		if provider, ok := processor.(interface{ RunControl() *contracts.RunControl }); ok {
			runControl = provider.RunControl()
		}
	}
	completeCompactControl(runControl, failed)
	if params.EventBus != nil {
		params.EventBus.Publish(clientVisibleEventData(failed))
	}
}

func shouldStartRunContinuation(persisted bool, completion chat.RunCompletion, continuation *contracts.DeltaRunContinuation) bool {
	return persisted &&
		continuation != nil &&
		strings.TrimSpace(continuation.RunID) != "" &&
		strings.EqualFold(strings.TrimSpace(completion.FinishReason), "complete")
}

func handleAwaitingLifecycle(params RunExecutorParams, data stream.EventData, tracker *awaitingTracker) {
	switch data.Type {
	case "awaiting.ask":
		awaitingID := strings.TrimSpace(data.String("awaitingId"))
		if awaitingID == "" {
			return
		}
		runID := strings.TrimSpace(data.String("runId"))
		if runID == "" {
			runID = params.Session.RunID
		}
		mode := strings.TrimSpace(data.String("mode"))
		pending := chat.PendingAwaiting{
			AwaitingID: awaitingID,
			RunID:      runID,
			Mode:       mode,
			CreatedAt:  data.Timestamp,
		}
		if params.Chats != nil {
			_ = params.Chats.SetPendingAwaiting(params.Session.ChatID, pending)
		}
		if params.RunControl != nil {
			taskID := strings.TrimSpace(data.String("taskId"))
			internalAwaitingID := awaitingID
			publicAwaitingID := ""
			if rawAwaitingID := rawAwaitingIDForTask(taskID, awaitingID); rawAwaitingID != "" && rawAwaitingID != awaitingID {
				internalAwaitingID = rawAwaitingID
				publicAwaitingID = awaitingID
			}
			params.RunControl.ExpectSubmit(contracts.AwaitingSubmitContext{
				AwaitingID:       internalAwaitingID,
				PublicAwaitingID: publicAwaitingID,
				TaskID:           taskID,
				Mode:             mode,
				ItemCount:        awaitingEventItemCount(data),
				Questions:        awaitingEventQuestions(data),
				NoTimeout:        strings.EqualFold(mode, "planning"),
				Timeout:          int64(contracts.AnyIntNode(data.Value("timeout"))),
			})
		}
		tracker.pendingAwaitingID = awaitingID
		tracker.pendingMode = mode
		if params.Notifications != nil {
			payload := map[string]any{
				"chatId":     params.Session.ChatID,
				"runId":      runID,
				"awaitingId": awaitingID,
				"mode":       mode,
				"createdAt":  data.Timestamp,
			}
			if timeout, exists := data.Payload["timeout"]; exists {
				payload["timeout"] = contracts.AnyIntNode(timeout)
			}
			decorateNotificationRunOwner(payload, params.Session)
			if viewportType := strings.TrimSpace(data.String("viewportType")); viewportType != "" {
				payload["viewportType"] = viewportType
			}
			if viewportKey := strings.TrimSpace(data.String("viewportKey")); viewportKey != "" {
				payload["viewportKey"] = viewportKey
			}
			params.Notifications.Broadcast("awaiting.asking", payload)
		}
	case "awaiting.answer":
		awaitingID := strings.TrimSpace(data.String("awaitingId"))
		if awaitingID == "" {
			return
		}
		if params.Chats != nil {
			_ = params.Chats.ClearPendingAwaiting(params.Session.ChatID, awaitingID)
		}
		if tracker.pendingAwaitingID == awaitingID {
			tracker.pendingAwaitingID = ""
			tracker.pendingMode = ""
		}
		runID := strings.TrimSpace(data.String("runId"))
		if runID == "" {
			runID = params.Session.RunID
		}
		payload := map[string]any{
			"chatId":     params.Session.ChatID,
			"runId":      runID,
			"awaitingId": awaitingID,
			"mode":       strings.TrimSpace(data.String("mode")),
			"status":     strings.TrimSpace(data.String("status")),
			"answeredAt": data.Timestamp,
		}
		decorateNotificationRunOwner(payload, params.Session)
		if submitID := strings.TrimSpace(data.String("submitId")); submitID != "" {
			payload["submitId"] = submitID
		}
		if _, ok := data.Payload["durationMs"]; ok {
			payload["durationMs"] = contracts.AnyIntNode(data.Value("durationMs"))
		}
		if errorCode := awaitingAnswerErrorCode(data); errorCode != "" {
			payload["errorCode"] = errorCode
		}
		if params.Notifications != nil {
			params.Notifications.Broadcast("awaiting.answered", payload)
		}
	}
}

func decorateNotificationRunOwner(payload map[string]any, session contracts.QuerySession) {
	if payload == nil {
		return
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	if owner.IsTeam() {
		payload["teamId"] = owner.TeamID
		return
	}
	payload["agentKey"] = owner.AgentKey
	if owner.TeamID != "" {
		payload["teamId"] = owner.TeamID
	}
}

func awaitingEventItemCount(data stream.EventData) int {
	switch strings.ToLower(strings.TrimSpace(data.String("mode"))) {
	case "question":
		return awaitingPayloadItemCount(data.Value("questions"))
	case "approval":
		return awaitingPayloadItemCount(data.Value("approvals"))
	case "form":
		return awaitingPayloadItemCount(data.Value("forms"))
	case "planning":
		if lenAnyMap(data.Value("planning")) > 0 {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func awaitingEventQuestions(data stream.EventData) []any {
	if !strings.EqualFold(strings.TrimSpace(data.String("mode")), "question") {
		return nil
	}
	switch questions := data.Value("questions").(type) {
	case []any:
		return append([]any(nil), questions...)
	case []map[string]any:
		result := make([]any, 0, len(questions))
		for _, question := range questions {
			result = append(result, question)
		}
		return result
	default:
		return nil
	}
}

func awaitingPayloadItemCount(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func rawAwaitingIDForTask(taskID string, awaitingID string) string {
	taskID = strings.TrimSpace(taskID)
	awaitingID = strings.TrimSpace(awaitingID)
	if taskID == "" || awaitingID == "" {
		return awaitingID
	}
	prefix := taskID + ":"
	if !strings.HasPrefix(awaitingID, prefix) {
		return awaitingID
	}
	return strings.TrimSpace(strings.TrimPrefix(awaitingID, prefix))
}

func awaitingAnswerErrorCode(data stream.EventData) string {
	errPayload := contracts.AnyMapNode(data.Value("error"))
	if len(errPayload) == 0 {
		return ""
	}
	return strings.TrimSpace(contracts.AnyStringNode(errPayload["code"]))
}

func maybeBroadcastInterruptedAwaiting(params RunExecutorParams, tracker *awaitingTracker) {
	if tracker == nil || strings.TrimSpace(tracker.pendingAwaitingID) == "" {
		return
	}
	if params.Chats != nil {
		_ = params.Chats.ClearPendingAwaiting(params.Session.ChatID, tracker.pendingAwaitingID)
	}
	if params.Notifications != nil {
		payload := map[string]any{
			"chatId":     params.Session.ChatID,
			"runId":      params.Session.RunID,
			"awaitingId": tracker.pendingAwaitingID,
			"mode":       tracker.pendingMode,
			"status":     "error",
			"errorCode":  "run_interrupted",
			"answeredAt": time.Now().UnixMilli(),
		}
		decorateNotificationRunOwner(payload, params.Session)
		params.Notifications.Broadcast("awaiting.answered", payload)
	}
	tracker.pendingAwaitingID = ""
	tracker.pendingMode = ""
}

func persistRunCompletionWithReason(params RunExecutorParams, assistantText string, runUsage chat.UsageData, finishReason string, notifyPersisted bool) (bool, chat.RunCompletion) {
	completedAtMillis := time.Now().UnixMilli()
	owner := contracts.ResolveRunOwner(params.Session.RunOwner)
	completion := chat.RunCompletion{
		ChatID:          params.Session.ChatID,
		RunID:           params.Session.RunID,
		AgentKey:        owner.AgentKey,
		AgentMode:       persistedRunMode(params.Session.Mode),
		TeamID:          owner.TeamID,
		AssistantText:   assistantText,
		InitialMessage:  params.Request.Message,
		FinishReason:    finishReason,
		StartedAtMillis: params.StartedAtMillis,
		UpdatedAtMillis: completedAtMillis,
		Usage:           runUsage,
	}
	if err := timecontract.ValidateEpochMillis(completion.StartedAtMillis, "startedAt", "run.completion"); err != nil {
		return false, completion
	}
	if params.Chats == nil {
		return false, completion
	}
	if err := params.Chats.OnRunCompleted(completion); err != nil {
		return false, completion
	}
	if notifyPersisted && finishReason == "complete" && params.OnPersisted != nil {
		params.OnPersisted(completion)
	}
	return true, completion
}

// persistedRunMode exposes only stable public spellings for modes created by
// the current runtime. It intentionally does not reinterpret historical rows.
func persistedRunMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "PLAN_EXECUTE":
		return "PLAN-EXECUTE"
	case "ONESHOT":
		return "REACT"
	default:
		return strings.TrimSpace(mode)
	}
}

func broadcastRunCompletion(params RunExecutorParams, completion chat.RunCompletion) {
	if params.Chats == nil {
		return
	}
	if params.OnUnreadChanged != nil {
		if sum, err := params.Chats.Summary(completion.ChatID); err == nil && sum != nil {
			params.OnUnreadChanged(*sum)
		}
	}
	if params.Notifications != nil {
		params.Notifications.Broadcast("chat.updated", map[string]any{
			"chatId":         completion.ChatID,
			"lastRunId":      completion.RunID,
			"lastRunContent": completion.AssistantText,
			"updatedAt":      completion.UpdatedAtMillis,
		})
	}
}

func (s *Server) broadcastRunCompletionNotifications(completion chat.RunCompletion) {
	if s == nil {
		return
	}
	broadcastRunCompletion(RunExecutorParams{
		Chats:         s.deps.Chats,
		Notifications: s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
	}, completion)
}
