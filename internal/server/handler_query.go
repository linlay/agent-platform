package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
)

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	admission, err := s.prepareQueryAdmission(r, true)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	prepared, err := s.completeQueryPreparation(r.Context(), admission, nil)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if isProxyRoutedAgent(prepared.agentDef) {
		if isNonStreamingQuery(prepared.req) && !isSyncQueryContext(r.Context()) {
			s.handleProxyQueryNonStream(w, r, prepared)
			return
		}
		if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "ws" {
			s.handleProxyWebSocketQuery(w, r, prepared)
			return
		}
		s.handleProxyQuery(w, r, prepared)
		return
	}
	if isSyncQueryContext(r.Context()) {
		s.handleQuerySync(w, r.Context(), prepared)
		return
	}
	if isNonStreamingQuery(prepared.req) {
		s.handleQueryNonStream(w, r.Context(), prepared)
		return
	}
	s.handleQueryAsync(w, r, prepared)
}

func isNonStreamingQuery(req api.QueryRequest) bool {
	return req.Stream != nil && !*req.Stream
}

func (s *Server) handleQueryAsync(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	locale := requestLocale(r, s.deps.Config.I18N.DefaultLocale)
	runCtx, control, _ := s.deps.Runs.Register(r.Context(), prepared.session)
	principal := PrincipalFromContext(r.Context())
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})

	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonStreamWriterFailed, err.Error()))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, err := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if err != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, err.Error()))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)

	StartRunExecutor(RunExecutorParams{
		RunCtx:             runCtx,
		Request:            prepared.req,
		Session:            prepared.session,
		Summary:            prepared.summary,
		Agent:              s.deps.Agent,
		Registry:           s.deps.Registry,
		Assembler:          assembler,
		Mapper:             mapper,
		Stream:             s.deps.Config.Stream,
		Billing:            s.deps.Config.Billing,
		StepWriter:         stepWriter,
		EventBus:           eventBus,
		Chats:              s.deps.Chats,
		Models:             s.deps.Models,
		RunControl:         control,
		ResourceBaseURL:    prepared.resourceBaseURL,
		ResourceTickets:    s.ticketService,
		BuildQuerySession:  s.BuildQuerySession,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
		OnComplete: func(runID string) {
			releaseQuery(prepared.release)
			s.deps.Runs.Finish(runID)
			s.broadcast("run.finished", map[string]any{
				"runId":  runID,
				"chatId": prepared.req.ChatID,
			})
		},
	})

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", localizeStreamEventData(locale, event)); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleQuerySync(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	locale := i18n.ResolveLocale(s.deps.Config.I18N.DefaultLocale, responseLocale(w))
	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		releaseQuery(prepared.release)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	if _, err := s.runQuerySync(ctx, prepared, func(data stream.EventData) error {
		return sseWriter.WriteJSON("message", localizeStreamEventData(locale, data))
	}); err == nil {
		_ = sseWriter.WriteDone()
	}
}

func (s *Server) handleQueryNonStream(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	result, err := s.runQuerySync(ctx, prepared, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(queryResponseFromResult(prepared.req, result)))
}

type queryRunResult struct {
	AssistantText string
	FinishReason  string
	Usage         chat.UsageData
}

type queryEventCollector struct {
	assistantText strings.Builder
	finishReason  string
	usage         chat.UsageData
}

func (c *queryEventCollector) Consume(event stream.EventData) {
	if c == nil {
		return
	}
	switch event.Type {
	case "content.delta":
		if delta := event.String("delta"); delta != "" {
			c.assistantText.WriteString(delta)
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			c.assistantText.Reset()
			c.assistantText.WriteString(text)
		}
	case "content.end":
		if text := event.String("text"); text != "" && c.assistantText.Len() == 0 {
			c.assistantText.WriteString(text)
		}
	case "usage.snapshot":
		c.consumeUsage(event)
	case "run.complete":
		c.finishReason = "complete"
		c.consumeUsage(event)
	case "run.cancel":
		c.finishReason = "cancel"
		c.consumeUsage(event)
	case "run.error":
		c.finishReason = "error"
		c.consumeUsage(event)
	}
}

