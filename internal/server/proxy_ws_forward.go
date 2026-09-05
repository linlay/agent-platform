package server

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

func proxyWebSocketTarget(proxy *catalog.ProxyConfig) (string, http.Header, error) {
	return runtimeproxy.WebSocketTarget(proxy)
}

func proxyUpstreamTransport(proxy *catalog.ProxyConfig) string {
	return runtimeproxy.UpstreamTransport(proxy)
}

func proxyQueryPayload(req api.QueryRequest, proxy *catalog.ProxyConfig, references []api.Reference) map[string]any {
	return runtimeproxy.QueryPayload(queryCommandFromAPI(req), proxy, runtimeReferencesFromAPI(references))
}

func proxyRequestType(proxy *catalog.ProxyConfig, name string) string {
	return runtimeproxy.RequestType(proxy, name)
}

func proxyRouteRequestType(route *proxyRunRoute, name string) string {
	return route.RequestType(name)
}

func proxyProtocol(proxy *catalog.ProxyConfig) string {
	return runtimeproxy.Protocol(proxy)
}

func proxyQueryPayloadWithWorkspace(req api.QueryRequest, proxy *catalog.ProxyConfig, references []api.Reference, workspaceRoot string) map[string]any {
	return runtimeproxy.QueryPayloadWithWorkspace(queryCommandFromAPI(req), proxy, runtimeReferencesFromAPI(references), workspaceRoot)
}

func proxyForwardParams(req api.QueryRequest, workspaceRoot string) map[string]any {
	return runtimeproxy.ForwardParams(queryCommandFromAPI(req), workspaceRoot)
}

func proxyRequestHasReservedCWD(params map[string]any) bool {
	return runtimeproxy.RequestHasReservedCWD(params)
}

func proxyAgentKey(proxy *catalog.ProxyConfig, fallback string) string {
	return runtimeproxy.AgentKey(proxy, fallback)
}

type proxyDecodedFrame = runtimeproxy.DecodedFrame

// decodeProxyFrameAt preserves JSON numeric syntax and validates every
// upstream stream event before it can be forwarded or persisted. A malformed
// non-event frame retains the historical "ignore it" behavior; a malformed
// timestamp is a contract violation and must terminate the proxy run.
func decodeProxyFrameAt(data []byte, eventLocation string) (proxyDecodedFrame, bool, error) {
	return runtimeproxy.DecodeFrameAt(data, eventLocation)
}

func proxyFrameMatchesRequest(frame proxyDecodedFrame, requestID string) bool {
	return runtimeproxy.FrameMatchesRequest(frame, requestID)
}

func proxyFrameError(frame proxyDecodedFrame) error {
	return runtimeproxy.FrameError(frame)
}

func decodeProxyEventAt(data []byte, eventLocation string) (stream.EventData, bool, error) {
	return runtimeproxy.DecodeEventAt(data, eventLocation)
}

func normalizeProxyEventIdentity(event stream.EventData, req api.QueryRequest) stream.EventData {
	return runtimeproxy.NormalizeEventIdentity(event, queryCommandFromAPI(req))
}

func normalizeProxyArtifactURLs(event *stream.EventData, chatID string) {
	runtimeproxy.NormalizeArtifactURLs(event, chatID)
}

func proxyPublicArtifactURL(raw string, chatID string) (string, bool) {
	return runtimeproxy.PublicArtifactURL(raw, chatID)
}

