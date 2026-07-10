package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
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

func proxyWebSocketTarget(proxy *catalog.ProxyConfig) (string, http.Header, error) {
	if proxy == nil || (strings.TrimSpace(proxy.BaseURL) == "" && strings.TrimSpace(proxy.WebSocketURL) == "") {
		return "", nil, fmt.Errorf("PROXY agent missing proxyConfig.baseUrl")
	}
	rawURL := strings.TrimSpace(proxy.WebSocketURL)
	directWS := rawURL != ""
	if rawURL == "" {
		rawURL = strings.TrimRight(proxy.BaseURL, "/")
	}
	parsed, err := url.Parse(rawURL)
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
	if !directWS {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
	}
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
		return "ws"
	}
	switch strings.ToLower(strings.TrimSpace(proxy.Transport)) {
	case "sse":
		return "sse"
	case "ws", "websocket":
		return "ws"
	default:
		return "ws"
	}
}

func proxyQueryPayload(req api.QueryRequest, proxy *catalog.ProxyConfig, references []api.Reference) map[string]any {
	payload := map[string]any{
		"requestId":   req.RequestID,
		"runId":       req.RunID,
		"chatId":      req.ChatID,
		"agentKey":    proxyAgentKey(proxy, req.AgentKey),
		"role":        req.Role,
		"message":     req.Message,
		"accessLevel": req.AccessLevel,
		"references":  references,
		"params":      proxyForwardParams(req, ""),
		"model":       req.Model,
		"scene":       req.Scene,
		"stream":      true,
	}
	if req.PlanningMode != nil {
		payload["planningMode"] = *req.PlanningMode
	}
	return map[string]any{
		"frame":   "request",
		"type":    proxyRequestType(proxy, "query"),
		"id":      req.RequestID,
		"payload": payload,
	}
}

func proxyRequestType(proxy *catalog.ProxyConfig, name string) string {
	if strings.EqualFold(strings.TrimSpace(proxyProtocol(proxy)), config.ChannelProtocolPlatformWS) {
		return "/api/" + strings.TrimSpace(name)
	}
	return "request." + strings.TrimSpace(name)
}

func proxyRouteRequestType(route *proxyRunRoute, name string) string {
	if route != nil && strings.EqualFold(strings.TrimSpace(route.protocol), config.ChannelProtocolPlatformWS) {
		return "/api/" + strings.TrimSpace(name)
	}
	return "request." + strings.TrimSpace(name)
}

func proxyProtocol(proxy *catalog.ProxyConfig) string {
	if proxy == nil || strings.TrimSpace(proxy.Protocol) == "" {
		return "agw-platform"
	}
	return strings.ToLower(strings.TrimSpace(proxy.Protocol))
}

func proxyQueryPayloadWithWorkspace(req api.QueryRequest, proxy *catalog.ProxyConfig, references []api.Reference, workspaceRoot string) map[string]any {
	payload := proxyQueryPayload(req, proxy, references)
	if inner, ok := payload["payload"].(map[string]any); ok {
		inner["params"] = proxyForwardParams(req, workspaceRoot)
	}
	return payload
}

func proxyForwardParams(req api.QueryRequest, workspaceRoot string) map[string]any {
	params := contracts.CloneMap(req.Params)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return params
	}
	if params == nil {
		params = map[string]any{}
	}
	params["cwd"] = workspaceRoot
	return params
}

func proxyRequestHasReservedCWD(params map[string]any) bool {
	if params == nil {
		return false
	}
	_, ok := params["cwd"]
	return ok
}

