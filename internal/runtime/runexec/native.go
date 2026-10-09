package runexec

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/orchestration"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

type NativeOptions struct {
	ObserveEvent      func(stream.EventData)
	EmitVisible       func(stream.EventData) error
	RunCtx            context.Context
	Request           runtimetypes.QueryCommand
	Session           contracts.QuerySession
	StartedAtMillis   int64
	Summary           chat.Summary
	Agent             runtimetypes.Engine
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
	ResourceTickets   proxy.TicketIssuer
	BuildQuerySession func(context.Context, runtimetypes.QueryCommand, chat.Summary, catalog.AgentDefinition, session.Options) (contracts.QuerySession, error)
	PrepareSystemInit func(runtimetypes.QueryCommand, *contracts.QuerySession, bool) (*chat.QueryLineSystem, error)
	Notifications     contracts.NotificationSink
	OnUnreadChanged   func(chat.Summary)
	OnPersisted       func(chat.RunCompletion)
	OnContinuation    func(contracts.DeltaRunContinuation) (string, error)
	// OnComplete receives the same terminal record used for persistence. Callers
	// use it for run.finished so its time and status cannot drift from the run.
	OnComplete func(chat.RunCompletion)
}

type AwaitingTracker struct {
	PendingAwaitingID string
	PendingMode       string
}

func CompleteCompactControl(runControl *contracts.RunControl, data stream.EventData) {
	if runControl == nil || (data.Type != "context.compact.complete" && data.Type != "context.compact.failed") {
		return
	}
	status := "failed"
	if data.Type == "context.compact.complete" {
		status = "completed"
	}
	compactionUsage, _ := data.Value("compactionUsage").(map[string]any)
	response := queryinput.CompactResponse{
		CycleID:                    data.String("cycleId"),
		CycleComplete:              CompactCycleFlag(data.Value("cycleComplete")),
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
		CompressionRatio:           CompactFloat64(data.Value("compressionRatio")),
		RemainingRatio:             CompactFloat64(data.Value("remainingRatio")),
		ReleasedRatio:              CompactFloat64(data.Value("releasedRatio")),
		TokensFreed:                contracts.AnyIntNode(data.Value("tokensFreed")),
		ToolsCleared:               contracts.AnyIntNode(data.Value("toolsCleared")),
		ReasoningCleared:           contracts.AnyIntNode(data.Value("reasoningCleared")),
		ToolsKept:                  contracts.AnyIntNode(data.Value("toolsKept")),
		CompactionUsage:            contracts.CloneMap(compactionUsage),
		Detail:                     data.String("detail"),
		Retryable:                  data.Value("retryable") == true,
	}
	runControl.CompleteCompact(response.RequestID, response)
}