func (c *queryEventCollector) Result() queryRunResult {
	if c == nil {
		return queryRunResult{FinishReason: "complete"}
	}
	finishReason := strings.TrimSpace(c.finishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	return queryRunResult{
		AssistantText: c.assistantText.String(),
		FinishReason:  finishReason,
		Usage:         c.usage,
	}
}

func (c *queryEventCollector) consumeUsage(event stream.EventData) {
	if c == nil || event.Payload == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	if run, _ := usage["run"].(map[string]any); run != nil {
		mergeUsageMapIntoRunData(&c.usage, run)
		return
	}
	mergeUsageMapIntoRunData(&c.usage, usage)
}

func queryResponseFromResult(req api.QueryRequest, result queryRunResult) api.QueryResponse {
	finishReason := strings.TrimSpace(result.FinishReason)
	if finishReason == "" {
		finishReason = "complete"
	}
	return api.QueryResponse{
		RequestID:     req.RequestID,
		RunID:         req.RunID,
		ChatID:        req.ChatID,
		AgentKey:      req.AgentKey,
		AssistantText: result.AssistantText,
		FinishReason:  finishReason,
		Usage:         mapUsageDataPtr(&result.Usage),
	}
}

func (s *Server) runQuerySync(ctx context.Context, prepared preparedQuery, emitVisible func(stream.EventData) error) (queryRunResult, error) {
	defer releaseQuery(prepared.release)
	control := contracts.NewRunControl(ctx, prepared.req.RunID)
	control.SetObserverCount(1)
	runCtx := contracts.WithRunControl(control.Context(), control)
	defer control.SetObserverCount(0)

	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})
	defer s.broadcast("run.finished", map[string]any{
		"runId":  prepared.req.RunID,
		"chatId": prepared.req.ChatID,
	})

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	principal := &Principal{Subject: prepared.session.Subject}
	if strings.TrimSpace(principal.Subject) == "" {
		principal = nil
	}

	var (
		assistantText strings.Builder
		chatUsage     chat.UsageData
		runUsage      chat.UsageData
	)
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	processor := &runEventProcessor{
		assistantText: &assistantText,
		stepWriter:    chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled)),
		stream:        s.deps.Config.Stream,
		billing:       s.deps.Config.Billing,
		models:        s.deps.Models,
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}
	processor.stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	runCtx = chat.WithApprovalSummarySink(runCtx, processor.stepWriter.RecordApproval)
	writeEvent := func(event stream.StreamEvent) error {
		data, visible := processor.Consume(event)
		if !visible {
			return nil
		}
		if emitVisible == nil {
			return nil
		}
		return emitVisible(data)
	}

	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return queryRunResult{}, err
		}
	}

	agentStream, err := s.deps.Agent.Stream(runCtx, prepared.req, prepared.session)
	if err != nil {
		control.TransitionState(contracts.RunLoopStateFailed)
		for _, event := range assembler.Fail(err) {
			if writeErr := writeEvent(event); writeErr != nil {
				return queryRunResult{}, writeErr
			}
		}
		persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "error", false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		return queryRunResult{AssistantText: assistantText.String(), FinishReason: "error", Usage: runUsage}, nil
	}
	defer agentStream.Close()

	streamFailed := false
	streamInterrupted := false
	for {
		delta, nextErr := agentStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			streamInterrupted = true
			break
		}
		if nextErr != nil {
			streamFailed = true
			control.TransitionState(contracts.RunLoopStateFailed)
			for _, event := range assembler.Fail(nextErr) {
				if writeErr := writeEvent(event); writeErr != nil {
					return queryRunResult{}, writeErr
				}
			}
			break
		}
		inputs := mapper.Map(delta)
		for _, input := range inputs {
			for _, event := range assembler.Consume(input) {
				if err := writeEvent(event); err != nil {
					return queryRunResult{}, err
				}
			}
		}
	}

	processor.stepWriter.Flush()
	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, finishReason, false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		return queryRunResult{AssistantText: assistantText.String(), FinishReason: finishReason, Usage: runUsage}, nil
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return queryRunResult{}, err
		}
	}
	persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "complete", true)
	if persisted {
		syncBroadcastChatUpdated(s.deps.Notifications, completion)
	}
	return queryRunResult{AssistantText: assistantText.String(), FinishReason: "complete", Usage: runUsage}, nil
}

// syncRunExecutorParams 构造 handleQuerySync 三次持久化完成态调用所需参数
// 调用共用的 RunExecutorParams，避免重复拼装三份 callback。
func syncRunExecutorParams(s *Server, prepared preparedQuery, control *contracts.RunControl, principal *Principal) RunExecutorParams {
	return RunExecutorParams{
		Request:            prepared.req,
		Session:            prepared.session,
		Chats:              s.deps.Chats,
		RunControl:         control,
		ResourceBaseURL:    prepared.resourceBaseURL,
		ResourceTickets:    s.ticketService,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
	}
}

// syncBroadcastChatUpdated 复刻 run_executor.broadcastRunCompletion 的 chat.updated
// 广播语义。async 路径在 StartRunExecutor 内部走那条；sync 路径没经过 StartRunExecutor，
// 这里手动补上，让 automation 触发的 run 也能通知 hub（进而透传到 gateway / webclient）。
func syncBroadcastChatUpdated(notifications contracts.NotificationSink, completion chat.RunCompletion) {
	if notifications == nil {
		return
	}
	notifications.Broadcast("chat.updated", map[string]any{
		"chatId":         completion.ChatID,
		"lastRunId":      completion.RunID,
		"lastRunContent": completion.AssistantText,
		"updatedAt":      completion.UpdatedAtMillis,
	})
}
