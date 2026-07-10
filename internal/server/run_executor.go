package server

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
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
	Mapper             contracts.StreamDeltaMapper
	Billing            config.BillingConfig
	StepWriter         *chat.StepWriter
	EventBus           *stream.RunEventBus
	Chats              chat.Store
	Models             *models.ModelRegistry
	RunControl         *contracts.RunControl
	ResourceBaseURL    string
	ResourceTickets    *ResourceTicketService
	BuildQuerySession  func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	PrepareSystemInits func(api.QueryRequest, *contracts.QuerySession, bool) ([]chat.QueryLineSystemInit, error)
	BuildChildSystems  func(api.QueryRequest, *contracts.QuerySession) []chat.QueryLineSystemInit
	Notifications      contracts.NotificationSink
	OnUnreadChanged    func(chat.Summary)
	OnPersisted        func(chat.RunCompletion)
	OnContinuation     func(contracts.DeltaRunContinuation) (string, error)
	OnComplete         func(string)
}

type runEventProcessor struct {
	assistantText *strings.Builder
	stepWriter    *chat.StepWriter
	billing       config.BillingConfig
	models        *models.ModelRegistry
	chatUsage     chat.UsageData
	runUsage      *chat.UsageData
	runModelKey   string
	runModelMixed bool
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
	return data, isClientVisibleEvent(event.Type)
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
	case "debug.llmChat":
		inner, ok := data.Payload["data"].(map[string]any)
		if !ok {
			return
		}
		usage, ok := inner["usage"].(map[string]any)
		if !ok {
			usage = map[string]any{}
			inner["usage"] = usage
		}
		(usageCostDecorator{models: p.models, billing: p.billing}).decorateDebugLLMReturnUsage(inner)
		if p.runUsage != nil {
			if ru, ok := usage["runUsage"].(map[string]any); ok {
				mergeUsageMapIntoRunData(p.runUsage, ru)
				p.applyRunModelKey()
			}
		}
		if p.runUsage != nil {
			chatUsage := addUsageData(p.chatUsage, *p.runUsage)
			chatUsage.ModelKey = ""
			usage["chatUsage"] = usageDataMap(chatUsage)
		}
	case "usage.snapshot":
		usage, ok := data.Payload["usage"].(map[string]any)
		if !ok {
			return
		}
		p.decorateUsageSnapshot(data)
		if p.runUsage != nil {
			chatUsage := addUsageData(p.chatUsage, *p.runUsage)
			chatUsage.ModelKey = ""
			usage["chat"] = usageDataMapForSnapshot(chatUsage)
		}
	case "run.complete", "run.error", "run.cancel":
		if p.runUsage != nil {
			if usage, ok := data.Payload["usage"].(map[string]any); ok {
				if run, ok := usage["run"].(map[string]any); ok {
					mergeUsageMapIntoRunData(p.runUsage, run)
				} else {
					mergeUsageMapIntoRunData(p.runUsage, usage)
				}
			}
		}
		p.decorateTerminalUsage(data)
	}
}

func (p *runEventProcessor) decorateUsageSnapshot(data *stream.EventData) {
	if p == nil || data == nil {
		return
	}
	usage, _ := data.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	var (
		currentUsage chat.UsageData
		hasCurrent   bool
	)
	if current, _ := usage["current"].(map[string]any); current != nil {
		currentUsage, hasCurrent = (usageCostDecorator{models: p.models, billing: p.billing}).decorateCurrentUsage(data)
		if modelKey := strings.TrimSpace(currentUsage.ModelKey); modelKey != "" {
			p.recordRunModelKey(modelKey)
		}
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		if p.runUsage != nil {
			if usageEstimatedCostFromData(currentUsage) != nil {
				addEstimatedUsageCost(p.runUsage, currentUsage)
			}
			mergeUsageMapIntoRunData(p.runUsage, run)
			p.applyRunModelKey()
			runUsage := *p.runUsage
			runUsage.ModelKey = ""
			usage["run"] = usageDataMapForSnapshot(runUsage)
		}
	} else if hasCurrent && p.runUsage != nil {
		*p.runUsage = addUsageData(*p.runUsage, currentUsage)
		p.applyRunModelKey()
		runUsage := *p.runUsage
		runUsage.ModelKey = ""
		usage["run"] = usageDataMapForSnapshot(runUsage)
	}
}

