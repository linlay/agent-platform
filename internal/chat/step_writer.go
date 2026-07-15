package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

// StepWriter accumulates SSE events and writes Java-compatible JSONL lines
// (_type: "query" / "react" / "react-tool" / "submit" / "steer" / "event")
// to the chat store.
//
// It mirrors the behaviour of Java's TurnTraceWriter:
//   - internal stage markers flush the current step and start a new one
//   - artifact publication audits are attached only to their matching tool step
//   - snapshot events (reasoning/content/tool/action) become StoredMessages
//   - request.submit + awaiting.answer are merged into SubmitLines
//   - request.steer becomes a typed EventLine so chat detail can replay it
type StepWriter struct {
	store  StepLineStore
	chatID string
	runID  string
	mode   string // "REACT" / "PLAN_EXECUTE" / "ONESHOT" / "CODER"

	queryWritten bool
	seqCounter   int

	currentStage string

	messages         []StoredMessage
	pendingArtifacts *ArtifactPublicationState
	pendingSources   *SourceState
	taskBuffers      map[string]*taskStepBuffer
	closedTaskIDs    map[string]bool
	stepLiveSeq      int64

	// tool/action name tracking (for tool.result → StoredMessage.Name)
	toolNames     map[string]string
	actionNames   map[string]string
	toolTaskIDs   map[string]string
	actionTaskIDs map[string]string

	// msgId generation
	currentMsgID  string
	needNewMsgID  bool
	lastToolOrder []string

	// pending step-level metadata captured during the current LLM turn
	pendingAwaiting         []map[string]any
	pendingQueryMessages    []map[string]any
	pendingApproval         *StepApproval
	pendingSubmit           map[string]any
	pendingSubmitLiveSeq    int64
	pendingSubmitTimestamp  int64
	pendingUsage            map[string]any
	pendingContextWindowMax int
	pendingContextCurrent   int
	pendingEstimated        int
	pendingModelKey         string
	pendingReasoningEffort  string
	pendingInputMessages    []map[string]any
	pendingSystemRef        map[string]any
	pendingSystemInit       *QueryLineSystem
	// lastTimestamp is carried from the most recent source event. It is used
	// only to finish an aggregation which already contains that event; it is
	// never replaced with the wall clock.
	lastTimestamp int64
	// persistenceErr is sticky. A JSONL write failure must stop the producer
	// path rather than silently dropping a request/step and leaving replay with
	// an apparently valid but incomplete history.
	persistenceErr error
}

type StepWriterOption func(*StepWriter)

