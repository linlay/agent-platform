package server

import (
	"context"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/stream"
)

type RunExecutorParams struct {
	RunCtx             context.Context
	Request            api.QueryRequest
	Session            contracts.QuerySession
	StartedAtMillis    int64
	Summary            chat.Summary
	Agent              contracts.AgentEngine
	Registry           catalog.Registry
	Assembler          *stream.StreamEventAssembler
	Mapper             *llm.DeltaMapper
	Stream             config.StreamConfig
	StepWriter         *chat.StepWriter
	EventBus           *stream.RunEventBus
	Chats              chat.Store
	RunControl         *contracts.RunControl
	ResourceBaseURL    string
	ResourceTickets    *ResourceTicketService
	BuildQuerySession  func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	PrepareSystemInits func(api.QueryRequest, *contracts.QuerySession, bool) ([]chat.QueryLineSystemInit, error)
	BuildChildSystems  func(api.QueryRequest, *contracts.QuerySession) []chat.QueryLineSystemInit
	Notifications      contracts.NotificationSink
	OnUnreadChanged    func(chat.Summary)
	OnPersisted        func(chat.RunCompletion)
	OnComplete         func(string)
}

type runEventProcessor struct {
	assistantText *strings.Builder
	stepWriter    *chat.StepWriter
	stream        config.StreamConfig
	chatUsage     chat.UsageData
	runUsage      *chat.UsageData
}

type awaitingTracker struct {
	pendingAwaitingID string
	pendingMode       string
}

func (p *runEventProcessor) Consume(event stream.StreamEvent) (stream.EventData, bool) {
	data := event.Data()
	p.decorate(&data)
	if p.stepWriter != nil {
		p.stepWriter.OnEvent(data)
	}
	return data, isClientVisibleEvent(event.Type, p.stream)
}

func (p *runEventProcessor) decorate(data *stream.EventData) {
	if data == nil {
		return
	}
	switch data.Type {
	case "content.delta":
		if p.assistantText != nil {
			if delta := data.String("delta"); delta != "" {
				p.assistantText.WriteString(delta)
			}
		}
	case "content.snapshot":
		if p.assistantText != nil {
			if text := data.String("text"); text != "" {
				p.assistantText.Reset()
				p.assistantText.WriteString(text)
			}
		}
	case "debug.preCall", "debug.postCall":
		inner, ok := data.Payload["data"].(map[string]any)
		if !ok {
			return
		}
		usage, ok := inner["usage"].(map[string]any)
		if !ok {
			usage = map[string]any{}
			inner["usage"] = usage
		}
		if p.runUsage != nil {
			if ru, ok := usage["runUsage"].(map[string]any); ok {
				applyUsageMapToData(p.runUsage, ru)
			}
		}
		usage["chatUsage"] = usageDataMap(addUsageData(p.chatUsage, *p.runUsage))
	case "run.complete", "run.error", "run.cancel":
		if p.runUsage != nil {
			if usage, ok := data.Payload["usage"].(map[string]any); ok {
				if run, ok := usage["run"].(map[string]any); ok {
					applyUsageMapToData(p.runUsage, run)
				} else {
					applyUsageMapToData(p.runUsage, usage)
				}
			}
		}
		p.decorateTerminalUsage(data)
	}
}

func (p *runEventProcessor) decorateTerminalUsage(data *stream.EventData) {
	if data == nil || data.Payload == nil {
		return
	}
	delete(data.Payload, "chatUsage")
	if p.runUsage == nil || (p.runUsage.TotalTokens == 0 && p.runUsage.LlmChatCompletionCount == 0) {
		delete(data.Payload, "usage")
		return
	}
	data.Payload["usage"] = map[string]any{
		"chat": usageDataMap(addUsageData(p.chatUsage, *p.runUsage)),
		"run":  usageDataMap(*p.runUsage),
	}
}

func applyUsageMapToData(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	target.PromptTokens = contracts.AnyIntNode(usage["promptTokens"])
	target.CompletionTokens = contracts.AnyIntNode(usage["completionTokens"])
	target.TotalTokens = contracts.AnyIntNode(usage["totalTokens"])
	target.CachedTokens = usageDetailInt(usage, "promptTokensDetails", "cachedTokens")
	target.ReasoningTokens = usageDetailInt(usage, "completionTokensDetails", "reasoningTokens")
	target.PromptCacheHitTokens = contracts.AnyIntNode(usage["promptCacheHitTokens"])
	target.PromptCacheMissTokens = contracts.AnyIntNode(usage["promptCacheMissTokens"])
	target.LlmChatCompletionCount = contracts.AnyIntNode(usage["llmChatCompletionCount"])
}

