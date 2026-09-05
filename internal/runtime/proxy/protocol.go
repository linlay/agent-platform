// Package proxy owns outbound Proxy HTTP/WebSocket protocol adaptation. It
// does not expose local HTTP handlers.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func WebSocketTarget(proxy *catalog.ProxyConfig) (string, http.Header, error) {
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

func UpstreamTransport(proxy *catalog.ProxyConfig) string {
	if proxy != nil && strings.EqualFold(strings.TrimSpace(proxy.Transport), "sse") {
		return "sse"
	}
	return "ws"
}

func QueryPayload(req runtimetypes.QueryCommand, proxy *catalog.ProxyConfig, references []runtimetypes.Reference) map[string]any {
	payload := map[string]any{
		"requestId": req.RequestID, "runId": req.RunID, "chatId": req.ChatID,
		"agentKey": AgentKey(proxy, req.AgentKey), "role": req.Role, "message": req.Message,
		"accessLevel": req.AccessLevel, "references": references, "params": ForwardParams(req, ""),
		"model": req.Model, "scene": req.Scene, "stream": true,
	}
	if req.Hidden != nil {
		payload["hidden"] = *req.Hidden
	}
	if req.PlanningMode != nil {
		payload["planningMode"] = *req.PlanningMode
	}
	if len(req.MustUseSkills) > 0 {
		payload["mustUseSkills"] = append([]string(nil), req.MustUseSkills...)
	}
	return map[string]any{"frame": "request", "type": RequestType(proxy, "query"), "id": req.RequestID, "payload": payload}
}

func RequestType(proxy *catalog.ProxyConfig, name string) string {
	if strings.EqualFold(strings.TrimSpace(Protocol(proxy)), config.ChannelProtocolPlatformWS) {
		return "/api/" + strings.TrimSpace(name)
	}
	return "request." + strings.TrimSpace(name)
}

func Protocol(proxy *catalog.ProxyConfig) string {
	if proxy == nil || strings.TrimSpace(proxy.Protocol) == "" {
		return "agw-platform"
	}
	return strings.ToLower(strings.TrimSpace(proxy.Protocol))
}

func QueryPayloadWithWorkspace(req runtimetypes.QueryCommand, proxy *catalog.ProxyConfig, references []runtimetypes.Reference, workspaceRoot string) map[string]any {
	payload := QueryPayload(req, proxy, references)
	if inner, ok := payload["payload"].(map[string]any); ok {
		inner["params"] = ForwardParams(req, workspaceRoot)
	}
	return payload
}

func ForwardParams(req runtimetypes.QueryCommand, _ string) map[string]any {
	return contracts.CloneMap(req.Params)
}

func RequestHasReservedCWD(params map[string]any) bool {
	_, ok := params["cwd"]
	return params != nil && ok
}