// NewStepWriter creates a StepWriter for a single run.
func NewStepWriter(store StepLineStore, chatID, runID, mode string, opts ...StepWriterOption) *StepWriter {
	w := &StepWriter{
		store:         store,
		chatID:        chatID,
		runID:         runID,
		mode:          strings.ToUpper(strings.TrimSpace(mode)),
		taskBuffers:   map[string]*taskStepBuffer{},
		closedTaskIDs: map[string]bool{},
		toolNames:     map[string]string{},
		actionNames:   map[string]string{},
		toolTaskIDs:   map[string]string{},
		actionTaskIDs: map[string]string{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

func (w *StepWriter) SetPendingSystemInit(line *QueryLineSystem) {
	if w == nil || line == nil {
		return
	}
	cloned := cloneQueryLineSystem(*line)
	w.pendingSystemInit = &cloned
}

func (w *StepWriter) SetPendingQueryMessages(messages []map[string]any) {
	if w == nil || len(messages) == 0 {
		return
	}
	w.pendingQueryMessages = cloneMessageMaps(messages)
}

// Err returns the first JSONL persistence failure observed for this run.
// Callers which own the stream lifecycle must turn a time-contract error into
// the normal terminal run.error flow instead of continuing with partial
// history.
func (w *StepWriter) Err() error {
	if w == nil {
		return nil
	}
	return w.persistenceErr
}

func (w *StepWriter) recordPersistenceError(err error) {
	if w == nil || err == nil || w.persistenceErr != nil {
		return
	}
	w.persistenceErr = err
}

// OnEvent processes a single SSE event from the stream.
// It should be called for every event that goes through writeEvent in server.go.
func (w *StepWriter) OnEvent(event stream.EventData) {
	if w == nil || w.persistenceErr != nil {
		return
	}
	switch event.Type {
	case "request.query":
		w.handleRequestQuery(event)

	case "run.start":
		// default stage; will be overridden by the first internal stage marker
		if w.currentStage == "" {
			w.currentStage = "oneshot"
		}

	case "reasoning.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		ts := event.Timestamp
		w.appendStoredMessage(event, StoredMessage{
			Role:             "assistant",
			ReasoningContent: textContent(event.String("text")),
			ReasoningID:      event.String("reasoningId"),
			MsgID:            w.currentMsgID,
			Ts:               &ts,
		})

	case "content.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		ts := event.Timestamp
		actorType, teamID, agentKey := contentActorFromEvent(event)
		w.appendStoredMessage(event, StoredMessage{
			Role:         "assistant",
			Content:      textContent(event.String("text")),
			ContentID:    event.String("contentId"),
			MsgID:        w.currentMsgID,
			Ts:           &ts,
			ActorType:    actorType,
			TeamID:       teamID,
			AgentKey:     agentKey,
			Presentation: event.String("presentation"),
		})

	case "tool.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		toolID := event.String("toolId")
		toolName := event.String("toolName")
		taskID := event.String("taskId")
		ts := event.Timestamp
		w.toolNames[toolID] = toolName
		if strings.TrimSpace(taskID) != "" {
			w.toolTaskIDs[toolID] = taskID
		}
		w.appendStoredMessage(event, StoredMessage{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:   toolID,
				Type: "function",
				Function: StoredFunction{
					Name:      toolName,
					Arguments: event.String("arguments"),
				},
				ToolID: toolID,
			}},
			MsgID: w.currentMsgID,
			Ts:    &ts,
		})

	case "tool.result":
		w.flushAssistantStepBeforeToolResult(event)
		w.ensureStep()
		toolID := event.String("toolId")
		toolName := w.toolNames[toolID]
		if strings.TrimSpace(toolName) == "" {
			toolName = event.String("toolName")
			if strings.TrimSpace(toolName) != "" {
				w.toolNames[toolID] = toolName
			}
		}
		ts := event.Timestamp
		durationMs := durationMsPointer(event.Value("durationMs"), event.Payload)
		w.appendStoredMessage(stream.EventData{
			Seq:       event.Seq,
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"taskId": w.toolTaskIDs[toolID],
			},
		}, StoredMessage{
			Role:       "tool",
			Name:       toolName,
			ToolCallID: toolID,
			Content:    textContent(formatResult(event.Value("result"))),
			ToolID:     toolID,
			DurationMs: durationMs,
			Ts:         &ts,
		})
		w.needNewMsgID = true

	case "awaiting.ask":
		w.bufferAwaitingEvent(event)

	case "request.submit":
		w.flushCurrentStep()
		w.bufferSubmitEvent(event)

	case "awaiting.answer":
		w.writeSubmitLine(event)

	case "request.steer":
		w.flushCurrentStep()
		w.appendTypedEventLine(event, "steer")

	case "action.snapshot":
		w.ensureStep()
		w.ensureMsgID()
		actionID := event.String("actionId")
		actionName := event.String("actionName")
		taskID := event.String("taskId")
		ts := event.Timestamp
		w.actionNames[actionID] = actionName
		if strings.TrimSpace(taskID) != "" {
			w.actionTaskIDs[actionID] = taskID
		}
		w.appendStoredMessage(event, StoredMessage{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:   actionID,
				Type: "function",
				Function: StoredFunction{
					Name:      actionName,
					Arguments: event.String("arguments"),
				},
				ActionID: actionID,
			}},
			MsgID: w.currentMsgID,
			Ts:    &ts,
		})

	case "action.result":
		w.flushAssistantStepBeforeToolResult(event)
		w.ensureStep()
		actionID := event.String("actionId")
		ts := event.Timestamp
		w.appendStoredMessage(stream.EventData{
			Seq:       event.Seq,
			Type:      event.Type,
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"taskId": w.actionTaskIDs[actionID],
			},
		}, StoredMessage{
			Role:       "tool",
			Name:       w.actionNames[actionID],
			ToolCallID: actionID,
			Content:    textContent(formatResult(event.Value("result"))),
			ActionID:   actionID,
			Ts:         &ts,
		})
		w.needNewMsgID = true

	case "plan.create", "plan.update":
		// Live-only for new JSONL writes. Plan task state is persisted in .tools/plan-tasks.

	case "task.start":
		w.flushCurrentStep()
		taskID := event.String("taskId")
		if strings.TrimSpace(taskID) == "" {
			break
		}
		delete(w.closedTaskIDs, taskID)
		buffer := w.ensureTaskBuffer(taskID)
		if strings.TrimSpace(buffer.taskStage) == "" {
			buffer.taskStage = w.currentStage
		}
		buffer.taskStatus = ""
		buffer.taskSubAgentKey = event.String("subAgentKey")
		buffer.teamID = event.String("teamId")
		buffer.presentation = event.String("presentation")
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		buffer.lastTimestamp = event.Timestamp
	case "task.complete", "task.cancel", "task.error":
		taskID := event.String("taskId")
		if strings.TrimSpace(taskID) == "" {
			break
		}
		buffer := w.ensureTaskBuffer(taskID)
		switch event.Type {
		case "task.cancel":
			buffer.taskStatus = "cancelled"
		case "task.error":
			buffer.taskStatus = "failed"
		default:
			buffer.taskStatus = "completed"
		}
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		buffer.lastTimestamp = event.Timestamp
		w.flushTaskStep(taskID)
		delete(w.taskBuffers, taskID)
		w.closedTaskIDs[taskID] = true

	case "artifact.publish":
		if !w.appendArtifactEvent(event) {
			w.flushCurrentStep()
			w.appendTypedEventLine(event, "event")
		}

	case "source.publish":
		if !w.appendSourceEvent(event) {
			w.flushCurrentStep()
			w.appendTypedEventLine(event, "event")
		}

	case "debug.llmChat":
		if inner, ok := event.Value("data").(map[string]any); ok {
			if taskID := w.taskIDForEvent(event); taskID != "" {
				if w.closedTaskIDs[taskID] {
					break
				}
				buffer := w.ensureTaskBuffer(taskID)
				w.captureTaskDebugData(buffer, inner)
				buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
				buffer.lastTimestamp = event.Timestamp
			} else {
				w.captureRootDebugData(inner)
				w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
				w.lastTimestamp = event.Timestamp
			}
		}
	case "llm.request":
		if taskID := w.taskIDForEvent(event); taskID != "" {
			if w.closedTaskIDs[taskID] {
				break
			}
			w.flushTaskStep(taskID)
			buffer := w.ensureTaskBuffer(taskID)
			w.captureTaskLLMRequestData(buffer, event)
			buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
			buffer.lastTimestamp = event.Timestamp
		} else {
			w.flushCurrentStep()
			w.captureRootLLMRequestData(event)
			w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
			w.lastTimestamp = event.Timestamp
		}
	case "usage.snapshot":
		if taskID := w.taskIDForEvent(event); taskID != "" {
			if w.closedTaskIDs[taskID] {
				break
			}
			buffer := w.ensureTaskBuffer(taskID)
			w.captureTaskUsageSnapshot(buffer, event)
			buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
			buffer.lastTimestamp = event.Timestamp
		} else {
			w.captureRootUsageSnapshot(event)
			w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
			w.lastTimestamp = event.Timestamp
		}

	case "run.complete", "run.cancel", "run.error":
		w.flushCurrentStep()
		w.flushAllTaskSteps()
		w.flushPendingSubmit()
	}
}

