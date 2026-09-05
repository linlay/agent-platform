package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	"agent-platform/internal/stream"
	platformws "agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type proxyRunRoute = runtimeproxy.Route

func newDetachedProxyRunRoute(prepared preparedQuery) *proxyRunRoute {
	proxy := prepared.agentDef.ProxyConfig
	route := runtimeproxy.NewRoute(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey)
	route.Protocol = proxyProtocol(proxy)
	if proxy != nil {
		route.UpstreamAgentKey = proxyAgentKey(proxy, prepared.req.AgentKey)
		route.Transport = proxyUpstreamTransport(proxy)
		route.BaseURL = strings.TrimRight(strings.TrimSpace(proxy.BaseURL), "/")
		route.Token = proxy.Token
		route.Timeout = proxyRequestTimeout(proxy)
	}
	return route
}

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

func (s *Server) wsProxyQuery(
	ctx context.Context,
	conn *platformws.Conn,
	req platformws.RequestFrame,
	prepared preparedQuery,
) {
	registered, statusErr := s.registerQueryRun(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		conn.ReleaseStream(req.ID)
		s.sendWSStatusError(conn, req.ID, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		conn.ReleaseStream(req.ID)
		conn.SendError(req.ID, "internal_error", 500, "run event bus unavailable", nil)
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		conn.ReleaseStream(req.ID)
		s.sendWSAttachError(conn, req.ID, prepared.req.RunID, prepared.req.ChatID, attachErr)
		return
	}
	conn.AttachObserver(req.ID, observer.ID, func() {
		s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	})
	s.broadcast("run.started", runStartedPushPayload(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey, registered.StartedAtMillis))

	upstreamTransport := proxyUpstreamTransport(prepared.agentDef.ProxyConfig)
	var route *proxyRunRoute
	if upstreamTransport == "ws" {
		route = runtimeproxy.NewRoute(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey)
		route.Protocol = proxyProtocol(prepared.agentDef.ProxyConfig)
		s.registerProxyRun(route)
	}

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.systemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	var proxyControl *contracts.RunControl
	if upstreamTransport == "ws" {
		proxyControl = control
	}
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, registered.StartedAtMillis, prepared.agentDef, s.deps.Chats, stepWriter, proxyControl, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)

	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) handleProxyWebSocketQuery(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	registered, statusErr := s.registerQueryRun(r.Context(), prepared)
	if statusErr != nil {
		releaseQuery(prepared.release)
		writeStatusError(w, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
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
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonStreamWriterFailed, err.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, attachErr.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	s.broadcast("run.started", runStartedPushPayload(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey, registered.StartedAtMillis))

	route := runtimeproxy.NewRoute(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey)
	route.Protocol = proxyProtocol(prepared.agentDef.ProxyConfig)
	s.registerProxyRun(route)

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.systemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, registered.StartedAtMillis, prepared.agentDef, s.deps.Chats, stepWriter, control, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)

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
			if err := sseWriter.WriteJSON("message", event); err != nil {
				if isTimeContractViolation(err) {
					s.terminateSSEForTimeContractViolation(
						sseWriter,
						lastSeq,
						event,
						api.InterruptRequest{
							RequestID: prepared.req.RequestID,
							RunID:     prepared.req.RunID,
							ChatID:    prepared.req.ChatID,
							AgentKey:  prepared.req.AgentKey,
							TeamID:    prepared.req.TeamID,
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
		releaseQuery(prepared.release)
		writeStatusError(w, statusErr)
		return
	}
	runCtx, control := registered.RunCtx, registered.Control
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		s.finishRegisteredQueryRun(prepared, registered)
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, attachErr.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	s.broadcast("run.started", runStartedPushPayload(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey, registered.StartedAtMillis))

	upstreamTransport := proxyUpstreamTransport(prepared.agentDef.ProxyConfig)
	var route *proxyRunRoute
	if upstreamTransport == "ws" {
		route = runtimeproxy.NewRoute(prepared.req.RunID, prepared.req.ChatID, prepared.req.AgentKey)
		route.Protocol = proxyProtocol(prepared.agentDef.ProxyConfig)
		s.registerProxyRun(route)
	}

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.systemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
	var proxyControl *contracts.RunControl
	if upstreamTransport == "ws" {
		proxyControl = control
	}
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, registered.StartedAtMillis, prepared.agentDef, s.deps.Chats, stepWriter, proxyControl, s.deps.Notifications, chatUsage, s.deps.Models, s.deps.Config.Billing)
	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)

	collector := newQueryEventCollector(prepared.req.IncludeFullText)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				result := collector.Result()
				if prepared.req.IncludeFullText {
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
				writeJSON(w, http.StatusOK, api.Success(queryResponseFromResult(prepared.req, result)))
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
) {
	s.runProxyWebSocketWithStartup(runCtx, prepared, route, eventBus, recorder, nil)
}

func (s *Server) runProxyWebSocketWithStartup(
	runCtx context.Context,
	prepared preparedQuery,
	route *proxyRunRoute,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	startup chan<- error,
) {
	defer func() {
		if route != nil {
			s.unregisterProxyRun(prepared.req.RunID, route)
			close(route.Done)
		}
		// Capture one platform completion clock before persistence/observer
		// cleanup. A persistence error must not cause run.finished to invent a
		// second, later timestamp.
		completedAtMillis := time.Now().UnixMilli()
		finishReason := "error"
		var (
			persisted  bool
			completion chat.RunCompletion
		)
		if recorder != nil {
			persisted, completion = recorder.Finish()
			if completion.UpdatedAtMillis != 0 {
				completedAtMillis = completion.UpdatedAtMillis
			}
			if strings.TrimSpace(completion.FinishReason) != "" {
				finishReason = completion.FinishReason
			}
		}
		if eventBus != nil {
			eventBus.FreezeAndWait()
		}
		releaseQuery(prepared.release)
		s.deps.Runs.Finish(prepared.req.RunID)
		s.broadcast("run.finished", runFinishedPushPayload(prepared.req.RunID, prepared.req.ChatID, finishReason, completedAtMillis))
		if persisted {
			s.broadcastRunCompletionNotifications(completion)
		}
		if completion.RunID != "" {
			notifyInternalQueryCompletion(runCtx, &completion, "")
		}
	}()

	if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "sse" {
		s.runProxySSE(runCtx, prepared, eventBus, recorder, startup)
		return
	}
	if startup != nil {
		startup <- nil
	}

	if strings.TrimSpace(prepared.agentDef.ProxyConfig.ChannelID) != "" {
		s.runProxyInboundChannel(runCtx, prepared, route, eventBus, recorder)
		return
	}

	upstreamURL, header, err := proxyWebSocketTarget(prepared.agentDef.ProxyConfig)
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	upstream, _, err := gws.DefaultDialer.DialContext(runCtx, upstreamURL, header)
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy websocket dial failed: %w", err))
		return
	}
	defer upstream.Close()

	writeDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				writeDone <- runCtx.Err()
				return
			case <-route.Done:
				writeDone <- nil
				return
			case msg := <-route.SendQueue:
				if err := upstream.WriteJSON(msg); err != nil {
					writeDone <- err
					return
				}
			}
		}
	}()

	proxyReferences, err := prepareProxyReferences(s.deps.Chats, s.ticketService, proxyReferenceOptions{
		ChatID:          prepared.req.ChatID,
		RunID:           prepared.req.RunID,
		Subject:         prepared.session.Subject,
		ResourceBaseURL: prepared.resourceBaseURL,
		WorkspaceRoot:   prepared.session.WorkspaceRoot,
		References:      prepared.req.References,
	})
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	if err := upstream.WriteJSON(proxyQueryPayloadWithWorkspace(prepared.req, prepared.agentDef.ProxyConfig, proxyReferences, prepared.session.WorkspaceRoot)); err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy websocket write failed: %w", err))
		return
	}

	var seq int64
	terminalSeen := false
	for {
		select {
		case err := <-writeDone:
			if err != nil && !terminalSeen {
				s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy websocket write loop failed: %w", err))
			}
			return
		default:
		}

		_, data, err := upstream.ReadMessage()
		if err != nil {
			if !terminalSeen {
				s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy websocket read failed: %w", err))
			}
			return
		}
		frame, ok, decodeErr := decodeProxyFrameAt(data, "proxy.websocket.event")
		if decodeErr != nil {
			terminalSeen = true
			s.publishProxyError(eventBus, recorder, prepared.req, decodeErr)
			return
		}
		if !ok || !proxyFrameMatchesRequest(frame, prepared.req.RequestID) {
			continue
		}
		if err := proxyFrameError(frame); err != nil {
			terminalSeen = true
			s.publishProxyError(eventBus, recorder, prepared.req, err)
			return
		}
		if !frame.HasEvent {
			if strings.EqualFold(frame.Frame, "stream") && frame.Reason != "" {
				terminalSeen = true
				return
			}
			continue
		}
		event, publishErr := publishProxyLiveEvent(eventBus, recorder, prepared.req, &seq, frame.Event)
		if publishErr != nil {
			terminalSeen = true
			s.publishProxyError(eventBus, recorder, prepared.req, publishErr)
			return
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
}

func (s *Server) runProxyInboundChannel(
	runCtx context.Context,
	prepared preparedQuery,
	route *proxyRunRoute,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
) {
	proxy := prepared.agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.ChannelID) == "" {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("CHANNEL agent missing channelConfig.channelId"))
		return
	}
	hub, ok := s.deps.Notifications.(ChannelConnectionProvider)
	if !ok || hub == nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("channel %s connection provider is not configured", proxy.ChannelID))
		return
	}
	upstream, ok := hub.GatewayConnection(proxy.ChannelID)
	if !ok || upstream == nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("channel %s is not connected", proxy.ChannelID))
		return
	}

	proxyReferences, err := prepareProxyReferences(s.deps.Chats, s.ticketService, proxyReferenceOptions{
		ChatID:          prepared.req.ChatID,
		RunID:           prepared.req.RunID,
		Subject:         prepared.session.Subject,
		ResourceBaseURL: prepared.resourceBaseURL,
		WorkspaceRoot:   prepared.session.WorkspaceRoot,
		References:      prepared.req.References,
	})
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	initial := proxyQueryPayloadWithWorkspace(prepared.req, proxy, proxyReferences, prepared.session.WorkspaceRoot)
	raw, err := json.Marshal(initial)
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	var reqFrame platformws.RequestFrame
	if err := json.Unmarshal(raw, &reqFrame); err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	frames, cleanup, err := upstream.OpenOutboundRequest(reqFrame)
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("channel %s request failed: %w", proxy.ChannelID, err))
		return
	}
	defer cleanup()

	writeDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				writeDone <- runCtx.Err()
				return
			case <-route.Done:
				writeDone <- nil
				return
			case msg := <-route.SendQueue:
				if !upstream.SendFrame(msg) {
					writeDone <- fmt.Errorf("channel %s write failed", proxy.ChannelID)
					return
				}
			}
		}
	}()

	var seq int64
	terminalSeen := false
	for {
		select {
		case err := <-writeDone:
			if err != nil && !terminalSeen {
				s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("channel websocket write loop failed: %w", err))
			}
			return
		case <-runCtx.Done():
			if !terminalSeen {
				s.publishProxyError(eventBus, recorder, prepared.req, runCtx.Err())
			}
			return
		case data, ok := <-frames:
			if !ok {
				if !terminalSeen {
					s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("channel %s disconnected", proxy.ChannelID))
				}
				return
			}
			frame, ok, decodeErr := decodeProxyFrameAt(data, "proxy.channel.event")
			if decodeErr != nil {
				terminalSeen = true
				s.publishProxyError(eventBus, recorder, prepared.req, decodeErr)
				return
			}
			if !ok || !proxyFrameMatchesRequest(frame, prepared.req.RequestID) {
				continue
			}
			if err := proxyFrameError(frame); err != nil {
				terminalSeen = true
				s.publishProxyError(eventBus, recorder, prepared.req, err)
				return
			}
			if !frame.HasEvent {
				if strings.EqualFold(frame.Frame, "stream") && frame.Reason != "" {
					terminalSeen = true
					return
				}
				continue
			}
			event, publishErr := publishProxyLiveEvent(eventBus, recorder, prepared.req, &seq, frame.Event)
			if publishErr != nil {
				terminalSeen = true
				s.publishProxyError(eventBus, recorder, prepared.req, publishErr)
				return
			}
			switch event.Type {
			case "run.complete", "run.error", "run.cancel":
				terminalSeen = true
				return
			}
		}
	}
}