func proxyAgentKey(proxy *catalog.ProxyConfig, fallback string) string {
	if proxy != nil {
		if key := strings.TrimSpace(proxy.AgentKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(fallback)
}

type proxyDecodedFrame struct {
	Frame    string
	Type     string
	ID       string
	Code     int
	Msg      string
	StreamID string
	Reason   string
	LastSeq  int64
	Event    stream.EventData
	HasEvent bool
}

func decodeProxyFrame(data []byte) (proxyDecodedFrame, bool) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return proxyDecodedFrame{}, false
	}
	decoded := proxyDecodedFrame{
		Frame:    strings.TrimSpace(contracts.AnyStringNode(raw["frame"])),
		Type:     strings.TrimSpace(contracts.AnyStringNode(raw["type"])),
		ID:       strings.TrimSpace(contracts.AnyStringNode(raw["id"])),
		Code:     contracts.AnyIntNode(raw["code"]),
		Msg:      strings.TrimSpace(contracts.AnyStringNode(raw["msg"])),
		StreamID: strings.TrimSpace(contracts.AnyStringNode(raw["streamId"])),
		Reason:   strings.TrimSpace(contracts.AnyStringNode(raw["reason"])),
		LastSeq:  int64(contracts.AnyIntNode(raw["lastSeq"])),
	}
	if decoded.Frame != "" && !strings.EqualFold(decoded.Frame, "stream") {
		return decoded, decoded.Frame != ""
	}
	eventNode := contracts.AnyMapNode(raw["event"])
	if len(eventNode) == 0 && decoded.Frame == "" {
		eventNode = raw
	}
	if len(eventNode) > 0 {
		event := stream.EventDataFromMap(eventNode)
		if strings.TrimSpace(event.Type) != "" {
			decoded.Event = event
			decoded.HasEvent = true
		}
	}
	if decoded.Frame == "" && !decoded.HasEvent {
		return proxyDecodedFrame{}, false
	}
	return decoded, decoded.Frame != "" || decoded.HasEvent
}

func proxyFrameMatchesRequest(frame proxyDecodedFrame, requestID string) bool {
	if strings.TrimSpace(frame.Frame) == "" {
		return true
	}
	frameName := strings.ToLower(strings.TrimSpace(frame.Frame))
	switch frameName {
	case "stream", "response", "error":
	default:
		return false
	}
	frameID := strings.TrimSpace(frame.ID)
	return frameID != "" && frameID == strings.TrimSpace(requestID)
}

func proxyFrameError(frame proxyDecodedFrame) error {
	frameName := strings.ToLower(strings.TrimSpace(frame.Frame))
	switch frameName {
	case "error":
		msg := strings.TrimSpace(frame.Msg)
		if msg == "" {
			msg = "upstream websocket returned error"
		}
		if frame.Code > 0 {
			return fmt.Errorf("%s (%d)", msg, frame.Code)
		}
		return fmt.Errorf("%s", msg)
	case "response":
		if frame.Code == 0 {
			return nil
		}
		msg := strings.TrimSpace(frame.Msg)
		if msg == "" {
			msg = "upstream websocket request failed"
		}
		return fmt.Errorf("%s (%d)", msg, frame.Code)
	default:
		return nil
	}
}

func decodeProxyEvent(data []byte) (stream.EventData, bool) {
	frame, ok := decodeProxyFrame(data)
	if !ok || !frame.HasEvent {
		return stream.EventData{}, false
	}
	return frame.Event, true
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

func (s *Server) forwardProxySubmit(req api.SubmitRequest) (api.SubmitResponse, *statusError, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.SubmitResponse{}, nil, false
	}
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.agentKey) {
		return api.SubmitResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	payload := map[string]any{
		"runId":      req.RunID,
		"chatId":     route.chatID,
		"agentKey":   route.agentKey,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
		"params":     req.Params,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    proxyRouteRequestType(route, "submit"),
		"id":      req.AwaitingID,
		"payload": payload,
	}) {
		return api.SubmitResponse{
			Accepted:   false,
			Status:     "unmatched",
			ChatID:     route.chatID,
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			SubmitID:   req.SubmitID,
			Detail:     "Proxy run is no longer active",
		}, nil, true
	}
	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     route.chatID,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		SubmitID:   req.SubmitID,
		Detail:     "Proxy submit forwarded",
	}, nil, true
}

