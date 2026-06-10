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
	"agent-platform/internal/stream"
	platformws "agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type proxyRunRoute struct {
	runID    string
	chatID   string
	agentKey string
	send     chan map[string]any
	done     chan struct{}
}

func (s *Server) registerProxyRun(route *proxyRunRoute) {
	if s == nil || route == nil || strings.TrimSpace(route.runID) == "" {
		return
	}
	s.proxyMu.Lock()
	if s.proxyRuns == nil {
		s.proxyRuns = map[string]*proxyRunRoute{}
	}
	s.proxyRuns[route.runID] = route
	s.proxyMu.Unlock()
}

func (s *Server) unregisterProxyRun(runID string, route *proxyRunRoute) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return
	}
	s.proxyMu.Lock()
	if current := s.proxyRuns[runID]; current == route {
		delete(s.proxyRuns, runID)
	}
	s.proxyMu.Unlock()
}

func (s *Server) lookupProxyRun(runID string) (*proxyRunRoute, bool) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return nil, false
	}
	s.proxyMu.RLock()
	route, ok := s.proxyRuns[runID]
	s.proxyMu.RUnlock()
	return route, ok
}

func (s *Server) wsProxyQuery(
	ctx context.Context,
	conn *platformws.Conn,
	req platformws.RequestFrame,
	prepared preparedQuery,
) {
	runCtx, control, _ := s.deps.Runs.Register(ctx, prepared.session)
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		conn.ReleaseStream(req.ID)
		conn.SendError(req.ID, "internal_error", 500, "run event bus unavailable", nil)
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		conn.ReleaseStream(req.ID)
		s.sendWSAttachError(conn, req.ID, prepared.req.RunID, prepared.req.ChatID, attachErr)
		return
	}
	conn.AttachObserver(req.ID, observer.ID, func() {
		s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	})
	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})

	upstreamTransport := proxyUpstreamTransport(prepared.agentDef.ProxyConfig)
	var route *proxyRunRoute
	if upstreamTransport == "ws" {
		route = &proxyRunRoute{
			runID:    prepared.req.RunID,
			chatID:   prepared.req.ChatID,
			agentKey: prepared.req.AgentKey,
			send:     make(chan map[string]any, 16),
			done:     make(chan struct{}),
		}
		s.registerProxyRun(route)
	}

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	var proxyControl *contracts.RunControl
	if upstreamTransport == "ws" {
		proxyControl = control
	}
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, prepared.agentDef, s.deps.Chats, stepWriter, proxyControl, chatUsage, s.deps.Models, s.deps.Config.Billing)

	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) handleProxyWebSocketQuery(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	runCtx, control, _ := s.deps.Runs.Register(r.Context(), prepared.session)
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}

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

	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		releaseQuery(prepared.release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.req, contracts.InterruptReasonObserverAttachFailed, attachErr.Error()))
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, attachErr.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	defer observer.MarkDone()

	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})

	route := &proxyRunRoute{
		runID:    prepared.req.RunID,
		chatID:   prepared.req.ChatID,
		agentKey: prepared.req.AgentKey,
		send:     make(chan map[string]any, 16),
		done:     make(chan struct{}),
	}
	s.registerProxyRun(route)

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	recorder := newProxyEventRecorder(prepared.req, prepared.agentDef, s.deps.Chats, stepWriter, control, chatUsage, s.deps.Models, s.deps.Config.Billing)
	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)

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

func (s *Server) runProxyWebSocket(
	runCtx context.Context,
	prepared preparedQuery,
	route *proxyRunRoute,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
) {
	defer func() {
		if route != nil {
			s.unregisterProxyRun(prepared.req.RunID, route)
			close(route.done)
		}
		var (
			persisted  bool
			completion chat.RunCompletion
		)
		if recorder != nil {
			persisted, completion = recorder.Finish()
		}
		if eventBus != nil {
			eventBus.FreezeAndWait()
		}
		releaseQuery(prepared.release)
		s.deps.Runs.Finish(prepared.req.RunID)
		s.broadcast("run.finished", map[string]any{"runId": prepared.req.RunID, "chatId": prepared.req.ChatID})
		if persisted {
			s.broadcastRunCompletionNotifications(completion)
		}
	}()

	if proxyUpstreamTransport(prepared.agentDef.ProxyConfig) == "sse" {
		s.runProxySSE(runCtx, prepared, eventBus, recorder)
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
			case <-route.done:
				writeDone <- nil
				return
			case msg := <-route.send:
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
		event, ok := decodeProxyEvent(data)
		if !ok {
			continue
		}
		event = normalizeProxyEventIdentity(event, prepared.req)
		if event.Seq <= 0 {
			seq++
			event.Seq = seq
		}
		if event.Timestamp <= 0 {
			event.Timestamp = time.Now().UnixMilli()
		}
		if recorder != nil {
			recorder.DecorateEvent(&event)
		}
		eventBus.Publish(event)
		if recorder != nil {
			recorder.OnEvent(event)
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
}

func (s *Server) runProxySSE(
	runCtx context.Context,
	prepared preparedQuery,
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
) {
	proxy := prepared.agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("PROXY agent missing proxyConfig.baseUrl"))
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"
	proxyReferences, err := prepareProxyReferences(s.deps.Chats, s.ticketService, proxyReferenceOptions{
		ChatID:          prepared.req.ChatID,
		RunID:           prepared.req.RunID,
		Subject:         prepared.session.Subject,
		ResourceBaseURL: prepared.resourceBaseURL,
		References:      prepared.req.References,
	})
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}
	body, err := json.Marshal(map[string]any{
		"requestId":  prepared.req.RequestID,
		"runId":      prepared.req.RunID,
		"chatId":     prepared.req.ChatID,
		"agentKey":   proxyAgentKey(proxy, prepared.req.AgentKey),
		"role":       prepared.req.Role,
		"message":    prepared.req.Message,
		"references": proxyReferences,
		"params":     proxyForwardParams(prepared.req, prepared.session.WorkspaceRoot),
		"model":      prepared.req.Model,
		"scene":      prepared.req.Scene,
		"stream":     true,
	})
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	timeout := time.Duration(proxy.Timeout) * time.Second
	client := &http.Client{Timeout: timeout}
	proxyReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("failed to create proxy sse request: %w", err))
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
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy sse request failed: %w", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy sse upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data))))
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
		event, ok := decodeProxyEvent([]byte(payload))
		if !ok {
			continue
		}
		event = normalizeProxyEventIdentity(event, prepared.req)
		if event.Seq <= 0 {
			seq++
			event.Seq = seq
		}
		if event.Timestamp <= 0 {
			event.Timestamp = time.Now().UnixMilli()
		}
		if recorder != nil {
			recorder.DecorateEvent(&event)
		}
		if eventBus != nil {
			eventBus.Publish(event)
		}
		if recorder != nil {
			recorder.OnEvent(event)
		}
		switch event.Type {
		case "run.complete", "run.error", "run.cancel":
			terminalSeen = true
			return
		}
	}
	if err := scanner.Err(); err != nil && !terminalSeen {
		s.publishProxyError(eventBus, recorder, prepared.req, fmt.Errorf("proxy sse read failed: %w", err))
	}
}