func AgentKey(proxy *catalog.ProxyConfig, fallback string) string {
	if proxy != nil {
		if key := strings.TrimSpace(proxy.AgentKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(fallback)
}

type DecodedFrame struct {
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

func DecodeFrameAt(data []byte, eventLocation string) (DecodedFrame, bool, error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return DecodedFrame{}, false, nil
	}
	decoded := DecodedFrame{
		Frame: strings.TrimSpace(contracts.AnyStringNode(raw["frame"])), Type: strings.TrimSpace(contracts.AnyStringNode(raw["type"])),
		ID: strings.TrimSpace(contracts.AnyStringNode(raw["id"])), Code: contracts.AnyIntNode(raw["code"]),
		Msg: strings.TrimSpace(contracts.AnyStringNode(raw["msg"])), StreamID: strings.TrimSpace(contracts.AnyStringNode(raw["streamId"])),
		Reason: strings.TrimSpace(contracts.AnyStringNode(raw["reason"])), LastSeq: int64(contracts.AnyIntNode(raw["lastSeq"])),
	}
	if decoded.Frame != "" && !strings.EqualFold(decoded.Frame, "stream") {
		return decoded, true, nil
	}
	eventNode := contracts.AnyMapNode(raw["event"])
	if len(eventNode) == 0 && decoded.Frame == "" {
		eventNode = raw
	}
	if len(eventNode) > 0 {
		event, err := stream.ParseEventDataMap(eventNode, eventLocation)
		if err != nil {
			return DecodedFrame{}, false, err
		}
		if strings.TrimSpace(event.Type) != "" {
			decoded.Event, decoded.HasEvent = event, true
		}
	}
	if decoded.Frame == "" && !decoded.HasEvent {
		return DecodedFrame{}, false, nil
	}
	return decoded, true, nil
}

func FrameMatchesRequest(frame DecodedFrame, requestID string) bool {
	if strings.TrimSpace(frame.Frame) == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(frame.Frame)) {
	case "stream", "response", "error":
	default:
		return false
	}
	return strings.TrimSpace(frame.ID) != "" && strings.TrimSpace(frame.ID) == strings.TrimSpace(requestID)
}

func FrameError(frame DecodedFrame) error {
	switch strings.ToLower(strings.TrimSpace(frame.Frame)) {
	case "error":
		message := strings.TrimSpace(frame.Msg)
		if message == "" {
			message = "upstream websocket returned error"
		}
		if frame.Code > 0 {
			return fmt.Errorf("%s (%d)", message, frame.Code)
		}
		return fmt.Errorf("%s", message)
	case "response":
		if frame.Code == 0 {
			return nil
		}
		message := strings.TrimSpace(frame.Msg)
		if message == "" {
			message = "upstream websocket request failed"
		}
		return fmt.Errorf("%s (%d)", message, frame.Code)
	default:
		return nil
	}
}

func DecodeEventAt(data []byte, eventLocation string) (stream.EventData, bool, error) {
	frame, ok, err := DecodeFrameAt(data, eventLocation)
	if err != nil || !ok || !frame.HasEvent {
		return stream.EventData{}, false, err
	}
	return frame.Event, true, nil
}

func NormalizeEventIdentity(event stream.EventData, req runtimetypes.QueryCommand) stream.EventData {
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	for key, value := range map[string]string{"requestId": req.RequestID, "chatId": req.ChatID, "runId": req.RunID, "agentKey": req.AgentKey} {
		if strings.TrimSpace(value) != "" {
			event.Payload[key] = value
		}
	}
	if event.Type == "artifact.publish" {
		NormalizeArtifactURLs(&event, req.ChatID)
	}
	return event
}

func NormalizeArtifactURLs(event *stream.EventData, chatID string) {
	if event == nil || event.Payload == nil {
		return
	}
	rawItems, ok := event.Payload["artifacts"].([]map[string]any)
	if !ok {
		if genericItems, genericOK := event.Payload["artifacts"].([]any); genericOK {
			rawItems = make([]map[string]any, 0, len(genericItems))
			for _, rawItem := range genericItems {
				if item, itemOK := rawItem.(map[string]any); itemOK {
					rawItems = append(rawItems, item)
				}
			}
		}
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, item := range rawItems {
		publicURL, valid := PublicArtifactURL(contracts.AnyStringNode(item["url"]), chatID)
		if !valid {
			continue
		}
		cloned := contracts.CloneMap(item)
		cloned["url"] = publicURL
		items = append(items, cloned)
	}
	event.Payload["artifacts"] = items
	event.Payload["artifactCount"] = len(items)
}

func PublicArtifactURL(raw string, chatID string) (string, bool) {
	raw, chatID = strings.TrimSpace(raw), strings.TrimSpace(chatID)
	if raw == "" || chatID == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err == nil && isResourceURL(parsed, raw) {
		resourceChatID, relativePath, parseErr := chat.ParseResourceKey(strings.TrimSpace(parsed.Query().Get("file")))
		if parseErr != nil || resourceChatID != chatID {
			return "", false
		}
		publicURL, buildErr := chat.BuildChatScopeRef(relativePath)
		return publicURL, buildErr == nil
	}
	if resourceChatID, relativePath, parseErr := chat.ParseResourceKey(raw); parseErr == nil && resourceChatID == chatID {
		publicURL, buildErr := chat.BuildChatScopeRef(relativePath)
		return publicURL, buildErr == nil
	}
	resourceChatID, relativePath, parseErr := chat.ParseResourceKey(chatID + "/" + raw)
	if parseErr != nil || resourceChatID != chatID {
		return "", false
	}
	publicURL, buildErr := chat.BuildChatScopeRef(relativePath)
	return publicURL, buildErr == nil
}

func isResourceURL(parsed *url.URL, rawURL string) bool {
	if parsed == nil {
		return false
	}
	if parsed.Path == "" && strings.HasPrefix(strings.TrimSpace(rawURL), "/api/resource") {
		return true
	}
	return parsed.Path == "/api/resource" || strings.HasSuffix(parsed.Path, "/api/resource")
}
