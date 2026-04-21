package server

import (
	"context"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
)

type RunExecutorParams struct {
	RunCtx            context.Context
	Request           api.QueryRequest
	Session           contracts.QuerySession
	Summary           chat.Summary
	Agent             contracts.AgentEngine
	Registry          catalog.Registry
	Assembler         *stream.StreamEventAssembler
	Mapper            *llm.DeltaMapper
	SSE               config.SSEConfig
	StepWriter        *chat.StepWriter
	EventBus          *stream.RunEventBus
	Chats             chat.Store
	RunControl        *contracts.RunControl
	BuildQuerySession func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	Notifications     contracts.NotificationSink
	OnComplete        func(string)
}

type runEventProcessor struct {
	assistantText *strings.Builder
	stepWriter    *chat.StepWriter
	sse           config.SSEConfig
	chatUsage     chat.UsageData
	runUsage      *chat.UsageData
}

func (p *runEventProcessor) Consume(event stream.StreamEvent) (stream.EventData, bool) {
	data := event.Data()
	p.decorate(&data)
	if p.stepWriter != nil {
		p.stepWriter.OnEvent(data)
	}
	return data, isClientVisibleEvent(event.Type, p.sse)
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
				p.runUsage.PromptTokens = contracts.AnyIntNode(ru["promptTokens"])
				p.runUsage.CompletionTokens = contracts.AnyIntNode(ru["completionTokens"])
				p.runUsage.TotalTokens = contracts.AnyIntNode(ru["totalTokens"])
			}
		}
		usage["chatUsage"] = map[string]any{
			"promptTokens":     p.chatUsage.PromptTokens + p.runUsage.PromptTokens,
			"completionTokens": p.chatUsage.CompletionTokens + p.runUsage.CompletionTokens,
			"totalTokens":      p.chatUsage.TotalTokens + p.runUsage.TotalTokens,
		}
	case "run.complete", "run.error", "run.cancel":
		if p.runUsage != nil {
			if usage, ok := data.Payload["usage"].(map[string]any); ok {
				p.runUsage.PromptTokens = contracts.AnyIntNode(usage["promptTokens"])
				p.runUsage.CompletionTokens = contracts.AnyIntNode(usage["completionTokens"])
				p.runUsage.TotalTokens = contracts.AnyIntNode(usage["totalTokens"])
			}
			if data.Payload == nil {
				data.Payload = map[string]any{}
			}
			data.Payload["chatUsage"] = map[string]any{
				"promptTokens":     p.chatUsage.PromptTokens + p.runUsage.PromptTokens,
				"completionTokens": p.chatUsage.CompletionTokens + p.runUsage.CompletionTokens,
				"totalTokens":      p.chatUsage.TotalTokens + p.runUsage.TotalTokens,
			}
		}
	}
}

func isClientVisibleEvent(eventType string, sse config.SSEConfig) bool {
	if eventType == "stage.marker" {
		return false
	}
	if (eventType == "debug.preCall" || eventType == "debug.postCall") && !sse.IncludeDebugEvents {
		return false
	}
	return !strings.HasSuffix(eventType, ".snapshot")
}

func StartRunExecutor(params RunExecutorParams) {
	go runExecutor(params)
}

func runExecutor(params RunExecutorParams) {
	defer func() {
		if params.StepWriter != nil {
			params.StepWriter.Flush()
		}
		if params.EventBus != nil {
			params.EventBus.Freeze()
		}
		if params.OnComplete != nil {
			params.OnComplete(params.Session.RunID)
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
		sse:           params.SSE,
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}

	runCtx := params.RunCtx
	if params.StepWriter != nil {
		runCtx = llm.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	publish := func(event stream.StreamEvent) {
		data, visible := processor.Consume(event)
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
		persistRunCompletionIfNeeded(params, assistantText.String(), runUsage, false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		inputs := params.Mapper.Map(delta)
		for _, input := range inputs {
			for _, event := range params.Assembler.Consume(input) {
				publish(event)
			}
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			for _, event := range params.Assembler.Consume(input) {
				publish(event)
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
		buildQuerySession: params.BuildQuerySession,
		mapper:            params.Mapper,
		emitDelta:         emitDelta,
		emitInputs:        emitInputs,
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
		persistRunCompletionIfNeeded(params, assistantText.String(), runUsage, false)
		return
	}

	for _, event := range params.Assembler.Complete() {
		publish(event)
	}
	persistRunCompletionIfNeeded(params, assistantText.String(), runUsage, true)
}

func persistRunCompletionIfNeeded(params RunExecutorParams, assistantText string, runUsage chat.UsageData, always bool) {
	if params.Chats == nil {
		return
	}
	if !always && runUsage.TotalTokens == 0 {
		return
	}
	completion := chat.RunCompletion{
		ChatID:          params.Session.ChatID,
		RunID:           params.Session.RunID,
		AssistantText:   assistantText,
		InitialMessage:  params.Request.Message,
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage:           runUsage,
	}
	if err := params.Chats.OnRunCompleted(completion); err != nil {
		return
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
