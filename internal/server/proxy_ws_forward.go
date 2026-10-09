package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
)

func proxyWebSocketTarget(proxy *catalog.ProxyConfig) (string, http.Header, error) {
	return runtimeproxy.WebSocketTarget(proxy)
}

func proxyUpstreamTransport(proxy *catalog.ProxyConfig) string {
	return runtimeproxy.UpstreamTransport(proxy)
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

func proxyForwardParams(req api.QueryRequest, proxy *catalog.ProxyConfig, workspaceRoot string) map[string]any {
	return runtimeproxy.ForwardParams(queryCommandFromAPI(req), proxy, workspaceRoot)
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

func proxyRunErrorEvent(req api.QueryRequest, err error) stream.EventData {
	return runexec.ProxyRunErrorEvent(queryCommandFromAPI(req), err)
}

func (s *Server) forwardProxySubmit(req api.SubmitRequest) (api.SubmitResponse, *statusError, bool) {
	route, ok := s.lookupProxyRun(req.RunID)
	if !ok {
		return api.SubmitResponse{}, nil, false
	}
	if strings.TrimSpace(req.AgentKey) != strings.TrimSpace(route.AgentKey) {
		return api.SubmitResponse{}, &statusError{Status: http.StatusForbidden, Message: "agentKey does not match run"}, true
	}
	if route.Transport == "sse" {
		var response api.SubmitResponse
		body := map[string]any{
			"runId":      req.RunID,
			"chatId":     route.ChatID,
			"agentKey":   firstNonBlank(route.UpstreamAgentKey, route.AgentKey),
			"awaitingId": req.AwaitingID,
			"submitId":   req.SubmitID,
		}
		req.WriteInput(body)
		statusErr := postProxyRunControl(route, "/api/submit", body, &response)
		return response, statusErr, true
	}
	payload := map[string]any{
		"runId":      req.RunID,
		"chatId":     route.ChatID,
		"agentKey":   route.AgentKey,
		"awaitingId": req.AwaitingID,
		"submitId":   req.SubmitID,
	}
	req.WriteInput(payload)
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
		return api.AccessLevelResponse{}, &statusError{Status: http.StatusForbidden, Message: "agentKey does not match run"}, true
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
		return api.InterruptResponse{}, &statusError{Status: http.StatusForbidden, Message: "agentKey does not match run"}, true
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
		return api.SteerResponse{}, &statusError{Status: http.StatusForbidden, Message: "agentKey does not match run"}, true
	}
	steerID := strings.TrimSpace(req.SteerID)
	if steerID == "" {
		steerID = time.Now().UTC().Format("20060102150405.000000000")
	}
	if len(req.References) > 0 {
		return api.SteerResponse{Accepted: false, Status: "unsupported", RunID: req.RunID, SteerID: steerID, Detail: "attachment steer is not supported for remote runs"}, nil, true
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
		return &statusError{Status: http.StatusBadGateway, Code: "proxy_unavailable", Message: "proxy control endpoint is unavailable"}
	}
	err := route.PostControl(path, payload, target)
	if err == nil {
		return nil
	}
	statusErr := &statusError{Status: http.StatusBadGateway, Code: "proxy_unavailable", Message: err.Error()}
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		switch appErr.Code() {
		case apperrors.CodeInternalError:
			statusErr.Status = http.StatusInternalServerError
			statusErr.Code = "internal_error"
		case apperrors.CodeProxyBadResponse:
			statusErr.Code = "proxy_invalid_response"
		case apperrors.CodeProxyUpstreamError:
			statusErr.Code = "proxy_control_failed"
		}
	}
	return statusErr
}

func sendProxyRouteMessage(route *proxyRunRoute, payload map[string]any) bool {
	return route.Send(payload)
}

func lenAnyMap(value any) int {
	if item, ok := value.(map[string]any); ok {
		return len(item)
	}
	return 0
}