func (s *Server) forwardProxyAccessLevel(req api.AccessLevelRequest) (api.AccessLevelResponse, *statusError, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.AccessLevelResponse{}, nil, false
	}
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.agentKey) {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	payload := map[string]any{
		"requestId":   req.RequestID,
		"runId":       req.RunID,
		"chatId":      route.chatID,
		"agentKey":    route.agentKey,
		"accessLevel": req.AccessLevel,
		"reason":      req.Reason,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    proxyRouteRequestType(route, "access-level"),
		"id":      firstNonBlank(strings.TrimSpace(req.RequestID), req.RunID),
		"payload": payload,
	}) {
		return api.AccessLevelResponse{
			Accepted:    false,
			Status:      "unmatched",
			RunID:       req.RunID,
			AccessLevel: req.AccessLevel,
			Detail:      "Proxy run is no longer active",
		}, nil, true
	}
	ack := s.deps.Runs.UpdateAccessLevel(req)
	return api.AccessLevelResponse{
		Accepted:            ack.Accepted,
		Status:              ack.Status,
		RunID:               req.RunID,
		PreviousAccessLevel: ack.PreviousAccessLevel,
		AccessLevel:         ack.AccessLevel,
		Version:             ack.Version,
		Detail:              ack.Detail,
	}, nil, true
}

func (s *Server) forwardProxyInterrupt(req api.InterruptRequest) (api.InterruptResponse, *statusError, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.InterruptResponse{}, nil, false
	}
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.agentKey) {
		return api.InterruptResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	forwarded := proxyWSInterruptRequest(req)
	payload := map[string]any{
		"requestId": forwarded.RequestID,
		"runId":     forwarded.RunID,
		"chatId":    route.chatID,
		"agentKey":  route.agentKey,
		"message":   forwarded.Message,
		"source":    forwarded.InterruptSource,
		"reason":    forwarded.InterruptReason,
		"detail":    forwarded.InterruptDetail,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    proxyRouteRequestType(route, "interrupt"),
		"id":      forwarded.RequestID,
		"payload": payload,
	}) {
		return api.InterruptResponse{
			Accepted: false,
			Status:   "unmatched",
			RunID:    req.RunID,
			Detail:   "Proxy run is no longer active",
		}, nil, true
	}
	return api.InterruptResponse{
		Accepted: true,
		Status:   "accepted",
		RunID:    req.RunID,
		Detail:   "Proxy interrupt forwarded",
	}, nil, true
}

func (s *Server) forwardProxySteer(req api.SteerRequest) (api.SteerResponse, *statusError, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.SteerResponse{}, nil, false
	}
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.agentKey) {
		return api.SteerResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	steerID := strings.TrimSpace(req.SteerID)
	if steerID == "" {
		steerID = time.Now().UTC().Format("20060102150405.000000000")
	}
	payload := map[string]any{
		"requestId": req.RequestID,
		"runId":     req.RunID,
		"chatId":    route.chatID,
		"agentKey":  route.agentKey,
		"steerId":   steerID,
		"message":   req.Message,
	}
	if !sendProxyRouteMessage(route, map[string]any{
		"frame":   "request",
		"type":    proxyRouteRequestType(route, "steer"),
		"id":      steerID,
		"payload": payload,
	}) {
		return api.SteerResponse{
			Accepted: false,
			Status:   "unmatched",
			RunID:    req.RunID,
			SteerID:  steerID,
			Detail:   "Proxy run is no longer active",
		}, nil, true
	}
	return api.SteerResponse{
		Accepted: true,
		Status:   "accepted",
		RunID:    req.RunID,
		SteerID:  steerID,
		Detail:   "Proxy steer forwarded",
	}, nil, true
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
	req               api.QueryRequest
	agentDef          catalog.AgentDefinition
	chatStore         chat.Store
	stepWriter        *chat.StepWriter
	control           *contracts.RunControl
	notifications     contracts.NotificationSink
	usageTracker      *proxyUsageTracker
	awaiting          awaitingTracker
	assistantText     strings.Builder
	startedAt         int64
	finishReason      string
	runUsage          chat.UsageData
	contents          map[string]*proxyContentBucket
	reasonings        map[string]*proxyContentBucket
	tools             map[string]*proxyToolBucket
	planningSnapshots map[string]bool
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
	notifications contracts.NotificationSink,
	chatUsage chat.UsageData,
	models *models.ModelRegistry,
	billing config.BillingConfig,
) *proxyEventRecorder {
	if stepWriter == nil {
		return nil
	}
	queryPayload := map[string]any{
		"requestId": req.RequestID,
		"runId":     req.RunID,
		"chatId":    req.ChatID,
		"agentKey":  req.AgentKey,
		"role":      req.Role,
		"message":   req.Message,
	}
	if req.IncludeUsage {
		queryPayload["includeUsage"] = true
	}
	if req.IncludeFullText {
		queryPayload["includeFullText"] = true
	}
	if req.PlanningMode != nil {
		queryPayload["planningMode"] = *req.PlanningMode
	}
	stepWriter.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: time.Now().UnixMilli(),
		Payload:   queryPayload,
	})
	recorder := &proxyEventRecorder{
		req:               req,
		agentDef:          agentDef,
		chatStore:         chatStore,
		stepWriter:        stepWriter,
		control:           control,
		notifications:     notifications,
		startedAt:         time.Now().UnixMilli(),
		contents:          map[string]*proxyContentBucket{},
		reasonings:        map[string]*proxyContentBucket{},
		tools:             map[string]*proxyToolBucket{},
		planningSnapshots: map[string]bool{},
	}
	recorder.usageTracker = newProxyUsageTracker(chatUsage, &recorder.runUsage, models, billing)
	return recorder
}

