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
	"agent-platform/internal/runtime/orchestration"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

type RunExecutorParams struct {
	ObserveEvent      func(stream.EventData)
	EmitVisible       func(stream.EventData) error
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
		CycleID:                    data.String("cycleId"),
		CycleComplete:              compactCycleFlag(data.Value("cycleComplete")),
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

func runExecutor(params RunExecutorParams) runexec.Result {
	tracker := &awaitingTracker{}
	result := runexec.Execute(runexec.ExecuteOptions{
		RunCtx: params.RunCtx, Session: params.Session,
		StartedAtMillis: params.StartedAtMillis, Summary: params.Summary,
		StartStream: func(ctx context.Context) (contracts.AgentStream, error) {
			return params.Agent.Stream(ctx, params.Request, params.Session)
		},
		Assembler: params.Assembler, Mapper: params.Mapper, Billing: params.Billing,
		Models: params.Models, StepWriter: params.StepWriter, RunControl: params.RunControl,
		ObserveEvent:         params.ObserveEvent,
		OnCompactEvent:       func(data stream.EventData) { completeCompactControl(params.RunControl, data) },
		OnPersistenceFailure: func(data stream.EventData) { handleCompactCheckpointPersistenceFailure(params, nil, data) },
		OnProcessingError:    func(err error) { publishLocalRunProcessingError(params, err) },
		OnEvent:              func(data stream.EventData) { handleAwaitingLifecycle(params, data, tracker) },
		Publish: func(data stream.EventData) error {
			data = clientVisibleEventData(data)
			if params.EventBus != nil {
				params.EventBus.Publish(data)
			}
			if params.EmitVisible != nil {
				return params.EmitVisible(data)
			}
			return nil
		},
		PersistCompletion: func(text string, usage chat.UsageData, reason string, notify bool) (bool, chat.RunCompletion) {
			return persistRunCompletionWithReason(params, text, usage, reason, notify)
		},
		NewOrchestrator: func(ctx context.Context, emitDelta func(contracts.AgentDelta), emitInputs func(...stream.StreamInput)) orchestration.DeltaHandler {
			return &frameOrchestrator{
				runCtx: ctx, request: params.Request, session: params.Session, summary: params.Summary,
				agent: params.Agent, registry: params.Registry, teamSnapshot: params.TeamSnapshot,
				buildQuerySession: params.BuildQuerySession, chats: params.Chats,
				resourceBaseURL: params.ResourceBaseURL, resourceTickets: params.ResourceTickets,
				prepareSystemInit: params.PrepareSystemInit, mapper: params.Mapper,
				emitDelta: emitDelta, emitInputs: emitInputs, currentLiveSeq: params.Assembler.CurrentSeq,
			}
		},
	})
	maybeBroadcastInterruptedAwaiting(params, tracker)
	if params.StepWriter != nil {
		params.StepWriter.Flush()
	}
	if params.EventBus != nil {
		params.EventBus.FreezeAndWait()
	}
	if params.OnComplete != nil {
		params.OnComplete(result.Completion)
	}
	if shouldStartRunContinuation(result.Persisted, result.Completion, result.Continuation) && params.OnContinuation != nil {
		if _, err := params.OnContinuation(*result.Continuation); err != nil {
			log.Printf("[server][run] start continuation failed sourceRunId=%s continuationRunId=%s err=%v", params.Session.RunID, result.Continuation.RunID, err)
		}
	}
	if result.Persisted {
		broadcastRunCompletion(params, result.Completion)
	}
	return result
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
	if id := data.String("cycleId"); id != "" {
		payload["cycleId"] = id
		payload["cycleComplete"] = true
	}
	return stream.EventData{
		Seq:       data.Seq,
		Type:      "context.compact.failed",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func compactCycleFlag(value any) *bool {
	if flag, ok := value.(bool); ok {
		return &flag
	}
	return nil
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