func (p *runEventProcessor) recordRunModelKey(modelKey string) {
	if p == nil || p.runModelMixed {
		return
	}
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return
	}
	if p.runModelKey == "" {
		p.runModelKey = modelKey
		return
	}
	if p.runModelKey != modelKey {
		p.runModelKey = ""
		p.runModelMixed = true
	}
}

func (p *runEventProcessor) applyRunModelKey() {
	if p == nil || p.runUsage == nil {
		return
	}
	if p.runModelMixed {
		p.runUsage.ModelKey = ""
		return
	}
	p.runUsage.ModelKey = strings.TrimSpace(p.runModelKey)
}

func (p *runEventProcessor) decorateTerminalUsage(data *stream.EventData) {
	if data == nil || data.Payload == nil {
		return
	}
	delete(data.Payload, "chatUsage")
	if p.runUsage == nil || !usageHasData(*p.runUsage) {
		delete(data.Payload, "usage")
		return
	}
	p.applyRunModelKey()
	chatUsage := addUsageData(p.chatUsage, *p.runUsage)
	chatUsage.ModelKey = ""
	runUsage := *p.runUsage
	runUsage.ModelKey = ""
	data.Payload["usage"] = map[string]any{
		"chat": usageDataMap(chatUsage),
		"run":  usageDataMap(runUsage),
	}
}

func applyUsageMapToData(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	*target = usageDataFromMap(usage)
}

func usageDataFromMap(usage map[string]any) chat.UsageData {
	out := chat.UsageData{
		ModelKey:               strings.TrimSpace(contracts.AnyStringNode(usage["modelKey"])),
		PromptTokens:           contracts.AnyIntNode(usage["promptTokens"]),
		CompletionTokens:       contracts.AnyIntNode(usage["completionTokens"]),
		TotalTokens:            contracts.AnyIntNode(usage["totalTokens"]),
		ReasoningTokens:        usageDetailInt(usage, "completionTokensDetails", "reasoningTokens"),
		LlmChatCompletionCount: contracts.AnyIntNode(usage["llmChatCompletionCount"]),
		ToolCallCount:          contracts.AnyIntNode(usage["toolCallCount"]),
	}
	cacheHitTokens, cacheMissTokens := usageCacheTokensFromMap(usage)
	out.CachedTokens = cacheHitTokens
	out.PromptCacheHitTokens = cacheHitTokens
	out.PromptCacheMissTokens = cacheMissTokens
	if estimatedCost := estimatedCostFromMap(usage); estimatedCost != nil {
		out.EstimatedCostCurrency = strings.ToUpper(strings.TrimSpace(contracts.AnyStringNode(estimatedCost["currency"])))
		out.EstimatedCostInputHit = floatValue(estimatedCost["inputCacheHit"])
		out.EstimatedCostInputMiss = floatValue(estimatedCost["inputCacheMiss"])
		out.EstimatedCostOutput = floatValue(estimatedCost["output"])
		out.EstimatedCostTotal = floatValue(estimatedCost["total"])
	}
	applyUsageTimingFromMap(&out, usage)
	return out
}

func mergeUsageMapIntoRunData(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	incoming := usageDataFromMap(usage)
	mergeRunUsageData(target, incoming)
}

