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
	// OnComplete receives the same completion timestamp that is persisted for
	// the run.  Callers use it for run.finished rather than inventing a second
	// wall-clock value while publishing the notification.
	OnComplete func(runID string, completedAtMillis int64)
}

type runEventProcessor struct {
	assistantText        *strings.Builder
	stepWriter           *chat.StepWriter
	billing              config.BillingConfig
	models               *models.ModelRegistry
	chatUsage            chat.UsageData
	runUsage             *chat.UsageData
	runModelKey          string
	runModelMixed        bool
	aggregateUsageByTask bool
	taskRunUsage         map[string]chat.UsageData
}

type awaitingTracker struct {
	pendingAwaitingID string
	pendingMode       string
}

func (p *runEventProcessor) Consume(event stream.StreamEvent) (stream.EventData, bool, error) {
	data := event.Data()
	// Validate before decorate/StepWriter. A tool result can carry arbitrary
	// nested structured data, so deferring this to SSE JSON marshaling would
	// let an invalid time point leak into JSONL persistence first.
	if err := stream.ValidateEventData(data, "run.executor.event"); err != nil {
		return stream.EventData{}, false, err
	}
	p.decorate(&data)
	if p.stepWriter != nil {
		p.stepWriter.OnEvent(data)
		if err := p.stepWriter.Err(); err != nil {
			return stream.EventData{}, false, err
		}
	}
	return data, shouldPublishClientEvent(data), nil
}