func proxyRunErrorEvent(req api.QueryRequest, err error) stream.EventData {
	payload := map[string]any{
		"runId":   req.RunID,
		"chatId":  req.ChatID,
		"message": err.Error(),
		"error":   err.Error(),
	}
	if isTimeContractViolation(err) {
		contractData := timeContractErrorData(err)
		payload["message"] = timeContractViolationMessage
		payload["error"] = contractData
		for _, key := range []string{"code", "field", "location", "expected"} {
			if value, ok := contractData[key]; ok {
				payload[key] = value
			}
		}
	}
	return stream.EventData{
		Seq:       1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func (s *Server) publishProxyError(
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	req api.QueryRequest,
	err error,
) {
	event := proxyRunErrorEvent(req, err)
	log.Printf("[proxy][ws] %s", err)
	if eventBus != nil {
		eventBus.Publish(event)
	}
	if recorder != nil {
		recorder.OnEvent(event)
	}
}

func (s *Server) publishProxyErrorAfter(
	eventBus *stream.RunEventBus,
	recorder *proxyEventRecorder,
	req api.QueryRequest,
	err error,
	lastSeq int64,
) {
	event := proxyRunErrorEvent(req, err)
	event.Seq = lastSeq + 1
	if event.Seq <= 0 {
		event.Seq = 1
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
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.AgentKey) {
		return api.SubmitResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	if route.Transport == "sse" {
		var response api.SubmitResponse
		statusErr := postProxyRunControl(route, "/api/submit", map[string]any{
			"runId":      req.RunID,
			"chatId":     route.ChatID,
			"agentKey":   firstNonBlank(route.UpstreamAgentKey, route.AgentKey),
			"awaitingId": req.AwaitingID,
			"submitId":   req.SubmitID,
			"params":     req.Params,
		}, &response)
		return response, statusErr, true
	}
	payload := map[string]any{
		"runId":      req.RunID,
		"chatId":     route.ChatID,
		"agentKey":   route.AgentKey,
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
			ChatID:     route.ChatID,
			RunID:      req.RunID,
			AwaitingID: req.AwaitingID,
			SubmitID:   req.SubmitID,
			Detail:     "Proxy run is no longer active",
		}, nil, true
	}
	return api.SubmitResponse{
		Accepted:   true,
		Status:     "accepted",
		ChatID:     route.ChatID,
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
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.AgentKey) {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	payload := map[string]any{
		"requestId":   req.RequestID,
		"runId":       req.RunID,
		"chatId":      route.ChatID,
		"agentKey":    route.AgentKey,
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
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.AgentKey) {
		return api.InterruptResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	forwarded := proxyWSInterruptRequest(req)
	if route.Transport == "sse" {
		var response api.InterruptResponse
		statusErr := postProxyRunControl(route, "/api/interrupt", map[string]any{
			"requestId": forwarded.RequestID,
			"runId":     forwarded.RunID,
			"chatId":    route.ChatID,
			"agentKey":  firstNonBlank(route.UpstreamAgentKey, route.AgentKey),
			"message":   forwarded.Message,
			"source":    forwarded.InterruptSource,
			"reason":    forwarded.InterruptReason,
			"detail":    forwarded.InterruptDetail,
		}, &response)
		return response, statusErr, true
	}
	payload := map[string]any{
		"requestId": forwarded.RequestID,
		"runId":     forwarded.RunID,
		"chatId":    route.ChatID,
		"agentKey":  route.AgentKey,
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
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.AgentKey) {
		return api.SteerResponse{}, &statusError{status: http.StatusForbidden, message: "agentKey does not match run"}, true
	}
	steerID := strings.TrimSpace(req.SteerID)
	if steerID == "" {
		steerID = time.Now().UTC().Format("20060102150405.000000000")
	}
	if route.Transport == "sse" {
		var response api.SteerResponse
		statusErr := postProxyRunControl(route, "/api/steer", map[string]any{
			"requestId": req.RequestID,
			"runId":     req.RunID,
			"chatId":    route.ChatID,
			"agentKey":  firstNonBlank(route.UpstreamAgentKey, route.AgentKey),
			"steerId":   steerID,
			"message":   req.Message,
		}, &response)
		return response, statusErr, true
	}
	payload := map[string]any{
		"requestId": req.RequestID,
		"runId":     req.RunID,
		"chatId":    route.ChatID,
		"agentKey":  route.AgentKey,
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

func postProxyRunControl(route *proxyRunRoute, path string, payload map[string]any, target any) *statusError {
	if route == nil {
		return &statusError{status: http.StatusBadGateway, code: "proxy_unavailable", message: "proxy control endpoint is unavailable"}
	}
	err := route.PostControl(path, payload, target)
	if err == nil {
		return nil
	}
	statusErr := &statusError{status: http.StatusBadGateway, code: "proxy_unavailable", message: err.Error()}
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		switch appErr.Code() {
		case apperrors.CodeInternalError:
			statusErr.status = http.StatusInternalServerError
			statusErr.code = "internal_error"
		case apperrors.CodeProxyBadResponse:
			statusErr.code = "proxy_invalid_response"
		case apperrors.CodeProxyUpstreamError:
			statusErr.code = "proxy_control_failed"
		}
	}
	return statusErr
}

func sendProxyRouteMessage(route *proxyRunRoute, payload map[string]any) bool {
	return route.Send(payload)
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
	markdownGuards    map[string]*stream.MarkdownDestinationGuard
	markdownText      map[string]*strings.Builder
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
	startedAtMillis int64,
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
	if req.Hidden != nil {
		queryPayload["hidden"] = *req.Hidden
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
	if len(req.MustUseSkills) > 0 {
		queryPayload["mustUseSkills"] = append([]string(nil), req.MustUseSkills...)
	}
	for key, value := range req.TrustedQueryMetadata {
		if _, reserved := queryPayload[key]; !reserved {
			queryPayload[key] = value
		}
	}
	stepWriter.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: startedAtMillis,
		Payload:   queryPayload,
	})
	recorder := &proxyEventRecorder{
		req:               req,
		agentDef:          agentDef,
		chatStore:         chatStore,
		stepWriter:        stepWriter,
		control:           control,
		notifications:     notifications,
		startedAt:         startedAtMillis,
		contents:          map[string]*proxyContentBucket{},
		reasonings:        map[string]*proxyContentBucket{},
		tools:             map[string]*proxyToolBucket{},
		planningSnapshots: map[string]bool{},
		markdownGuards:    map[string]*stream.MarkdownDestinationGuard{},
		markdownText:      map[string]*strings.Builder{},
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

func (r *proxyEventRecorder) sanitizeMarkdownEvent(event *stream.EventData) {
	if r == nil || event == nil {
		return
	}
	contentID := strings.TrimSpace(event.String("contentId"))
	switch event.Type {
	case "content.start":
		if contentID != "" {
			r.markdownGuards[contentID] = stream.NewMarkdownDestinationGuard(r.req.ChatID)
			r.markdownText[contentID] = &strings.Builder{}
		}
	case "content.delta":
		if contentID == "" {
			return
		}
		guard := r.markdownGuards[contentID]
		if guard == nil {
			guard = stream.NewMarkdownDestinationGuard(r.req.ChatID)
			r.markdownGuards[contentID] = guard
		}
		safeDelta := guard.Write(event.String("delta"))
		event.Payload["delta"] = safeDelta
		buffer := r.markdownText[contentID]
		if buffer == nil {
			buffer = &strings.Builder{}
			r.markdownText[contentID] = buffer
		}
		buffer.WriteString(safeDelta)
	case "content.end":
		guard := r.markdownGuards[contentID]
		delete(r.markdownGuards, contentID)
		buffer := r.markdownText[contentID]
		delete(r.markdownText, contentID)
		if text := event.String("text"); text != "" {
			fullGuard := stream.NewMarkdownDestinationGuard(r.req.ChatID)
			event.Payload["text"] = fullGuard.Write(text) + fullGuard.Flush()
		} else {
			var safeText strings.Builder
			if buffer != nil {
				safeText.WriteString(buffer.String())
			}
			if guard != nil {
				safeText.WriteString(guard.Flush())
			}
			event.Payload["text"] = safeText.String()
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			fullGuard := stream.NewMarkdownDestinationGuard(r.req.ChatID)
			event.Payload["text"] = fullGuard.Write(text) + fullGuard.Flush()
		}
	}
}

func publishProxyLiveEvent(eventBus *stream.RunEventBus, recorder *proxyEventRecorder, req api.QueryRequest, seq *int64, event stream.EventData) (stream.EventData, error) {
	event = normalizeProxyEventIdentity(event, req)
	if recorder != nil {
		recorder.sanitizeMarkdownEvent(&event)
	}
	if err := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "proxy.upstream.event"); err != nil {
		return stream.EventData{}, err
	}
	if snapshot, ok := recorder.syntheticPlanningSnapshotBeforeAwaiting(event); ok {
		assignProxySyntheticSeq(&snapshot, seq, event.Seq)
		publishProxyEventData(eventBus, recorder, snapshot)
	}
	assignProxyEventSeq(&event, seq)
	publishProxyEventData(eventBus, recorder, event)
	return event, nil
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
	if r == nil || event.Type != "awaiting.ask" || !strings.EqualFold(strings.TrimSpace(event.String("mode")), "planning") {
		return stream.EventData{}, false
	}
	planning := contracts.AnyMapNode(event.Value("planning"))
	if strings.TrimSpace(contracts.AnyStringNode(planning["text"])) == "" {
		return stream.EventData{}, false
	}
	chatDir := ""
	if r.chatStore != nil {
		chatDir = r.chatStore.ChatDir(r.req.ChatID)
	}
	if strings.TrimSpace(contracts.AnyStringNode(planning["planningFile"])) == "" {
		planningID := strings.TrimSpace(contracts.AnyStringNode(planning["planningId"]))
		if planningID == "" || filepath.Base(planningID) != planningID || chatDir == "" {
			return stream.EventData{}, false
		}
		planningFile := filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolPlanningDirName, planningID+".md")
		if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
			return stream.EventData{}, false
		}
		if err := os.WriteFile(planningFile, []byte(contracts.AnyStringNode(planning["text"])), 0o644); err != nil {
			return stream.EventData{}, false
		}
		planning["planningFile"] = planningFile
		event.Payload["planning"] = planning
	}
	state, snapshot := chat.PlanningSnapshotFromAwaitingItem(eventPayloadWithType(event), r.req.ChatID, r.req.RunID, chatDir)
	if state == nil || snapshot == nil || strings.TrimSpace(state.Markdown) == "" || r.hasPlanningSnapshot(state.PlanningID) {
		return stream.EventData{}, false
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
			RunOwner: contracts.AgentRunOwner(r.req.AgentKey, r.req.TeamID),
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
			RunOwner: contracts.AgentRunOwner(r.req.AgentKey, r.req.TeamID),
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
	if err := timecontract.ValidateEpochMillis(timestamp, "timestamp", "proxy.resource.pushed"); err != nil {
		log.Printf("[proxy][ws] refusing resource.pushed with invalid timestamp: %v", err)
		return
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
			"pushedAt":   timestamp,
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
	if r == nil {
		return false, chat.RunCompletion{}
	}
	if r.stepWriter != nil {
		r.stepWriter.Flush()
	}
	finishReason := r.finishReason
	if strings.TrimSpace(finishReason) == "" {
		finishReason = "complete"
	}
	completion := chat.RunCompletion{
		ChatID:          r.req.ChatID,
		RunID:           r.req.RunID,
		AgentKey:        r.req.AgentKey,
		AgentMode:       catalog.AgentModeForAPI(r.agentDef.Mode),
		AssistantText:   r.assistantText.String(),
		InitialMessage:  r.req.Message,
		FinishReason:    finishReason,
		StartedAtMillis: r.startedAt,
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage:           r.runUsage,
	}
	if r.req.ChatID == "" || r.req.RunID == "" || r.chatStore == nil {
		return false, completion
	}
	if err := r.chatStore.OnRunCompleted(completion); err != nil {
		log.Printf("[proxy][ws] OnRunCompleted failed: %v", err)
		// The completion clock was captured before the persistence attempt. Keep
		// returning it so run.finished carries the same real completion instant
		// even when storage rejects a historic/invalid record.
		return false, completion
	}
	return true, completion
}

func lenAnyMap(value any) int {
	if item, ok := value.(map[string]any); ok {
		return len(item)
	}
	return 0
}