func (s *Server) runProxySSE(
	runCtx context.Context,
	prepared preparedQuery,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	startup chan<- error,
) {
	startupResolved := false
	startupObserverReady := false
	resolveStartup := func(err error) {
		if startup == nil || startupResolved {
			return
		}
		startupResolved = true
		startup <- err
	}
	defer resolveStartup(nil)
	proxy := prepared.agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		err := fmt.Errorf("PROXY agent missing proxyConfig.baseUrl")
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"
	proxyReferences, err := prepareProxyReferences(s.deps.Chats, s.ticketService, proxyReferenceOptions{
		ChatID:          prepared.req.ChatID,
		RunID:           prepared.req.RunID,
		Subject:         prepared.session.Subject,
		ResourceBaseURL: prepared.resourceBaseURL,
		WorkspaceRoot:   prepared.session.WorkspaceRoot,
		References:      prepared.req.References,
	})
	if err != nil {
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	bodyPayload := map[string]any{
		"requestId":   prepared.req.RequestID,
		"runId":       prepared.req.RunID,
		"chatId":      prepared.req.ChatID,
		"agentKey":    proxyAgentKey(proxy, prepared.req.AgentKey),
		"role":        prepared.req.Role,
		"message":     prepared.req.Message,
		"accessLevel": prepared.req.AccessLevel,
		"references":  proxyReferences,
		"params":      proxyForwardParams(prepared.req, prepared.session.WorkspaceRoot),
		"model":       prepared.req.Model,
		"scene":       prepared.req.Scene,
		"stream":      true,
	}
	if prepared.req.Hidden != nil {
		bodyPayload["hidden"] = *prepared.req.Hidden
	}
	if prepared.req.PlanningMode != nil {
		bodyPayload["planningMode"] = *prepared.req.PlanningMode
	}
	if len(prepared.req.MustUseSkills) > 0 {
		bodyPayload["mustUseSkills"] = append([]string(nil), prepared.req.MustUseSkills...)
	}
	body, err := json.Marshal(bodyPayload)
	if err != nil {
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	client := &http.Client{Timeout: proxyRequestTimeout(proxy)}
	proxyReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		err = fmt.Errorf("failed to create proxy sse request: %w", err)
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	if proxy.Token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+proxy.Token)
	}

	log.Printf("[proxy][ws] bridging websocket client to upstream sse %s (agent=%s, chatId=%s)", targetURL, prepared.agentDef.Key, prepared.req.ChatID)
	resp, err := client.Do(proxyReq)
	if err != nil {
		err = fmt.Errorf("proxy sse request failed: %w", err)
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("proxy sse upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		resolveStartup(err)
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	var seq int64
	terminalSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == stream.DoneSentinel {
			continue
		}
		event, ok, decodeErr := decodeProxyEventAt([]byte(payload), "proxy.sse.event")
		if decodeErr != nil {
			terminalSeen = true
			resolveStartup(decodeErr)
			s.publishProxyErrorAfter(eventBus, recorder, prepared.req, decodeErr, seq)
			return
		}
		if !ok {
			continue
		}
		resolveStartup(nil)
		if startup != nil && !startupObserverReady {
			startupObserverReady = true
			waitForProxyStartupObserver(runCtx, eventBus)
		}
		event, err = publishProxyLiveEvent(eventBus, recorder, prepared.req, &seq, event)
		if err != nil {
			terminalSeen = true
			resolveStartup(err)
			s.publishProxyErrorAfter(eventBus, recorder, prepared.req, err, seq)
			return
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
	if err := scanner.Err(); err != nil && !terminalSeen {
		err = fmt.Errorf("proxy sse read failed: %w", err)
		resolveStartup(err)
		s.publishProxyErrorAfter(eventBus, recorder, prepared.req, err, seq)
	}
}

// StartQuery must validate the first upstream SSE event before the HTTP
// adapter commits a 200 response. Once validation succeeds, hold that event
// briefly until the adapter has attached its observer; otherwise an upstream
// sequence beginning above 1 could be mistaken for an expired replay window.
func waitForProxyStartupObserver(ctx context.Context, eventBus *stream.RunEventBus) {
	if eventBus == nil || eventBus.ObserverCount() > 0 {
		return
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for eventBus.ObserverCount() == 0 {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
		}
	}
}