func (p *runEventProcessor) decorate(data *stream.EventData) {
	if data == nil {
		return
	}
	switch data.Type {
	case "content.delta":
		if strings.TrimSpace(data.String("taskId")) != "" {
			return
		}
		if p.assistantText != nil {
			if delta := data.String("delta"); delta != "" {
				p.assistantText.WriteString(delta)
			}
		}
	case "content.snapshot":
		if strings.TrimSpace(data.String("taskId")) != "" {
			return
		}
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
		if p.runUsage != nil && !p.aggregateUsageByTask {
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
		if p.runUsage != nil && !p.aggregateUsageByTask {
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
	if p.aggregateUsageByTask {
		p.decorateAggregatedTaskUsageSnapshot(data, usage, currentUsage)
		return
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

func (p *runEventProcessor) decorateAggregatedTaskUsageSnapshot(data *stream.EventData, usage map[string]any, currentUsage chat.UsageData) {
	if p == nil || p.runUsage == nil || data == nil || usage == nil {
		return
	}
	if p.taskRunUsage == nil {
		p.taskRunUsage = map[string]chat.UsageData{}
	}
	key := strings.TrimSpace(data.String("taskId"))
	if key == "" {
		key = "__team_coordinator__"
	}
	accumulated := p.taskRunUsage[key]
	if usageEstimatedCostFromData(currentUsage) != nil {
		addEstimatedUsageCost(&accumulated, currentUsage)
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		mergeRunUsageData(&accumulated, usageDataFromMap(run))
	} else {
		accumulated = addUsageData(accumulated, currentUsage)
	}
	p.taskRunUsage[key] = accumulated

	total := chat.UsageData{}
	for _, taskUsage := range p.taskRunUsage {
		total = addUsageData(total, taskUsage)
	}
	*p.runUsage = total
	p.applyRunModelKey()
	runUsage := *p.runUsage
	runUsage.ModelKey = ""
	usage["run"] = usageDataMapForSnapshot(runUsage)
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

func shouldPublishClientEvent(data stream.EventData) bool {
	if data.Type == "request.query" && strings.TrimSpace(data.String("kind")) == "system-init" {
		return false
	}
	return isClientVisibleEvent(data.Type)
}

func clientVisibleEventData(data stream.EventData) stream.EventData {
	if data.Type != "request.query" || len(data.Payload) == 0 {
		return data
	}
	payload := make(map[string]any, len(data.Payload))
	for key, value := range data.Payload {
		if key == "messages" || key == "system" {
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
			params.OnComplete(params.Session.RunID, completion.UpdatedAtMillis)
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
	processor := &runEventProcessor{
		assistantText:        &assistantText,
		stepWriter:           params.StepWriter,
		billing:              params.Billing,
		models:               params.Models,
		chatUsage:            chatUsage,
		runUsage:             &runUsage,
		aggregateUsageByTask: params.Session.TeamRuntime != nil,
	}

	runCtx := params.RunCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancelExecution := context.WithCancel(runCtx)
	defer cancelExecution()
	if params.StepWriter != nil {
		runCtx = chat.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	var timeContractErr error
	publishBatch := func(rawEvents []stream.StreamEvent, normalizedEvents []stream.StreamEvent) error {
		if timeContractErr != nil {
			return timeContractErr
		}
		if len(rawEvents) == 0 && len(normalizedEvents) == 0 {
			return nil
		}
		processed := make(map[int64]stream.EventData, len(rawEvents))
		for _, event := range rawEvents {
			data, _, err := processor.Consume(event)
			if err != nil {
				timeContractErr = err
				// Stop the producer as soon as the bad event is observed. The
				// subsequent local run.error is platform-owned and is published
				// below instead of repairing this event.
				cancelExecution()
				return err
			}
			processed[event.Seq] = data
			handleAwaitingLifecycle(params, data, tracker)
		}
		for _, event := range normalizedEvents {
			data, ok := processed[event.Seq]
			if !ok {
				var err error
				data, _, err = processor.Consume(event)
				if err != nil {
					timeContractErr = err
					cancelExecution()
					return err
				}
				handleAwaitingLifecycle(params, data, tracker)
			}
			if shouldPublishClientEvent(data) && params.EventBus != nil {
				params.EventBus.Publish(clientVisibleEventData(data))
			}
		}
		return nil
	}

	publishAssembler := func(rawEvents []stream.StreamEvent, normalizedEvents []stream.StreamEvent) error {
		return publishBatch(rawEvents, normalizedEvents)
	}
	failTimeContract := func(err error) {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		publishLocalTimeContractRunError(params, err)
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
	}

	if err := publishAssembler(params.Assembler.BootstrapWithRaw()); err != nil {
		failTimeContract(err)
		return
	}

	agentStream, err := params.Agent.Stream(runCtx, params.Request, params.Session)
	if err != nil {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishAssembler(params.Assembler.FailWithRaw(err)); publishErr != nil {
			failTimeContract(publishErr)
			return
		}
		persisted, completion = persistRunCompletionWithReason(params, assistantText.String(), runUsage, "error", false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		if timeContractErr != nil {
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
			if timeContractErr != nil {
				return
			}
			if content, ok := input.(stream.ContentDelta); ok && params.Session.TeamRuntime != nil {
				content.ActorType = "team"
				content.TeamID = strings.TrimSpace(params.Session.TeamID)
				content.AgentKey = ""
				content.Presentation = "reply"
				input = content
			}
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishAssembler(params.Assembler.ConsumeWithRaw(input)); err != nil {
				return
			}
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			if timeContractErr != nil {
				return
			}
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishAssembler(params.Assembler.ConsumeWithRaw(input)); err != nil {
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
		nextLiveSeq:       params.Assembler.NextSeq,
	}

	streamFailed, streamInterrupted, orchestrateErr := orchestrator.Run(agentStream)
	if timeContractErr != nil {
		failTimeContract(timeContractErr)
		return
	}
	if orchestrateErr != nil {
		streamFailed = true
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishAssembler(params.Assembler.FailWithRaw(orchestrateErr)); publishErr != nil {
			failTimeContract(publishErr)
			return
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

	if err := publishAssembler(params.Assembler.CompleteWithRaw()); err != nil {
		failTimeContract(err)
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
			"resolvedAt": data.Timestamp,
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
	owner := contracts.ResolveRunOwner(session.RunOwner, session.AgentKey, session.TeamID)
	payload["ownerType"] = string(owner.Type)
	if owner.Type == contracts.RunOwnerTypeTeam {
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
			"resolvedAt": time.Now().UnixMilli(),
		}
		decorateNotificationRunOwner(payload, params.Session)
		params.Notifications.Broadcast("awaiting.answered", payload)
	}
	tracker.pendingAwaitingID = ""
	tracker.pendingMode = ""
}

func persistRunCompletionWithReason(params RunExecutorParams, assistantText string, runUsage chat.UsageData, finishReason string, notifyPersisted bool) (bool, chat.RunCompletion) {
	completedAtMillis := time.Now().UnixMilli()
	owner := contracts.ResolveRunOwner(params.Session.RunOwner, params.Session.AgentKey, params.Session.TeamID)
	completion := chat.RunCompletion{
		ChatID:          params.Session.ChatID,
		RunID:           params.Session.RunID,
		OwnerType:       string(owner.Type),
		AgentKey:        owner.AgentKey,
		AgentMode:       params.Session.Mode,
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