func mergeRunUsageData(target *chat.UsageData, incoming chat.UsageData) {
	if target == nil {
		return
	}
	modelKey := target.ModelKey
	currency := target.EstimatedCostCurrency
	inputHit := target.EstimatedCostInputHit
	inputMiss := target.EstimatedCostInputMiss
	output := target.EstimatedCostOutput
	total := target.EstimatedCostTotal
	firstTokenLatencyTotalMs := target.FirstTokenLatencyTotalMs
	firstTokenLatencyCount := target.FirstTokenLatencyCount
	generationDurationMs := target.GenerationDurationMs
	*target = incoming
	if strings.TrimSpace(incoming.ModelKey) == "" {
		target.ModelKey = modelKey
	}
	if strings.TrimSpace(incoming.EstimatedCostCurrency) == "" {
		target.EstimatedCostCurrency = currency
		target.EstimatedCostInputHit = inputHit
		target.EstimatedCostInputMiss = inputMiss
		target.EstimatedCostOutput = output
		target.EstimatedCostTotal = total
	}
	if incoming.FirstTokenLatencyTotalMs == 0 && incoming.FirstTokenLatencyCount == 0 && incoming.GenerationDurationMs == 0 {
		target.FirstTokenLatencyTotalMs = firstTokenLatencyTotalMs
		target.FirstTokenLatencyCount = firstTokenLatencyCount
		target.GenerationDurationMs = generationDurationMs
	}
}

func addEstimatedUsageCost(target *chat.UsageData, delta chat.UsageData) {
	if target == nil || strings.TrimSpace(delta.EstimatedCostCurrency) == "" {
		return
	}
	if strings.TrimSpace(target.EstimatedCostCurrency) == "" {
		target.EstimatedCostCurrency = strings.ToUpper(strings.TrimSpace(delta.EstimatedCostCurrency))
	}
	target.EstimatedCostInputHit += delta.EstimatedCostInputHit
	target.EstimatedCostInputMiss += delta.EstimatedCostInputMiss
	target.EstimatedCostOutput += delta.EstimatedCostOutput
	target.EstimatedCostTotal += delta.EstimatedCostTotal
}