func (r *proxyEventRecorder) DecorateEvent(event *stream.EventData) {
	if r == nil || r.usageTracker == nil {
		return
	}
	r.usageTracker.Decorate(event)
}

func publishProxyLiveEvent(eventBus *stream.RunEventBus, recorder *proxyEventRecorder, req api.QueryRequest, seq *int64, event stream.EventData) stream.EventData {
	event = normalizeProxyEventIdentity(event, req)
	if event.Timestamp <= 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	if snapshot, ok := recorder.syntheticPlanningSnapshotBeforeAwaiting(event); ok {
		assignProxySyntheticSeq(&snapshot, seq, event.Seq)
		publishProxyEventData(eventBus, recorder, snapshot)
	}
	assignProxyEventSeq(&event, seq)
	publishProxyEventData(eventBus, recorder, event)
	return event
}

func assignProxyEventSeq(event *stream.EventData, seq *int64) {
	if event == nil || seq == nil {
		return
	}
	if event.Seq <= 0 || event.Seq <= *seq {
		*seq = *seq + 1
		event.Seq = *seq
		return
	}
	*seq = event.Seq
}

func assignProxySyntheticSeq(event *stream.EventData, seq *int64, beforeSeq int64) {
	if event == nil || seq == nil {
		return
	}
	if beforeSeq > 0 && beforeSeq > *seq {
		event.Seq = beforeSeq
		*seq = beforeSeq
		return
	}
	*seq = *seq + 1
	event.Seq = *seq
}

func publishProxyEventData(eventBus *stream.RunEventBus, recorder *proxyEventRecorder, event stream.EventData) {
	if recorder != nil {
		recorder.DecorateEvent(&event)
	}
	if eventBus != nil {
		eventBus.Publish(event)
	}
	if recorder != nil {
		recorder.OnEvent(event)
	}
}

func (r *proxyEventRecorder) syntheticPlanningSnapshotBeforeAwaiting(event stream.EventData) (stream.EventData, bool) {
	if r == nil || event.Type != "awaiting.ask" || !strings.EqualFold(strings.TrimSpace(event.String("mode")), "plan") {
		return stream.EventData{}, false
	}
	plan := contracts.AnyMapNode(event.Value("plan"))
	if strings.TrimSpace(contracts.AnyStringNode(plan["text"])) == "" {
		return stream.EventData{}, false
	}
	chatDir := ""
	if r.chatStore != nil {
		chatDir = r.chatStore.ChatDir(r.req.ChatID)
	}
	state, snapshot := chat.PlanningSnapshotFromAwaitingItem(eventPayloadWithType(event), r.req.ChatID, r.req.RunID, chatDir, event.Timestamp)
	if state == nil || snapshot == nil || strings.TrimSpace(state.Markdown) == "" || r.hasPlanningSnapshot(state.PlanningID) {
		return stream.EventData{}, false
	}
	if snapshot.Timestamp <= 0 {
		snapshot.Timestamp = event.Timestamp
	}
	return *snapshot, true
}