func applyTerminalEventUsage(target *chat.UsageData, event stream.EventData) {
	if target == nil {
		return
	}
	usage, ok := event.Payload["usage"].(map[string]any)
	if !ok {
		return
	}
	if run, ok := usage["run"].(map[string]any); ok {
		applyUsageMapToData(target, run)
		return
	}
	applyUsageMapToData(target, usage)
}

func usageDetailInt(usage map[string]any, detailKey string, valueKey string) int {
	details, _ := usage[detailKey].(map[string]any)
	return contracts.AnyIntNode(details[valueKey])
}

func addUsageData(base chat.UsageData, delta chat.UsageData) chat.UsageData {
	return chat.UsageData{
		PromptTokens:           base.PromptTokens + delta.PromptTokens,
		CompletionTokens:       base.CompletionTokens + delta.CompletionTokens,
		TotalTokens:            base.TotalTokens + delta.TotalTokens,
		CachedTokens:           base.CachedTokens + delta.CachedTokens,
		ReasoningTokens:        base.ReasoningTokens + delta.ReasoningTokens,
		PromptCacheHitTokens:   base.PromptCacheHitTokens + delta.PromptCacheHitTokens,
		PromptCacheMissTokens:  base.PromptCacheMissTokens + delta.PromptCacheMissTokens,
		LlmChatCompletionCount: base.LlmChatCompletionCount + delta.LlmChatCompletionCount,
	}
}

func usageDataMap(usage chat.UsageData) map[string]any {
	out := map[string]any{
		"promptTokens":     usage.PromptTokens,
		"completionTokens": usage.CompletionTokens,
		"totalTokens":      usage.TotalTokens,
	}
	if usage.CachedTokens > 0 {
		out["promptTokensDetails"] = map[string]any{"cachedTokens": usage.CachedTokens}
	}
	if usage.ReasoningTokens > 0 {
		out["completionTokensDetails"] = map[string]any{"reasoningTokens": usage.ReasoningTokens}
	}
	if usage.PromptCacheHitTokens > 0 {
		out["promptCacheHitTokens"] = usage.PromptCacheHitTokens
	}
	if usage.PromptCacheMissTokens > 0 {
		out["promptCacheMissTokens"] = usage.PromptCacheMissTokens
	}
	if usage.LlmChatCompletionCount > 0 {
		out["llmChatCompletionCount"] = usage.LlmChatCompletionCount
	}
	return out
}

func isClientVisibleEvent(eventType string, streamCfg config.StreamConfig) bool {
	if (eventType == "debug.preCall" || eventType == "debug.postCall") && !streamCfg.DebugEventsEnabled {
		return false
	}
	if (eventType == "tool.args" || eventType == "tool.result") && !streamCfg.IncludeToolPayloadEvents {
		return false
	}
	return !strings.HasSuffix(eventType, ".snapshot")
}

func StartRunExecutor(params RunExecutorParams) {
	go runExecutor(params)
}

