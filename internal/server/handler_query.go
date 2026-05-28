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
	"agent-platform/internal/stream"
)

// isHiddenRequest 判断请求是否标记为"系统自发触发"：
// 这类 run 的 QueryLine 仍会写入 chat JSONL 以保留完整 trace，
// 但会携带 hidden 标记，供 webclient / export / search 避免渲染成可见用户发言。
// 典型来源：automation 触发的定时任务。
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
	release, availability := s.tryAcquireQuery(admission)
	if !availability.CanQuery {
		writeJSON(w, http.StatusTooManyRequests, queryAvailabilityFailure(availability))
		return
	}
	prepared, err := s.completeQueryPreparation(r.Context(), admission, release)
	if err != nil {
		releaseQuery(release)
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if isProxyRoutedAgent(prepared.agentDef) {
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
	s.handleQueryAsync(w, r, prepared)
}

func (s *Server) handleQueryAsync(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	runCtx, control, _ := s.deps.Runs.Register(r.Context(), prepared.session)
	principal := PrincipalFromContext(r.Context())
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
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
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, err := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if err != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req), chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
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
		StepWriter:         stepWriter,
		EventBus:           eventBus,
		Chats:              s.deps.Chats,
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
			if err := sseWriter.WriteJSON("message", event); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleQuerySync(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
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
	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

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
		stepWriter:    chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req), chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled)),
		stream:        s.deps.Config.Stream,
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
		return sseWriter.WriteJSON("message", data)
	}

	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	agentStream, err := s.deps.Agent.Stream(runCtx, prepared.req, prepared.session)
	if err != nil {
		control.TransitionState(contracts.RunLoopStateFailed)
		for _, event := range assembler.Fail(err) {
			_ = writeEvent(event)
		}
		persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "error", false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		_ = sseWriter.WriteDone()
		return
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
					return
				}
			}
			break
		}
		inputs := mapper.Map(delta)
		for _, input := range inputs {
			for _, event := range assembler.Consume(input) {
				if err := writeEvent(event); err != nil {
					return
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
		_ = sseWriter.WriteDone()
		return
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return
		}
	}
	persisted, completion := persistRunCompletionWithReason(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, "complete", true)
	if persisted {
		syncBroadcastChatUpdated(s.deps.Notifications, completion)
	}
	_ = sseWriter.WriteDone()
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