func estimatedCostFromMap(usage map[string]any) map[string]any {
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	return estimatedCost
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func usageDetailInt(usage map[string]any, detailKey string, valueKey string) int {
	details, _ := usage[detailKey].(map[string]any)
	return contracts.AnyIntNode(details[valueKey])
}

func applyUsageTimingFromMap(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	timing, _ := usage["timing"].(map[string]any)
	if timing == nil {
		return
	}
	firstTokenLatencyTotalMs := int64(contracts.AnyIntNode(timing["firstTokenLatencyTotalMs"]))
	firstTokenLatencyCount := contracts.AnyIntNode(timing["firstTokenLatencyCount"])
	if firstTokenLatencyTotalMs <= 0 || firstTokenLatencyCount <= 0 {
		if firstTokenLatencyMs := int64(contracts.AnyIntNode(timing["firstTokenLatencyMs"])); firstTokenLatencyMs > 0 {
			firstTokenLatencyTotalMs = firstTokenLatencyMs
			firstTokenLatencyCount = 1
		}
	}
	target.FirstTokenLatencyTotalMs = firstTokenLatencyTotalMs
	target.FirstTokenLatencyCount = firstTokenLatencyCount
	target.GenerationDurationMs = int64(contracts.AnyIntNode(timing["generationDurationMs"]))
}

func addUsageData(base chat.UsageData, delta chat.UsageData) chat.UsageData {
	return chat.UsageData{
		ModelKey:                 mergedUsageModelKey(base, delta),
		PromptTokens:             base.PromptTokens + delta.PromptTokens,
		CompletionTokens:         base.CompletionTokens + delta.CompletionTokens,
		TotalTokens:              base.TotalTokens + delta.TotalTokens,
		CachedTokens:             base.CachedTokens + delta.CachedTokens,
		ReasoningTokens:          base.ReasoningTokens + delta.ReasoningTokens,
		PromptCacheHitTokens:     base.PromptCacheHitTokens + delta.PromptCacheHitTokens,
		PromptCacheMissTokens:    base.PromptCacheMissTokens + delta.PromptCacheMissTokens,
		EstimatedCostCurrency:    firstNonBlank(base.EstimatedCostCurrency, delta.EstimatedCostCurrency),
		EstimatedCostInputHit:    base.EstimatedCostInputHit + delta.EstimatedCostInputHit,
		EstimatedCostInputMiss:   base.EstimatedCostInputMiss + delta.EstimatedCostInputMiss,
		EstimatedCostOutput:      base.EstimatedCostOutput + delta.EstimatedCostOutput,
		EstimatedCostTotal:       base.EstimatedCostTotal + delta.EstimatedCostTotal,
		LlmChatCompletionCount:   base.LlmChatCompletionCount + delta.LlmChatCompletionCount,
		ToolCallCount:            base.ToolCallCount + delta.ToolCallCount,
		FirstTokenLatencyTotalMs: base.FirstTokenLatencyTotalMs + delta.FirstTokenLatencyTotalMs,
		FirstTokenLatencyCount:   base.FirstTokenLatencyCount + delta.FirstTokenLatencyCount,
		GenerationDurationMs:     base.GenerationDurationMs + delta.GenerationDurationMs,
	}
}

func addUsageTimingMap(out map[string]any, usage chat.UsageData) {
	if out == nil {
		return
	}
	timing := map[string]any{}
	if usage.FirstTokenLatencyCount > 0 {
		timing["firstTokenLatencyTotalMs"] = usage.FirstTokenLatencyTotalMs
		timing["firstTokenLatencyCount"] = usage.FirstTokenLatencyCount
	}
	if usage.GenerationDurationMs > 0 {
		timing["generationDurationMs"] = usage.GenerationDurationMs
	}
	if len(timing) > 0 {
		out["timing"] = timing
	}
}

func usageDataMap(usage chat.UsageData) map[string]any {
	return usageDataMapWithOptions(usage, false)
}

func usageDataMapForSnapshot(usage chat.UsageData) map[string]any {
	return usageDataMapWithOptions(usage, true)
}

func usageDataMapWithOptions(usage chat.UsageData, includeZeroToolCallCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     usage.PromptTokens,
		"completionTokens": usage.CompletionTokens,
		"totalTokens":      usage.TotalTokens,
	}
	if modelKey := strings.TrimSpace(usage.ModelKey); modelKey != "" {
		out["modelKey"] = modelKey
	}
	if usage.CachedTokens > 0 {
		out["promptTokensDetails"] = map[string]any{"cacheHitTokens": usage.CachedTokens}
	}
	if usage.ReasoningTokens > 0 || includeZeroToolCallCount {
		out["completionTokensDetails"] = map[string]any{"reasoningTokens": usage.ReasoningTokens}
	}
	cacheHitTokens, cacheMissTokens := usageCacheTokens(usage)
	if cacheHitTokens > 0 || cacheMissTokens > 0 {
		promptDetails, _ := out["promptTokensDetails"].(map[string]any)
		if promptDetails == nil {
			promptDetails = map[string]any{}
			out["promptTokensDetails"] = promptDetails
		}
		if cacheHitTokens > 0 || includeZeroToolCallCount {
			promptDetails["cacheHitTokens"] = cacheHitTokens
		}
		if cacheMissTokens > 0 || includeZeroToolCallCount {
			promptDetails["cacheMissTokens"] = cacheMissTokens
		}
	}
	if usage.LlmChatCompletionCount > 0 {
		out["llmChatCompletionCount"] = usage.LlmChatCompletionCount
	}
	if usage.ToolCallCount > 0 || includeZeroToolCallCount {
		out["toolCallCount"] = usage.ToolCallCount
	}
	if estimated := usageEstimatedCostFromData(usage); estimated != nil {
		out["estimatedCost"] = estimated
	}
	addUsageTimingMap(out, usage)
	return out
}

func mergedUsageModelKey(base chat.UsageData, delta chat.UsageData) string {
	baseKey := strings.TrimSpace(base.ModelKey)
	deltaKey := strings.TrimSpace(delta.ModelKey)
	if baseKey == "" && !usageHasData(base) {
		return deltaKey
	}
	if deltaKey == "" && !usageHasData(delta) {
		return baseKey
	}
	if baseKey != "" && baseKey == deltaKey {
		return baseKey
	}
	return ""
}