func runExecutor(params RunExecutorParams) {
	if params.StartedAtMillis <= 0 {
		params.StartedAtMillis = time.Now().UnixMilli()
	}
	tracker := &awaitingTracker{}
	var (
		persisted  bool
		completion chat.RunCompletion
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
			params.OnComplete(params.Session.RunID)
		}
		if persisted {
			broadcastRunCompletion(params, completion)
		}
	}()

	var (
		assistantText strings.Builder
		runUsage      chat.UsageData
		chatUsage     chat.UsageData
	)
	if params.Summary.Usage != nil {
		chatUsage = *params.Summary.Usage
	}
	processor := &runEventProcessor{
		assistantText: &assistantText,
		stepWriter:    params.StepWriter,
		stream:        params.Stream,
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}

	runCtx := params.RunCtx
	if params.StepWriter != nil {
		runCtx = llm.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	publish := func(event stream.StreamEvent) {
		data, visible := processor.Consume(event)
		handleAwaitingLifecycle(params, data, tracker)
		if visible && params.EventBus != nil {
			params.EventBus.Publish(data)
		}
	}

	for _, event := range params.Assembler.Bootstrap() {
		publish(event)
	}

	agentStream, err := params.Agent.Stream(runCtx, params.Request, params.Session)
	if err != nil {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		for _, event := range params.Assembler.Fail(err) {
			publish(event)
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		inputs := params.Mapper.Map(delta)
		for _, input := range inputs {
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			for _, event := range params.Assembler.Consume(input) {
				publish(event)
			}
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			for _, event := range params.Assembler.Consume(input) {
				publish(event)
			}
		}
	}

	orchestrator := &frameOrchestrator{
		runCtx:             runCtx,
		request:            params.Request,
		session:            params.Session,
		summary:            params.Summary,
		agent:              params.Agent,
		registry:           params.Registry,
		buildQuerySession:  params.BuildQuerySession,
		chats:              params.Chats,
		resourceBaseURL:    params.ResourceBaseURL,
		resourceTickets:    params.ResourceTickets,
		prepareSystemInits: params.PrepareSystemInits,
		buildChildSystems:  params.BuildChildSystems,
		mapper:             params.Mapper,
		emitDelta:          emitDelta,
		emitInputs:         emitInputs,
	}

	streamFailed, streamInterrupted, orchestrateErr := orchestrator.Run(agentStream)
	if orchestrateErr != nil {
		streamFailed = true
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		for _, event := range params.Assembler.Fail(orchestrateErr) {
			publish(event)
		}
	}

	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, finishReason, false)
		return
	}

	for _, event := range params.Assembler.Complete() {
		publish(event)
	}
	persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "complete", true)
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
		tracker.pendingAwaitingID = awaitingID
		tracker.pendingMode = mode
		if params.Notifications != nil {
			payload := map[string]any{
				"chatId":     params.Session.ChatID,
				"runId":      runID,
				"agentKey":   params.Session.AgentKey,
				"awaitingId": awaitingID,
				"mode":       mode,
				"timeout":    contracts.AnyIntNode(data.Value("timeout")),
				"createdAt":  data.Timestamp,
			}
			if viewportType := strings.TrimSpace(data.String("viewportType")); viewportType != "" {
				payload["viewportType"] = viewportType
			}
			if viewportKey := strings.TrimSpace(data.String("viewportKey")); viewportKey != "" {
				payload["viewportKey"] = viewportKey
			}
			params.Notifications.Broadcast("awaiting.ask", payload)
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
			"resolvedAt": data.Timestamp,
		}
		if errorCode := awaitingAnswerErrorCode(data); errorCode != "" {
			payload["errorCode"] = errorCode
		}
		if params.Notifications != nil {
			params.Notifications.Broadcast("awaiting.answer", payload)
		}
	}
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
		params.Notifications.Broadcast("awaiting.answer", map[string]any{
			"chatId":     params.Session.ChatID,
			"runId":      params.Session.RunID,
			"awaitingId": tracker.pendingAwaitingID,
			"mode":       tracker.pendingMode,
			"status":     "error",
			"errorCode":  "run_interrupted",
			"resolvedAt": time.Now().UnixMilli(),
		})
	}
	tracker.pendingAwaitingID = ""
	tracker.pendingMode = ""
}

func persistRunCompletionWithReason(params RunExecutorParams, assistantText string, runUsage chat.UsageData, finishReason string, notifyPersisted bool) (bool, chat.RunCompletion) {
	if params.Chats == nil {
		return false, chat.RunCompletion{}
	}
	now := time.Now().UnixMilli()
	if params.StartedAtMillis <= 0 {
		if startedAt, ok := chat.ParseRunIDMillis(params.Session.RunID); ok {
			params.StartedAtMillis = startedAt
		} else {
			params.StartedAtMillis = now
		}
	}
	completion := chat.RunCompletion{
		ChatID:          params.Session.ChatID,
		RunID:           params.Session.RunID,
		AgentKey:        params.Session.AgentKey,
		AssistantText:   assistantText,
		InitialMessage:  params.Request.Message,
		FinishReason:    finishReason,
		StartedAtMillis: params.StartedAtMillis,
		UpdatedAtMillis: now,
		Usage:           runUsage,
	}
	if err := params.Chats.OnRunCompleted(completion); err != nil {
		return false, chat.RunCompletion{}
	}
	if notifyPersisted && finishReason == "complete" && params.OnPersisted != nil {
		params.OnPersisted(completion)
	}
	return true, completion
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