func CompactFloat64(value any) float64 {
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

func ClientVisibleEventData(data stream.EventData) stream.EventData {
	if data.Type == "tool.wait" || data.Type == "tool.wait.update" {
		payload := contracts.CloneMap(data.Payload)
		delete(payload, "waitCheckpoint")
		data.Payload = payload
		return data
	}
	if len(data.Payload) == 0 {
		return data
	}
	if data.Type != "request.query" && !strings.HasPrefix(data.Type, "context.compact.") {
		return data
	}
	payload := make(map[string]any, len(data.Payload))
	for key, value := range data.Payload {
		if key == "messages" || key == "system" || key == "checkpointMessages" || key == "compactCoveredMessages" || key == "l1KeepRecent" || key == "l1PreserveReasoning" || key == "previousRunState" || key == "awaitingId" {
			continue
		}
		payload[key] = value
	}
	data.Payload = payload
	return data
}

func StartNative(params NativeOptions) {
	go ExecuteNative(params)
}

func ExecuteNative(params NativeOptions) Result {
	tracker := &AwaitingTracker{}
	result := Execute(ExecuteOptions{
		RunCtx: params.RunCtx, Session: params.Session,
		StartedAtMillis: params.StartedAtMillis, Summary: params.Summary,
		StartStream: func(ctx context.Context) (contracts.AgentStream, error) {
			return params.Agent.Stream(ctx, params.Request, params.Session)
		},
		Assembler: params.Assembler, Mapper: params.Mapper, Billing: params.Billing,
		Models: params.Models, StepWriter: params.StepWriter, RunControl: params.RunControl,
		Notifications:        params.Notifications,
		ObserveEvent:         params.ObserveEvent,
		OnCompactEvent:       func(data stream.EventData) { CompleteCompactControl(params.RunControl, data) },
		OnPersistenceFailure: func(data stream.EventData) { HandleCompactCheckpointPersistenceFailure(params, data) },
		OnProcessingError:    func(err error) { PublishLocalRunProcessingError(params, err) },
		OnEvent:              func(data stream.EventData) { HandleAwaitingLifecycle(params, data, tracker) },
		Publish: func(data stream.EventData) error {
			data = ClientVisibleEventData(data)
			if params.EventBus != nil {
				params.EventBus.Publish(data)
			}
			if params.EmitVisible != nil {
				return params.EmitVisible(data)
			}
			return nil
		},
		PersistCompletion: func(text string, usage chat.UsageData, reason string, notify bool) (bool, chat.RunCompletion) {
			return PersistRunCompletionWithReason(params, text, usage, reason, notify)
		},
		NewOrchestrator: func(ctx context.Context, emitDelta func(contracts.AgentDelta), emitInputs func(...stream.StreamInput)) orchestration.DeltaHandler {
			return &orchestration.Coordinator{
				RunCtx: ctx, Request: params.Request, Session: params.Session, Summary: params.Summary,
				Agent: params.Agent, Registry: params.Registry, TeamSnapshot: params.TeamSnapshot,
				BuildQuerySession: params.BuildQuerySession, Chats: params.Chats,
				ResourceBaseURL: params.ResourceBaseURL, ResourceTickets: params.ResourceTickets,
				PrepareSystemInit: params.PrepareSystemInit, Mapper: params.Mapper,
				EmitDelta: emitDelta, EmitInputs: emitInputs, CurrentLiveSeq: params.Assembler.CurrentSeq,
			}
		},
	})
	MaybeBroadcastInterruptedAwaiting(params, tracker)
	if params.StepWriter != nil {
		params.StepWriter.Flush()
	}
	if params.EventBus != nil {
		params.EventBus.FreezeAndWait()
	}
	if params.OnComplete != nil {
		params.OnComplete(result.Completion)
	}
	if ShouldStartRunContinuation(result.Persisted, result.Completion, result.Continuation) && params.OnContinuation != nil {
		if _, err := params.OnContinuation(*result.Continuation); err != nil {
			log.Printf("[server][run] start continuation failed sourceRunId=%s continuationRunId=%s err=%v", params.Session.RunID, result.Continuation.RunID, err)
		}
	}
	if result.Persisted {
		BroadcastCompletion(params, result.Completion)
	}
	return result
}

func PublishLocalTimeContractRunError(params NativeOptions, err error) {
	if params.EventBus == nil {
		return
	}
	params.EventBus.Publish(LocalTimeContractRunErrorEvent(
		params.EventBus.LatestSeq()+1,
		params.Session.RunID,
		params.Session.ChatID,
		err,
	))
}

func PublishLocalRunProcessingError(params NativeOptions, err error) {
	if IsTimeContractViolation(err) {
		PublishLocalTimeContractRunError(params, err)
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

func CompactCheckpointPersistenceFailedEvent(data stream.EventData) stream.EventData {
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

func CompactCycleFlag(value any) *bool {
	if flag, ok := value.(bool); ok {
		return &flag
	}
	return nil
}

func HandleCompactCheckpointPersistenceFailure(params NativeOptions, data stream.EventData) {
	if data.Type != "context.compact.complete" {
		return
	}
	failed := CompactCheckpointPersistenceFailedEvent(data)
	CompleteCompactControl(params.RunControl, failed)
	if params.EventBus != nil {
		params.EventBus.Publish(ClientVisibleEventData(failed))
	}
}

func ShouldStartRunContinuation(persisted bool, completion chat.RunCompletion, continuation *contracts.DeltaRunContinuation) bool {
	return persisted &&
		continuation != nil &&
		strings.TrimSpace(continuation.RunID) != "" &&
		strings.EqualFold(strings.TrimSpace(completion.FinishReason), "complete")
}

func HandleAwaitingLifecycle(params NativeOptions, data stream.EventData, tracker *AwaitingTracker) {
	if data.Type == "tool.wait" {
		id := data.String("toolId")
		if params.Chats != nil {
			_ = params.Chats.SetPendingAwaiting(params.Session.ChatID, chat.PendingAwaiting{AwaitingID: id, RunID: params.Session.RunID, Mode: "wait", CreatedAt: data.Timestamp})
		}
		tracker.PendingAwaitingID = id
		tracker.PendingMode = "wait"
		return
	}
	if data.Type == "tool.result" && data.String("toolName") == "wait" {
		if params.Chats != nil {
			_ = params.Chats.ClearPendingAwaiting(params.Session.ChatID, data.String("toolId"))
		}
		if tracker.PendingAwaitingID == data.String("toolId") {
			tracker.PendingAwaitingID = ""
			tracker.PendingMode = ""
		}
		return
	}
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
			// A Team already tracks its member awaitings by public ID because
			// raw IDs can repeat across members; only shared-control children
			// are aliased to their raw waiter.
			_, teamOwned := params.RunControl.LookupAwaiting(awaitingID)
			if rawAwaitingID := RawAwaitingIDForTask(taskID, awaitingID); !teamOwned && rawAwaitingID != "" && rawAwaitingID != awaitingID {
				internalAwaitingID = rawAwaitingID
				publicAwaitingID = awaitingID
			}
			summaries, truncated := contracts.SummarizeApprovals(data.Value("approvals"))
			params.RunControl.ExpectSubmit(contracts.AwaitingSubmitContext{
				Summaries:          summaries,
				SummariesTruncated: truncated,
				AwaitingID:         internalAwaitingID,
				PublicAwaitingID:   publicAwaitingID,
				TaskID:             taskID,
				Mode:               mode,
				ItemCount:          AwaitingEventItemCount(data),
				Questions:          AwaitingEventQuestions(data),
				NoTimeout:          strings.EqualFold(mode, "planning"),
				Timeout:            int64(contracts.AnyIntNode(data.Value("timeout"))),
			})
		}
		tracker.PendingAwaitingID = awaitingID
		tracker.PendingMode = mode
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
			DecorateNotificationRunOwner(payload, params.Session)
			if ref := data.Value("view"); ref != nil {
				payload["view"] = ref
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
		if tracker.PendingAwaitingID == awaitingID {
			tracker.PendingAwaitingID = ""
			tracker.PendingMode = ""
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
		DecorateNotificationRunOwner(payload, params.Session)
		if submitID := strings.TrimSpace(data.String("submitId")); submitID != "" {
			payload["submitId"] = submitID
		}
		if _, ok := data.Payload["durationMs"]; ok {
			payload["durationMs"] = contracts.AnyIntNode(data.Value("durationMs"))
		}
		if errorCode := AwaitingAnswerErrorCode(data); errorCode != "" {
			payload["errorCode"] = errorCode
		}
		if params.Notifications != nil {
			params.Notifications.Broadcast("awaiting.answered", payload)
		}
	}
}

func DecorateNotificationRunOwner(payload map[string]any, session contracts.QuerySession) {
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

func AwaitingEventItemCount(data stream.EventData) int {
	switch strings.ToLower(strings.TrimSpace(data.String("mode"))) {
	case "question":
		return AwaitingPayloadItemCount(data.Value("questions"))
	case "approval":
		return AwaitingPayloadItemCount(data.Value("approvals"))
	case "form", "planning":
		return 1
	default:
		return 0
	}
}

func AwaitingEventQuestions(data stream.EventData) []any {
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

func AwaitingPayloadItemCount(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func RawAwaitingIDForTask(taskID string, awaitingID string) string {
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

func AwaitingAnswerErrorCode(data stream.EventData) string {
	errPayload := contracts.AnyMapNode(data.Value("error"))
	if len(errPayload) == 0 {
		return ""
	}
	return strings.TrimSpace(contracts.AnyStringNode(errPayload["code"]))
}

func MaybeBroadcastInterruptedAwaiting(params NativeOptions, tracker *AwaitingTracker) {
	if tracker == nil || strings.TrimSpace(tracker.PendingAwaitingID) == "" {
		return
	}
	if params.Chats != nil {
		_ = params.Chats.ClearPendingAwaiting(params.Session.ChatID, tracker.PendingAwaitingID)
	}
	if params.Notifications != nil {
		payload := map[string]any{
			"chatId":     params.Session.ChatID,
			"runId":      params.Session.RunID,
			"awaitingId": tracker.PendingAwaitingID,
			"mode":       tracker.PendingMode,
			"status":     "error",
			"errorCode":  "run_interrupted",
			"answeredAt": time.Now().UnixMilli(),
		}
		DecorateNotificationRunOwner(payload, params.Session)
		params.Notifications.Broadcast("awaiting.answered", payload)
	}
	tracker.PendingAwaitingID = ""
	tracker.PendingMode = ""
}

func PersistRunCompletionWithReason(params NativeOptions, assistantText string, runUsage chat.UsageData, finishReason string, notifyPersisted bool) (bool, chat.RunCompletion) {
	completedAtMillis := time.Now().UnixMilli()
	owner := contracts.ResolveRunOwner(params.Session.RunOwner)
	completion := chat.RunCompletion{
		ChatID:          params.Session.ChatID,
		RunID:           params.Session.RunID,
		AgentKey:        owner.AgentKey,
		AgentMode:       PersistedRunMode(params.Session.Mode),
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

// PersistedRunMode exposes only stable public spellings for modes created by
// the current runtime. It intentionally does not reinterpret historical rows.
func PersistedRunMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "PLAN_EXECUTE":
		return "PLAN-EXECUTE"
	case "ONESHOT", "REACT":
		return "GENERAL"
	default:
		return strings.TrimSpace(mode)
	}
}

func BroadcastCompletion(params NativeOptions, completion chat.RunCompletion) {
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

func LocalTimeContractRunErrorEvent(seq int64, runID, chatID string, err error) stream.EventData {
	if seq <= 0 {
		seq = 1
	}
	contractData := TimeContractErrorData(err)
	contractData["status"] = 422
	message := ContractViolationMessage(err)
	contractData["message"] = message
	payload := map[string]any{
		"runId":   runID,
		"chatId":  chatID,
		"message": message,
		"error":   contractData,
	}
	for _, key := range []string{"code", "field", "location", "expected"} {
		payload[key] = contractData[key]
	}
	return stream.EventData{
		Seq:       seq,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func NextLocalTimeContractErrorSeq(lastSeq int64, rejected stream.EventData) int64 {
	seq := rejected.Seq
	if seq <= lastSeq {
		seq = lastSeq + 1
	}
	if seq <= 0 {
		seq = 1
	}
	return seq
}

func IsTimeContractViolation(err error) bool {
	return errors.Is(err, ErrTimeContractViolation) || timecontract.IsViolation(err) || chat.IsJSONLSchemaViolation(err)
}

func TimeContractErrorData(err error) map[string]any {
	if chat.IsJSONLSchemaViolation(err) {
		return ChatStorageSchemaErrorData(err)
	}
	data := timecontract.ErrorData(err)
	data["category"] = string(apperrors.CategoryRequest)
	data["scope"] = string(apperrors.ScopeRequest)
	data["status"] = 422
	data["retryable"] = false
	data["userSafeMessageKey"] = string(apperrors.CodeTimeContractViolation)
	data["message"] = timeContractViolationMessage
	return data
}

func ContractViolationMessage(err error) string {
	if chat.IsJSONLSchemaViolation(err) {
		return chatStorageSchemaViolationMessage
	}
	return timeContractViolationMessage
}

const timeContractViolationMessage = "time contract violation"

var ErrTimeContractViolation = errors.New("time contract violation")

func LenAnyMap(value any) int {
	if item, ok := value.(map[string]any); ok {
		return len(item)
	}
	return 0
}

func ChatStorageSchemaErrorData(err error) map[string]any {
	data := chat.JSONLSchemaErrorData(err)
	data["category"] = string(apperrors.CategoryChatRun)
	data["scope"] = string(apperrors.ScopeChat)
	data["status"] = 422
	data["retryable"] = false
	data["userSafeMessageKey"] = string(apperrors.CodeChatStorageSchemaViolation)
	data["message"] = chatStorageSchemaViolationMessage
	return data
}

const chatStorageSchemaViolationMessage = "chat storage schema violation"