// OnStageMarker processes an internal stage boundary without exposing it as a
// persisted or streamed event.
func (w *StepWriter) OnStageMarker(stage string) {
	w.flushCurrentStep()
	w.currentStage = parseStage(stage)
}

// Flush writes any remaining accumulated step. Call at end of stream.
func (w *StepWriter) Flush() {
	if w == nil || w.persistenceErr != nil {
		return
	}
	w.flushCurrentStep()
	w.flushAllTaskSteps()
	w.flushPendingSubmit()
}

func (w *StepWriter) RecordApproval(approval StepApproval) {
	if strings.TrimSpace(approval.Summary) == "" {
		w.pendingApproval = nil
		return
	}
	copyApproval := approval
	if len(approval.Decisions) > 0 {
		copyApproval.Decisions = append([]StepApprovalDecision(nil), approval.Decisions...)
	}
	w.pendingApproval = &copyApproval
}

// ---------------------------------------------------------------------------
// internal
// ---------------------------------------------------------------------------

func (w *StepWriter) handleRequestQuery(event stream.EventData) {
	if w == nil || w.store == nil || event.Timestamp <= 0 {
		return
	}
	_, hasMessages := event.Payload["messages"]
	_, hasSystem := event.Payload["system"]
	bootstrapQuery := hasMessages || hasSystem
	if w.queryWritten && !bootstrapQuery {
		return
	}
	if bootstrapQuery {
		w.flushCurrentStep()
	}

	query := map[string]any{}
	// Copy all payload fields into query, excluding seq/type/timestamp
	for key, val := range event.Payload {
		if key == "liveSeq" || key == "seq" || key == "messages" || key == "system" {
			continue
		}
		query[key] = val
	}
	messages := cloneMessageMaps(w.pendingQueryMessages)
	var system *QueryLineSystem
	if w.pendingSystemInit != nil {
		cloned := cloneQueryLineSystem(*w.pendingSystemInit)
		system = &cloned
	}
	if bootstrapQuery {
		messages = messagesFromEventValue(event.Value("messages"))
		if parsed, ok := systemFromEventValue(event.Value("system")); ok {
			system = &parsed
		} else {
			system = nil
		}
	}
	stampQueryMessages(messages, event.Timestamp)

	if err := w.store.AppendQueryLine(w.chatID, QueryLine{
		Type:      "query",
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: event.Timestamp,
		LiveSeq:   event.Seq,
		Query:     query,
		Messages:  messages,
		System:    system,
	}); err != nil {
		w.recordPersistenceError(err)
		return
	}
	w.queryWritten = true
	if !bootstrapQuery {
		w.pendingSystemInit = nil
		w.pendingQueryMessages = nil
	}
}

