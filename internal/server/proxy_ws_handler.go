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
	"net/url"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
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
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		conn.ReleaseStream(req.ID)
		conn.SendError(req.ID, "internal_error", 500, "run event bus unavailable", nil)
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
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

	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req), chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	var proxyControl *contracts.RunControl
	if upstreamTransport == "ws" {
		proxyControl = control
	}
	recorder := newProxyEventRecorder(prepared.req, prepared.agentDef, s.deps.Chats, stepWriter, proxyControl)

	go s.runProxyWebSocket(runCtx, prepared, route, eventBus, recorder)
	conn.StartStreamForward(req.ID, observer)
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
		if recorder != nil {
			recorder.Finish()
		}
		if eventBus != nil {
			eventBus.Freeze()
		}
		s.deps.Runs.Finish(prepared.req.RunID)
		s.broadcast("run.finished", map[string]any{"runId": prepared.req.RunID, "chatId": prepared.req.ChatID})
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
	if err := upstream.WriteJSON(proxyQueryPayload(prepared.req, prepared.agentDef.ProxyConfig, proxyReferences)); err != nil {
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
		"params":     prepared.req.Params,
		"scene":      prepared.req.Scene,
		"stream":     true,
	})
	if err != nil {
		s.publishProxyError(eventBus, recorder, prepared.req, err)
		return
	}

	timeout := time.Duration(proxy.TimeoutMs) * time.Millisecond
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

func proxyWebSocketTarget(proxy *catalog.ProxyConfig) (string, http.Header, error) {
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		return "", nil, fmt.Errorf("PROXY agent missing proxyConfig.baseUrl")
	}
	parsed, err := url.Parse(strings.TrimRight(proxy.BaseURL, "/"))
	if err != nil {
		return "", nil, err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", nil, fmt.Errorf("unsupported proxy websocket scheme: %s", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
	query := parsed.Query()
	if proxy.Token != "" {
		query.Set("token", proxy.Token)
	}
	parsed.RawQuery = query.Encode()

	header := http.Header{}
	if proxy.Token != "" {
		header.Set("Authorization", "Bearer "+proxy.Token)
	}
	return parsed.String(), header, nil
}

func proxyUpstreamTransport(proxy *catalog.ProxyConfig) string {
	if proxy == nil {
		return "sse"
	}
	switch strings.ToLower(strings.TrimSpace(proxy.Transport)) {
	case "ws", "websocket":
		return "ws"
	default:
		return "sse"
	}
}

func proxyQueryPayload(req api.QueryRequest, proxy *catalog.ProxyConfig, references []api.Reference) map[string]any {
	payload := map[string]any{
		"requestId":  req.RequestID,
		"runId":      req.RunID,
		"chatId":     req.ChatID,
		"agentKey":   proxyAgentKey(proxy, req.AgentKey),
		"role":       req.Role,
		"message":    req.Message,
		"references": references,
		"params":     req.Params,
		"scene":      req.Scene,
		"stream":     true,
	}
	return map[string]any{
		"frame":   "request",
		"type":    "request.query",
		"id":      req.RequestID,
		"payload": payload,
	}
}

func proxyAgentKey(proxy *catalog.ProxyConfig, fallback string) string {
	if proxy != nil {
		if key := strings.TrimSpace(proxy.AgentKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(fallback)
}

func decodeProxyEvent(data []byte) (stream.EventData, bool) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return stream.EventData{}, false
	}
	if raw["event"] != nil {
		if nested, ok := raw["event"].(map[string]any); ok {
			raw = nested
		}
	}
	event := stream.EventDataFromMap(raw)
	return event, strings.TrimSpace(event.Type) != ""
}

func normalizeProxyEventIdentity(event stream.EventData, req api.QueryRequest) stream.EventData {
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	if strings.TrimSpace(req.RequestID) != "" {
		event.Payload["requestId"] = req.RequestID
	}
	if strings.TrimSpace(req.ChatID) != "" {
		event.Payload["chatId"] = req.ChatID
	}
	if strings.TrimSpace(req.RunID) != "" {
		event.Payload["runId"] = req.RunID
	}
	if strings.TrimSpace(req.AgentKey) != "" {
		event.Payload["agentKey"] = req.AgentKey
	}
	return event
}

func (s *Server) publishProxyError(
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	req api.QueryRequest,
	err error,
) {
	event := stream.EventData{
		Seq:       1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"runId":   req.RunID,
			"chatId":  req.ChatID,
			"message": err.Error(),
			"error":   err.Error(),
		},
	}
	log.Printf("[proxy][ws] %s", err)
	if eventBus != nil {
		eventBus.Publish(event)
	}
	if recorder != nil {
		recorder.OnEvent(event)
	}
}

