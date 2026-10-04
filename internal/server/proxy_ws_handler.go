package server

import (
	"context"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	"agent-platform/internal/stream"
)

type proxyRunRoute = runtimeproxy.Route

func (s *Server) registerProxyRun(route *proxyRunRoute) {
	if s == nil || s.proxyRuntime == nil {
		return
	}
	s.proxyRuntime.Register(route)
}

func (s *Server) unregisterProxyRun(runID string, route *proxyRunRoute) {
	if s == nil || s.proxyRuntime == nil {
		return
	}
	s.proxyRuntime.Unregister(runID, route)
}

func (s *Server) lookupProxyRun(runID string) (*proxyRunRoute, bool) {
	if s == nil || s.proxyRuntime == nil {
		return nil, false
	}
	return s.proxyRuntime.Lookup(runID)
}

func (s *Server) handleProxyWebSocketQuery(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	registered, statusErr := s.registerQueryRun(r.Context(), prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		writeStatusError(w, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
	if !ok {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}

	sseWriter, err := newSSEWriter(w, sseWriterOptions{
		SSE:            s.deps.Config.SSE,
		Render:         stream.DefaultRenderConfig(),
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonStreamWriterFailed, err.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, attachErr := s.deps.Runs.AttachObserver(prepared.Req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, attachErr.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.Req.RunID, observer.ID)
	defer observer.MarkDone()

	s.broadcast("run.started", runStartedPushPayload(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey, registered.StartedAtMillis))

	route := runtimeproxy.NewRoute(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey)
	route.Protocol = proxyProtocol(prepared.AgentDef.ProxyConfig)
	s.registerProxyRun(route)

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.Req.ChatID, prepared.Req.RunID, prepared.AgentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.SystemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.Session.CurrentMessages)
	var chatUsage chat.UsageData
	if prepared.Summary.Usage != nil {
		chatUsage = *prepared.Summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.Req, registered.StartedAtMillis, prepared.AgentDef, s.deps.Chats, stepWriter, control, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder, func(completion chat.RunCompletion) {
		notifyInternalQueryCompletion(r.Context(), &completion, "")
	})

	lastSeq := int64(0)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", localizeStreamEventData(requestLocale(r, "en"), event)); err != nil {
				if isTimeContractViolation(err) {
					s.terminateSSEForTimeContractViolation(
						sseWriter,
						lastSeq,
						event,
						api.InterruptRequest{
							RequestID: prepared.Req.RequestID,
							RunID:     prepared.Req.RunID,
							ChatID:    prepared.Req.ChatID,
							AgentKey:  prepared.Req.AgentKey,
							TeamID:    prepared.Req.TeamID,
						},
						err,
					)
				}
				return
			}
			lastSeq = event.Seq
		}
	}
}

func (s *Server) handleProxyQueryNonStream(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	registered, statusErr := s.registerQueryRun(r.Context(), prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		writeStatusError(w, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
	if !ok {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.Req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, attachErr.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.Req.RunID, observer.ID)
	defer observer.MarkDone()

	s.broadcast("run.started", runStartedPushPayload(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey, registered.StartedAtMillis))

	upstreamTransport := proxyUpstreamTransport(prepared.AgentDef.ProxyConfig)
	var route *proxyRunRoute
	if upstreamTransport == "ws" {
		route = runtimeproxy.NewRoute(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey)
		route.Protocol = proxyProtocol(prepared.AgentDef.ProxyConfig)
		s.registerProxyRun(route)
	}

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.Req.ChatID, prepared.Req.RunID, prepared.AgentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.SystemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.Session.CurrentMessages)
	var proxyControl *contracts.RunControl
	if upstreamTransport == "ws" {
		proxyControl = control
	}
	var chatUsage chat.UsageData
	if prepared.Summary.Usage != nil {
		chatUsage = *prepared.Summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.Req, registered.StartedAtMillis, prepared.AgentDef, s.deps.Chats, stepWriter, proxyControl, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder, func(completion chat.RunCompletion) {
		notifyInternalQueryCompletion(r.Context(), &completion, "")
	})

	collector := newQueryEventCollector(prepared.Req.IncludeFullText)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				result := collector.Result()
				if capture := internalQueryCaptureFromContext(r.Context()); capture != nil {
					capture.responseResult = &result
				}
				if prepared.Req.IncludeFullText {
					result.FullText = collector.FullText(result.AssistantText)
				}
				if queryRunFailed(result) {
					if result.ErrorPayload["code"] == "time_contract_violation" {
						writeTimeContractViolationData(w, result.ErrorPayload)
						return
					}
					writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, queryRunErrorMessage(result)))
					return
				}
				writeJSON(w, http.StatusOK, api.Success(queryResponseFromResult(prepared.Req, result)))
				return
			}
			collector.Consume(event)
		}
	}
}

func (s *Server) runProxyWebSocket(
	runCtx context.Context,
	prepared preparedQuery,
	route *proxyRunRoute,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	onCompletion func(chat.RunCompletion),
) {
	s.runProxyWebSocketWithStartup(runCtx, prepared, route, eventBus, recorder, nil, onCompletion)
}

func (s *Server) runProxyWebSocketWithStartup(runCtx context.Context, prepared preparedQuery, route *proxyRunRoute, eventBus *stream.RunEventBus, recorder *proxyEventRecorder, startup chan<- error, onCompletion func(chat.RunCompletion)) {
	s.proxyExecutor().Execute(runCtx, runtimePreparedQuery(prepared), route, eventBus, recorder, startup, onCompletion)
}