// stampQueryMessages assigns the request.query event's already-captured
// platform timestamp to newly produced model messages. This is not a
// historical fallback and never calls time.Now: an existing ts, including an
// invalid one, is preserved so the strict JSONL validator can reject it.
func stampQueryMessages(messages []map[string]any, timestamp int64) {
	for _, message := range messages {
		if message == nil {
			continue
		}
		if _, exists := message["ts"]; !exists {
			message["ts"] = timestamp
		}
	}
}

func (w *StepWriter) ensureStep() {
	if w.currentStage == "" {
		w.currentStage = "oneshot"
	}
}

func (w *StepWriter) appendStoredMessage(event stream.EventData, message StoredMessage) {
	if taskID := w.taskIDForEvent(event); taskID != "" {
		if w.closedTaskIDs[taskID] {
			return
		}
		buffer := w.ensureTaskBuffer(taskID)
		if agentKey := strings.TrimSpace(event.String("agentKey")); agentKey != "" {
			buffer.taskSubAgentKey = agentKey
		}
		if teamID := strings.TrimSpace(event.String("teamId")); teamID != "" {
			buffer.teamID = teamID
		}
		if presentation := strings.TrimSpace(event.String("presentation")); presentation != "" {
			buffer.presentation = presentation
		}
		if strings.TrimSpace(buffer.taskStage) == "" {
			buffer.taskStage = w.currentStage
		}
		buffer.messages = upsertStoredMessage(buffer.messages, message)
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		buffer.lastTimestamp = event.Timestamp
		return
	}
	w.messages = upsertStoredMessage(w.messages, message)
	w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
	w.lastTimestamp = event.Timestamp
}

func contentActorFromEvent(event stream.EventData) (string, string, string) {
	actorType := strings.TrimSpace(event.String("actorType"))
	teamID := strings.TrimSpace(event.String("teamId"))
	agentKey := strings.TrimSpace(event.String("agentKey"))
	actor, _ := event.Value("actor").(map[string]any)
	if actorType == "" {
		actorType = strings.TrimSpace(stringFromAny(actor["type"]))
	}
	if teamID == "" {
		teamID = strings.TrimSpace(stringFromAny(actor["teamId"]))
	}
	if agentKey == "" {
		agentKey = strings.TrimSpace(stringFromAny(actor["agentKey"]))
	}
	return actorType, teamID, agentKey
}