func (s *Server) forwardProxySubmit(req api.SubmitRequest) (api.SubmitResponse, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.SubmitResponse{}, false
	}
	payload := map[string]any{
		"runId":      req.RunID,
		"chatId":     route.chatID,
		"agentKey":   route.agentKey,
		"awaitingId": req.AwaitingID,
		"params":     req.Params,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    "request.submit",
		"id":      req.AwaitingID,
		"payload": payload,
	}) {
		return api.SubmitResponse{
			Accepted:   false,
			Status:     "unmatched",
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			Detail:     "Proxy run is no longer active",
		}, true
	}
	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		Detail:     "Proxy submit forwarded",
	}, true
}

func (s *Server) forwardProxyInterrupt(req api.InterruptRequest) (api.InterruptResponse, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.InterruptResponse{}, false
	}
	payload := map[string]any{
		"requestId": req.RequestID,
		"runId":     req.RunID,
		"chatId":    route.chatID,
		"agentKey":  route.agentKey,
		"message":   req.Message,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    "request.interrupt",
		"id":      req.RequestID,
		"payload": payload,
	}) {
		return api.InterruptResponse{
			Accepted: false,
			Status:   "unmatched",
			RunID:    req.RunID,
			Detail:   "Proxy run is no longer active",
		}, true
	}
	return api.InterruptResponse{
		Accepted: true,
		Status:   "accepted",
		RunID:    req.RunID,
		Detail:   "Proxy interrupt forwarded",
	}, true
}

func sendProxyRouteMessage(route *proxyRunRoute, payload map[string]any) bool {
	if route == nil {
		return false
	}
	select {
	case route.send <- payload:
		return true
	case <-route.done:
		return false
	case <-time.After(2 * time.Second):
		return false
	}
}

type proxyEventRecorder struct {
	req           api.QueryRequest
	agentDef      catalog.AgentDefinition
	chatStore     chat.Store
	stepWriter    *chat.StepWriter
	control       *contracts.RunControl
	assistantText strings.Builder
	startedAt     int64
	finishReason  string
	runUsage      chat.UsageData
	contents      map[string]*proxyContentBucket
	reasonings    map[string]*proxyContentBucket
	tools         map[string]*proxyToolBucket
}

type proxyContentBucket struct {
	runID string
	text  strings.Builder
}

type proxyToolBucket struct {
	runID    string
	toolName string
	args     strings.Builder
}

func newProxyEventRecorder(
	req api.QueryRequest,
	agentDef catalog.AgentDefinition,
	chatStore chat.Store,
	stepWriter *chat.StepWriter,
	control *contracts.RunControl,
) *proxyEventRecorder {
	if stepWriter == nil {
		return nil
	}
	stepWriter.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"requestId": req.RequestID,
			"runId":     req.RunID,
			"chatId":    req.ChatID,
			"agentKey":  req.AgentKey,
			"role":      req.Role,
			"message":   req.Message,
		},
	})
	return &proxyEventRecorder{
		req:        req,
		agentDef:   agentDef,
		chatStore:  chatStore,
		stepWriter: stepWriter,
		control:    control,
		startedAt:  time.Now().UnixMilli(),
		contents:   map[string]*proxyContentBucket{},
		reasonings: map[string]*proxyContentBucket{},
		tools:      map[string]*proxyToolBucket{},
	}
}