func (r *proxyEventRecorder) hasPlanningSnapshot(planningID string) bool {
	if r == nil || strings.TrimSpace(planningID) == "" {
		return false
	}
	return r.planningSnapshots[strings.TrimSpace(planningID)]
}

func (r *proxyEventRecorder) markPlanningSnapshot(event stream.EventData) {
	if r == nil {
		return
	}
	planningID := strings.TrimSpace(event.String("planningId"))
	if planningID == "" {
		return
	}
	if r.planningSnapshots == nil {
		r.planningSnapshots = map[string]bool{}
	}
	r.planningSnapshots[planningID] = true
}

func eventPayloadWithType(event stream.EventData) map[string]any {
	payload := make(map[string]any, len(event.Payload)+3)
	for key, value := range event.Payload {
		payload[key] = value
	}
	payload["type"] = event.Type
	if event.Seq > 0 {
		payload["seq"] = event.Seq
	}
	if event.Timestamp > 0 {
		payload["timestamp"] = event.Timestamp
	}
	return payload
}

func (r *proxyEventRecorder) OnEvent(event stream.EventData) {
	if r == nil || r.stepWriter == nil {
		return
	}
	if event.Type == "planning.snapshot" {
		r.markPlanningSnapshot(event)
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
		fileChange, _ := event.Payload["fileChange"].(map[string]any)
		b := r.tools[id]
		delete(r.tools, id)
		if b == nil {
			b = &proxyToolBucket{}
		}
		payload := map[string]any{
			"toolId":    id,
			"runId":     b.runID,
			"toolName":  b.toolName,
			"arguments": b.args.String(),
		}
		if len(fileChange) > 0 {
			payload["fileChange"] = fileChange
		}
		r.stepWriter.OnEvent(stream.EventData{
			Type:      "tool.snapshot",
			Timestamp: event.Timestamp,
			Payload:   payload,
		})
	case "usage.snapshot":
		r.stepWriter.OnEvent(event)
	case "awaiting.ask":
		r.handleLiveLifecycle(event)
		r.stepWriter.OnEvent(event)
	case "awaiting.answer":
		r.handleLiveLifecycle(event)
		r.stepWriter.OnEvent(event)
	case "run.complete":
		r.finishReason = "complete"
		r.stepWriter.OnEvent(event)
	case "run.cancel":
		r.maybeResolvePendingAwaiting()
		r.finishReason = "cancel"
		r.stepWriter.OnEvent(event)
	case "run.error":
		r.maybeResolvePendingAwaiting()
		r.finishReason = "error"
		r.stepWriter.OnEvent(event)
	case "artifact.publish":
		r.stepWriter.OnEvent(event)
		r.broadcastResourcePushed(event)
	case "tool.result",
		"task.start", "task.complete", "task.cancel", "task.error",
		"plan.create", "plan.update", "source.publish",
		"planning.start", "planning.delta", "planning.end", "planning.snapshot",
		"request.submit", "request.steer":
		r.stepWriter.OnEvent(event)
	}
}

func (r *proxyEventRecorder) handleLiveLifecycle(event stream.EventData) {
	if r == nil {
		return
	}
	handleAwaitingLifecycle(RunExecutorParams{
		Session: contracts.QuerySession{
			ChatID:   r.req.ChatID,
			RunID:    r.req.RunID,
			AgentKey: r.req.AgentKey,
			TeamID:   r.req.TeamID,
		},
		Chats:         r.chatStore,
		RunControl:    r.control,
		Notifications: r.notifications,
	}, event, &r.awaiting)
}

func (r *proxyEventRecorder) maybeResolvePendingAwaiting() {
	if r == nil {
		return
	}
	maybeBroadcastInterruptedAwaiting(RunExecutorParams{
		Session: contracts.QuerySession{
			ChatID:   r.req.ChatID,
			RunID:    r.req.RunID,
			AgentKey: r.req.AgentKey,
			TeamID:   r.req.TeamID,
		},
		Chats:         r.chatStore,
		Notifications: r.notifications,
	}, &r.awaiting)
}