func (w *StepWriter) appendSourceEvent(event stream.EventData) bool {
	if w == nil {
		return false
	}
	item := sourceItemFromEvent(event)
	if taskID := w.taskIDForEvent(event); taskID != "" {
		if w.closedTaskIDs[taskID] {
			return false
		}
		buffer := w.taskBuffers[taskID]
		if buffer == nil || !storedMessagesContainTool(buffer.messages) {
			return false
		}
		buffer.sources = appendSourceStateItem(buffer.sources, item)
		buffer.liveSeq = maxLiveSeq(buffer.liveSeq, event.Seq)
		buffer.lastTimestamp = event.Timestamp
		return true
	}
	if !storedMessagesContainTool(w.messages) {
		return false
	}
	w.pendingSources = appendSourceStateItem(w.pendingSources, item)
	w.stepLiveSeq = maxLiveSeq(w.stepLiveSeq, event.Seq)
	w.lastTimestamp = event.Timestamp
	return true
}

func (w *StepWriter) flushAssistantStepBeforeToolResult(event stream.EventData) {
	if w == nil {
		return
	}
	if taskID := w.taskIDForEvent(event); taskID != "" {
		buffer := w.taskBuffers[taskID]
		if buffer != nil && storedMessagesContainAssistant(buffer.messages) {
			w.flushTaskStep(taskID)
		}
		return
	}
	if storedMessagesContainAssistant(w.messages) {
		w.flushCurrentStep()
	}
}

func (w *StepWriter) captureRootUsageSnapshot(event stream.EventData) {
	contextWindow, _ := event.Value("contextWindow").(map[string]any)
	if usage, ok := event.Value("usage").(map[string]any); ok {
		if current, ok := usage["current"].(map[string]any); ok {
			if !hasProviderUsagePayload(current) {
				return
			}
			w.pendingUsage = usagePayloadFromSnapshotEvent(event, current, true)
			w.capturePendingModelMetadata(w.pendingUsage)
		}
	}
	if cw := contextWindow; cw != nil {
		w.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		w.pendingContextCurrent = toIntFromKeys(cw, "currentSize")
		w.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		w.capturePendingModelMetadata(cw)
	}
}

func (w *StepWriter) captureTaskUsageSnapshot(buffer *taskStepBuffer, event stream.EventData) {
	if buffer == nil {
		return
	}
	contextWindow, _ := event.Value("contextWindow").(map[string]any)
	if usage, ok := event.Value("usage").(map[string]any); ok {
		if current, ok := usage["current"].(map[string]any); ok {
			if !hasProviderUsagePayload(current) {
				return
			}
			buffer.pendingUsage = usagePayloadFromSnapshotEvent(event, current, true)
			buffer.capturePendingModelMetadata(buffer.pendingUsage)
		}
	}
	if cw := contextWindow; cw != nil {
		buffer.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		buffer.pendingContextCurrent = toIntFromKeys(cw, "currentSize")
		buffer.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		buffer.capturePendingModelMetadata(cw)
	}
}

func (w *StepWriter) captureRootDebugData(inner map[string]any) {
	model, _ := inner["model"].(map[string]any)
	if systemRef := systemRefFromDebugData(inner); len(systemRef) > 0 {
		w.pendingSystemRef = systemRef
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		w.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		w.pendingContextCurrent = toIntFromKeys(cw, "currentSize")
		w.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		w.capturePendingModelMetadata(cw, model)
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			w.pendingUsage = usagePayloadFromMap(llm, true)
			w.capturePendingModelMetadata(w.pendingUsage, llm, model)
		}
	}
}

func (w *StepWriter) captureRootLLMRequestData(event stream.EventData) {
	if w == nil {
		return
	}
	model, _ := event.Value("model").(map[string]any)
	if len(model) > 0 {
		w.capturePendingModelMetadata(model)
	}
	if systemRef, _ := event.Value("systemRef").(map[string]any); len(systemRef) > 0 {
		w.pendingSystemRef = completeSystemRef(systemRef)
	}
	if inputMessages := messagesFromEventValue(event.Value("inputMessages")); len(inputMessages) > 0 {
		w.pendingInputMessages = filterSystemAuditInputMessages(inputMessages)
	}
}