func (r *proxyEventRecorder) OnEvent(event stream.EventData) {
	if r == nil || r.stepWriter == nil {
		return
	}
	switch event.Type {
	case "content.start":
		id, _ := event.Payload["contentId"].(string)
		runID, _ := event.Payload["runId"].(string)
		if id != "" {
			r.contents[id] = &proxyContentBucket{runID: runID}
		}
	case "content.delta":
		id, _ := event.Payload["contentId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if delta == "" {
			return
		}
		r.assistantText.WriteString(delta)
		if b := r.contents[id]; b != nil {
			b.text.WriteString(delta)
		}
	case "content.end":
		id, _ := event.Payload["contentId"].(string)
		b := r.contents[id]
		delete(r.contents, id)
		text, _ := event.Payload["text"].(string)
		if b == nil {
			b = &proxyContentBucket{}
		}
		if text == "" {
			text = b.text.String()
		}
		if text != "" {
			r.stepWriter.OnEvent(stream.EventData{
				Type:      "content.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"contentId": id,
					"runId":     b.runID,
					"text":      text,
				},
			})
		}
	case "reasoning.start":
		id, _ := event.Payload["reasoningId"].(string)
		runID, _ := event.Payload["runId"].(string)
		if id != "" {
			r.reasonings[id] = &proxyContentBucket{runID: runID}
		}
	case "reasoning.delta":
		id, _ := event.Payload["reasoningId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if b := r.reasonings[id]; b != nil && delta != "" {
			b.text.WriteString(delta)
		}
	case "reasoning.end":
		id, _ := event.Payload["reasoningId"].(string)
		b := r.reasonings[id]
		delete(r.reasonings, id)
		text, _ := event.Payload["text"].(string)
		if b == nil {
			b = &proxyContentBucket{}
		}
		if text == "" {
			text = b.text.String()
		}
		if text != "" {
			r.stepWriter.OnEvent(stream.EventData{
				Type:      "reasoning.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"reasoningId": id,
					"runId":       b.runID,
					"text":        text,
				},
			})
		}
	case "tool.start":
		id, _ := event.Payload["toolId"].(string)
		runID, _ := event.Payload["runId"].(string)
		toolName, _ := event.Payload["toolName"].(string)
		if id != "" {
			r.tools[id] = &proxyToolBucket{runID: runID, toolName: toolName}
		}
	case "tool.args":
		id, _ := event.Payload["toolId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if b := r.tools[id]; b != nil && delta != "" {
			b.args.WriteString(delta)
		}
	case "tool.end":
		id, _ := event.Payload["toolId"].(string)
		b := r.tools[id]
		delete(r.tools, id)
		if b == nil {
			b = &proxyToolBucket{}
		}
		r.stepWriter.OnEvent(stream.EventData{
			Type:      "tool.snapshot",
			Timestamp: event.Timestamp,
			Payload: map[string]any{
				"toolId":    id,
				"runId":     b.runID,
				"toolName":  b.toolName,
				"arguments": b.args.String(),
			},
		})
	case "awaiting.ask":
		if r.control != nil {
			r.control.ExpectSubmit(awaitingContextFromProxyEvent(event))
		}
		r.stepWriter.OnEvent(event)
	case "run.complete":
		r.finishReason = "complete"
		applyTerminalEventUsage(&r.runUsage, event)
		r.stepWriter.OnEvent(event)
	case "run.cancel":
		r.finishReason = "cancel"
		applyTerminalEventUsage(&r.runUsage, event)
		r.stepWriter.OnEvent(event)
	case "run.error":
		r.finishReason = "error"
		applyTerminalEventUsage(&r.runUsage, event)
		r.stepWriter.OnEvent(event)
	case "tool.result",
		"task.start", "task.complete", "task.cancel", "task.fail",
		"plan.create", "plan.update", "artifact.publish",
		"request.submit", "awaiting.answer", "request.steer":
		r.stepWriter.OnEvent(event)
	}
}

func (r *proxyEventRecorder) Finish() {
	if r == nil || r.stepWriter == nil {
		return
	}
	r.stepWriter.Flush()
	if r.req.ChatID == "" || r.req.RunID == "" {
		return
	}
	if r.chatStore == nil {
		return
	}
	finishReason := r.finishReason
	if strings.TrimSpace(finishReason) == "" {
		finishReason = "complete"
	}
	if err := r.chatStore.OnRunCompleted(chat.RunCompletion{
		ChatID:          r.req.ChatID,
		RunID:           r.req.RunID,
		AgentKey:        r.req.AgentKey,
		AssistantText:   r.assistantText.String(),
		InitialMessage:  r.req.Message,
		FinishReason:    finishReason,
		StartedAtMillis: r.startedAt,
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage:           r.runUsage,
	}); err != nil {
		log.Printf("[proxy][ws] OnRunCompleted failed: %v", err)
	}
}

func awaitingContextFromProxyEvent(event stream.EventData) contracts.AwaitingSubmitContext {
	mode, _ := event.Payload["mode"].(string)
	awaitingID, _ := event.Payload["awaitingId"].(string)
	return contracts.AwaitingSubmitContext{
		AwaitingID: awaitingID,
		Mode:       mode,
		ItemCount:  proxyAwaitItemCount(mode, event.Payload["questions"], event.Payload["approvals"], event.Payload["forms"]),
	}
}

func proxyAwaitItemCount(mode string, questions any, approvals any, forms any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return lenAnySlice(questions)
	case "approval":
		return lenAnySlice(approvals)
	case "form":
		return lenAnySlice(forms)
	default:
		return 0
	}
}

func lenAnySlice(value any) int {
	if items, ok := value.([]any); ok {
		return len(items)
	}
	return 0
}