func (r *proxyEventRecorder) broadcastResourcePushed(event stream.EventData) {
	if r == nil || r.notifications == nil {
		return
	}
	chatID := strings.TrimSpace(event.String("chatId"))
	if chatID == "" {
		chatID = r.req.ChatID
	}
	if chatID == "" {
		return
	}
	timestamp := event.Timestamp
	if timestamp <= 0 {
		timestamp = time.Now().UnixMilli()
	}
	for _, artifact := range proxyArtifactItems(event.Value("artifacts")) {
		artifactID := strings.TrimSpace(contracts.AnyStringNode(artifact["artifactId"]))
		name := strings.TrimSpace(contracts.AnyStringNode(artifact["name"]))
		if artifactID == "" && name == "" {
			continue
		}
		payload := map[string]any{
			"chatId":     chatID,
			"artifactId": artifactID,
			"name":       name,
			"mimeType":   strings.TrimSpace(contracts.AnyStringNode(artifact["mimeType"])),
			"sha256":     strings.TrimSpace(contracts.AnyStringNode(artifact["sha256"])),
			"sizeBytes":  contracts.AnyIntNode(artifact["sizeBytes"]),
			"timestamp":  timestamp,
		}
		r.notifications.Broadcast("resource.pushed", payload)
	}
}

func proxyArtifactItems(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			if item := contracts.AnyMapNode(raw); len(item) > 0 {
				items = append(items, item)
			}
		}
		return items
	default:
		return nil
	}
}

func (r *proxyEventRecorder) Finish() (bool, chat.RunCompletion) {
	if r == nil || r.stepWriter == nil {
		return false, chat.RunCompletion{}
	}
	r.stepWriter.Flush()
	if r.req.ChatID == "" || r.req.RunID == "" {
		return false, chat.RunCompletion{}
	}
	if r.chatStore == nil {
		return false, chat.RunCompletion{}
	}
	finishReason := r.finishReason
	if strings.TrimSpace(finishReason) == "" {
		finishReason = "complete"
	}
	completion := chat.RunCompletion{
		ChatID:          r.req.ChatID,
		RunID:           r.req.RunID,
		AgentKey:        r.req.AgentKey,
		AssistantText:   r.assistantText.String(),
		InitialMessage:  r.req.Message,
		FinishReason:    finishReason,
		StartedAtMillis: r.startedAt,
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage:           r.runUsage,
	}
	if err := r.chatStore.OnRunCompleted(completion); err != nil {
		log.Printf("[proxy][ws] OnRunCompleted failed: %v", err)
		return false, chat.RunCompletion{}
	}
	return true, completion
}

func awaitingContextFromProxyEvent(event stream.EventData) contracts.AwaitingSubmitContext {
	mode, _ := event.Payload["mode"].(string)
	awaitingID, _ := event.Payload["awaitingId"].(string)
	return contracts.AwaitingSubmitContext{
		AwaitingID: awaitingID,
		Mode:       mode,
		ItemCount:  proxyAwaitItemCount(mode, event.Payload["questions"], event.Payload["approvals"], event.Payload["forms"], event.Payload["plan"]),
		Questions:  proxyAwaitQuestions(mode, event.Payload["questions"]),
	}
}

func proxyAwaitQuestions(mode string, value any) []any {
	if !strings.EqualFold(strings.TrimSpace(mode), "question") {
		return nil
	}
	switch questions := value.(type) {
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

func proxyAwaitItemCount(mode string, questions any, approvals any, forms any, plan any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return lenAnySlice(questions)
	case "approval":
		return lenAnySlice(approvals)
	case "form":
		return lenAnySlice(forms)
	case "plan":
		if lenAnyMap(plan) > 0 {
			return 1
		}
		return 0
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

func lenAnyMap(value any) int {
	if item, ok := value.(map[string]any); ok {
		return len(item)
	}
	return 0
}