func (w *StepWriter) captureTaskDebugData(buffer *taskStepBuffer, inner map[string]any) {
	if buffer == nil {
		return
	}
	model, _ := inner["model"].(map[string]any)
	if systemRef := systemRefFromDebugData(inner); len(systemRef) > 0 {
		buffer.pendingSystemRef = systemRef
	}
	if cw, ok := inner["contextWindow"].(map[string]any); ok {
		buffer.pendingContextWindowMax = toIntFromKeys(cw, "maxSize")
		buffer.pendingContextCurrent = toIntFromKeys(cw, "currentSize")
		buffer.pendingEstimated = toIntFromKeys(cw, "estimatedNextCallSize")
		buffer.capturePendingModelMetadata(cw, model)
	}
	if usage, ok := inner["usage"].(map[string]any); ok {
		if llm, ok := usage["llmReturnUsage"].(map[string]any); ok {
			buffer.pendingUsage = usagePayloadFromMap(llm, true)
			buffer.capturePendingModelMetadata(buffer.pendingUsage, llm, model)
		}
	}
}

func (w *StepWriter) captureTaskLLMRequestData(buffer *taskStepBuffer, event stream.EventData) {
	if buffer == nil {
		return
	}
	if agentKey := strings.TrimSpace(event.String("agentKey")); agentKey != "" {
		buffer.taskSubAgentKey = agentKey
	}
	if teamID := strings.TrimSpace(event.String("teamId")); teamID != "" {
		buffer.teamID = teamID
	}
	if presentation := strings.TrimSpace(event.String("presentation")); presentation != "" {
		buffer.presentation = presentation
	}
	model, _ := event.Value("model").(map[string]any)
	if len(model) > 0 {
		buffer.capturePendingModelMetadata(model)
	}
	if systemRef, _ := event.Value("systemRef").(map[string]any); len(systemRef) > 0 {
		buffer.pendingSystemRef = completeSystemRef(systemRef)
	}
	if inputMessages := messagesFromEventValue(event.Value("inputMessages")); len(inputMessages) > 0 {
		buffer.pendingInputMessages = filterSystemAuditInputMessages(inputMessages)
	}
}

func (w *StepWriter) capturePendingModelMetadata(values ...map[string]any) {
	if w == nil {
		return
	}
	captureStepModelMetadata(&w.pendingModelKey, &w.pendingReasoningEffort, values...)
}

func systemFromEventValue(value any) (QueryLineSystem, bool) {
	if typed, ok := value.(QueryLineSystem); ok {
		return cloneQueryLineSystem(typed), true
	}
	profile, _ := value.(map[string]any)
	return queryLineSystemFromProfile(profile)
}

func queryLineSystemFromProfile(profile map[string]any) (QueryLineSystem, bool) {
	if len(profile) == 0 {
		return QueryLineSystem{}, false
	}
	cacheKey := strings.TrimSpace(stringValue(profile["cacheKey"]))
	fingerprint := strings.TrimSpace(stringValue(profile["fingerprint"]))
	agentKey := strings.TrimSpace(stringValue(profile["agentKey"]))
	if agentKey == "" || cacheKey == "" || fingerprint == "" {
		return QueryLineSystem{}, false
	}
	model := cloneStepSystemPayload(anyMap(profile["model"]))
	return QueryLineSystem{
		AgentKey:       agentKey,
		CacheKey:       cacheKey,
		Fingerprint:    fingerprint,
		SystemMessage:  cloneStepSystemPayload(anyMap(profile["systemMessage"])),
		Tools:          cloneAnySliceDeep(anySlice(profile["tools"])),
		Model:          model,
		ToolChoice:     strings.TrimSpace(stringValue(profile["toolChoice"])),
		RequestOptions: cloneStepSystemPayload(anyMap(profile["requestOptions"])),
	}, true
}

func cloneQueryLineSystem(profile QueryLineSystem) QueryLineSystem {
	return QueryLineSystem{
		AgentKey:       profile.AgentKey,
		CacheKey:       profile.CacheKey,
		Fingerprint:    profile.Fingerprint,
		SystemMessage:  cloneStepSystemPayload(profile.SystemMessage),
		Tools:          cloneAnySliceDeep(profile.Tools),
		Model:          cloneStepSystemPayload(profile.Model),
		ToolChoice:     profile.ToolChoice,
		RequestOptions: cloneStepSystemPayload(profile.RequestOptions),
	}
}

func (w *StepWriter) flushCurrentStep() {
	w.flushCurrentStepAt(w.lastTimestamp)
}