func usageHasData(usage chat.UsageData) bool {
	return usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 ||
		usage.LlmChatCompletionCount > 0 || usage.ToolCallCount > 0 ||
		usage.EstimatedCostTotal > 0 || strings.TrimSpace(usage.EstimatedCostCurrency) != "" ||
		usage.FirstTokenLatencyTotalMs > 0 || usage.FirstTokenLatencyCount > 0 || usage.GenerationDurationMs > 0
}

func isClientVisibleEvent(eventType string) bool {
	if eventType == "llm.request" {
		return false
	}
	if eventType == "debug.llmChat" {
		return true
	}
	if eventType == "usage.snapshot" {
		return true
	}
	if eventType == "run.activity" {
		return true
	}
	return !strings.HasSuffix(eventType, ".snapshot")
}

func clientVisibleEventData(data stream.EventData) stream.EventData {
	if data.Type != "request.query" || len(data.Payload) == 0 {
		return data
	}
	payload := make(map[string]any, len(data.Payload))
	for key, value := range data.Payload {
		if key == "messages" || key == "systems" {
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
	if params.StartedAtMillis <= 0 {
		params.StartedAtMillis = time.Now().UnixMilli()
	}
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
			params.OnComplete(params.Session.RunID)
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
		billing:       params.Billing,
		models:        params.Models,
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}

	runCtx := params.RunCtx
	if params.StepWriter != nil {
		runCtx = chat.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	publishBatch := func(rawEvents []stream.StreamEvent, normalizedEvents []stream.StreamEvent) {
		if len(rawEvents) == 0 && len(normalizedEvents) == 0 {
			return
		}
		processed := make(map[int64]stream.EventData, len(rawEvents))
		for _, event := range rawEvents {
			data, _ := processor.Consume(event)
			processed[event.Seq] = data
			handleAwaitingLifecycle(params, data, tracker)
		}
		for _, event := range normalizedEvents {
			data, ok := processed[event.Seq]
			if !ok {
				data, _ = processor.Consume(event)
				handleAwaitingLifecycle(params, data, tracker)
			}
			if isClientVisibleEvent(data.Type) && params.EventBus != nil {
				params.EventBus.Publish(clientVisibleEventData(data))
			}
		}
	}

	publishAssembler := func(rawEvents []stream.StreamEvent, normalizedEvents []stream.StreamEvent) {
		publishBatch(rawEvents, normalizedEvents)
	}

	publishAssembler(params.Assembler.BootstrapWithRaw())

	agentStream, err := params.Agent.Stream(runCtx, params.Request, params.Session)
	if err != nil {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		publishAssembler(params.Assembler.FailWithRaw(err))
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		if value, ok := delta.(contracts.DeltaRunContinuation); ok {
			cloned := value
			cloned.Answer = contracts.CloneMap(value.Answer)
			continuation = &cloned
			return
		}
		inputs := params.Mapper.Map(delta)
		for _, input := range inputs {
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			publishAssembler(params.Assembler.ConsumeWithRaw(input))
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			publishAssembler(params.Assembler.ConsumeWithRaw(input))
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
		nextLiveSeq:        params.Assembler.NextSeq,
	}

	streamFailed, streamInterrupted, orchestrateErr := orchestrator.Run(agentStream)
	if orchestrateErr != nil {
		streamFailed = true
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		publishAssembler(params.Assembler.FailWithRaw(orchestrateErr))
	}

	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, finishReason, false)
		return
	}

	publishAssembler(params.Assembler.CompleteWithRaw())
	persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "complete", true)
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
				Timeout:          int64(contracts.AnyIntNode(data.Value("timeout"))),
			})
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
			"resolvedAt": data.Timestamp,
		}
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

func awaitingEventItemCount(data stream.EventData) int {
	switch strings.ToLower(strings.TrimSpace(data.String("mode"))) {
	case "question":
		return awaitingPayloadItemCount(data.Value("questions"))
	case "approval":
		return awaitingPayloadItemCount(data.Value("approvals"))
	case "form":
		return awaitingPayloadItemCount(data.Value("forms"))
	case "plan":
		if lenAnyMap(data.Value("plan")) > 0 {
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
		params.Notifications.Broadcast("awaiting.answered", map[string]any{
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