func (w *StepWriter) flushCurrentStepAt(updatedAt int64) {
	if len(w.messages) == 0 && len(w.pendingAwaiting) == 0 && (w.pendingSources == nil || len(w.pendingSources.Items) == 0) {
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingContextCurrent = 0
		w.pendingEstimated = 0
		w.pendingModelKey = ""
		w.pendingReasoningEffort = ""
		w.pendingInputMessages = nil
		w.pendingSystemRef = nil
		w.pendingSources = nil
		return
	}

	if updatedAt <= 0 {
		// There is no source event time to persist. Do not turn it into a
		// plausible-looking current time; discard this invalid aggregation.
		w.clearCurrentStep()
		return
	}
	messages := append([]StoredMessage(nil), w.messages...)
	if w.pendingApproval != nil && approvalMatchesToolMessages(w.pendingApproval, messages) {
		if approvalMessage, ok := approvalAuditMessage(w.pendingApproval, updatedAt); ok {
			messages = append(messages, approvalMessage)
		}
		w.pendingApproval = nil
	}
	messages = canonicalizeStoredToolResultOrder(messages)
	messages = canonicalizeStoredToolResultOrderForToolIDs(messages, w.lastToolOrder)

	line := StepLine{
		ChatID:    w.chatID,
		RunID:     w.runID,
		UpdatedAt: updatedAt,
		LiveSeq:   w.stepLiveSeq,
		Messages:  messages,
	}
	if len(w.pendingAwaiting) > 0 {
		line.Awaiting = w.pendingAwaiting
		w.pendingAwaiting = nil
	}
	if w.pendingUsage != nil {
		line.Usage = w.pendingUsage
	}
	if systemRef := completeSystemRef(w.pendingSystemRef); len(systemRef) > 0 {
		line.SystemRef = systemRef
	}
	if len(w.pendingInputMessages) > 0 {
		line.InputMessages = cloneMessageMaps(w.pendingInputMessages)
	}
	if w.pendingUsage != nil || w.pendingContextWindowMax > 0 || w.pendingContextCurrent > 0 || w.pendingEstimated > 0 {
		if cw := buildContextWindow(w.pendingContextWindowMax, w.pendingContextCurrent, w.pendingEstimated); len(cw) > 0 {
			line.ContextWindow = cw
		}
		w.pendingUsage = nil
		w.pendingContextWindowMax = 0
		w.pendingContextCurrent = 0
		w.pendingEstimated = 0
		w.pendingSystemRef = nil
	}
	if w.pendingArtifacts != nil {
		line.Artifacts = cloneArtifactPublicationState(w.pendingArtifacts)
		w.pendingArtifacts = nil
	}
	if w.pendingSources != nil {
		line.Sources = cloneSourceState(w.pendingSources)
		w.pendingSources = nil
	}
	applyStepLineModelMetadata(&line, w.pendingModelKey, w.pendingReasoningEffort)

	if w.mode == "PLAN_EXECUTE" {
		line.Stage = w.currentStage
	}
	w.assignReactSeq(&line)

	if err := w.store.AppendStepLine(w.chatID, line); err != nil {
		w.recordPersistenceError(err)
		return
	}
	if order := assistantToolCallOrder(line.Messages); len(order) > 0 {
		w.lastToolOrder = order
	}
	w.clearCurrentStep()
}

func (w *StepWriter) clearCurrentStep() {
	if w == nil {
		return
	}
	w.messages = nil
	w.stepLiveSeq = 0
	w.pendingAwaiting = nil
	w.pendingApproval = nil
	w.pendingUsage = nil
	w.pendingContextWindowMax = 0
	w.pendingContextCurrent = 0
	w.pendingEstimated = 0
	w.pendingModelKey = ""
	w.pendingReasoningEffort = ""
	w.pendingInputMessages = nil
	w.pendingSystemRef = nil
	w.pendingArtifacts = nil
	w.pendingSources = nil
}

func (w *StepWriter) assignReactSeq(line *StepLine) {
	if line == nil {
		return
	}
	if stepLineStartsModelCall(*line) {
		line.Type = StepLineTypeReact
		w.seqCounter++
		line.Seq = w.seqCounter
		return
	}
	if stepLineIsReactToolContinuation(*line) {
		line.Type = StepLineTypeReactTool
	} else {
		line.Type = StepLineTypeReact
	}
	if stepLineCanReuseReactSeq(*line) && w.seqCounter > 0 {
		line.Seq = w.seqCounter
	}
}
