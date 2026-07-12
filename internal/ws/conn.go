package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/config"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"

	gws "github.com/gorilla/websocket"
)

type RouteHandler func(context.Context, *Conn, RequestFrame)

type outboundMessage struct {
	frame     any
	msgType   int
	closeText string
}

const streamKindTerminal = "terminal"

type streamEntry struct {
	runID      string
	streamID   string
	kind       string
	terminalID string
	observerID string
	lastSeq    int64
	detach     func()
	detachOnce sync.Once
}

type DetachedStream struct {
	RunID           string
	StreamRequestID string
	StreamID        string
	LastSeq         int64
}

type outboundRequest struct {
	mu     sync.Mutex
	ch     chan []byte
	closed bool
}

func newOutboundRequest(buffer int) *outboundRequest {
	if buffer <= 0 {
		buffer = 1
	}
	return &outboundRequest{ch: make(chan []byte, buffer)}
}

func (r *outboundRequest) deliver(data []byte, closed <-chan struct{}) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return true
	}
	select {
	case <-closed:
		return true
	case r.ch <- append([]byte(nil), data...):
		return true
	}
}

func (r *outboundRequest) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.ch)
}

type Conn struct {
	sessionID           string
	socket              *gws.Conn
	hub                 *Hub
	cfg                 config.WebSocketConfig
	heartbeatInterval   time.Duration
	writeQueueFullGrace time.Duration
	requestBaseURL      string
	clientInfoMu        sync.RWMutex
	remoteAddr          string
	userAgent           string
	source              string
	deviceID            string
	closeReasonMu       sync.RWMutex
	closeReason         string
	localeMu            sync.RWMutex
	locale              string

	// silent=true 时：Run 不主动发 push.connected，writeLoop 不发 push.heartbeat / auth.expiring。
	// 用于 agent-platform 反向连出到网关的场景——网关按自己的节奏发注册 ACK，我们只做被动应答。
	silent bool

	authMu sync.RWMutex
	auth   AuthSession

	mu               sync.Mutex
	inflightRequests map[string]struct{}
	outboundRequests map[string]*outboundRequest
	activeStreams    map[string]*streamEntry
	cancelledStreams map[string]struct{}
	observingRuns    map[string]string
	writeQueue       chan outboundMessage
	closed           chan struct{}
	closeOnce        sync.Once
	closing          atomic.Bool
	nextStreamID     atomic.Int64
}

func (c *Conn) SetRequestBaseURL(baseURL string) {
	if c == nil {
		return
	}
	c.requestBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func (c *Conn) RequestBaseURL() string {
	if c == nil {
		return ""
	}
	return c.requestBaseURL
}

var nextSessionID atomic.Int64

// NewSilentConn 是 NewConn 的变体，用于 agent-platform 反向连出到网关的场景：
// 不主动发 push.connected / heartbeat / auth.expiring，其他行为（读 request frame、
// hub 广播、stream/response 等）完全复用。
func NewSilentConn(socket *gws.Conn, hub *Hub, cfg config.WebSocketConfig, heartbeatInterval time.Duration, auth AuthSession) *Conn {
	c := NewConn(socket, hub, cfg, heartbeatInterval, auth)
	c.silent = true
	return c
}

func NewConn(socket *gws.Conn, hub *Hub, cfg config.WebSocketConfig, heartbeatInterval time.Duration, auth AuthSession) *Conn {
	if cfg.MaxMessageSizeBytes <= 0 {
		cfg.MaxMessageSizeBytes = defaultMaxMessageSizeBytes
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.WriteQueueSize <= 0 {
		cfg.WriteQueueSize = defaultWriteQueueSize
	}
	if cfg.MaxObservesPerConn <= 0 {
		cfg.MaxObservesPerConn = defaultMaxObservesPerConn
	}
	if heartbeatInterval <= 0 {
		heartbeatInterval = time.Duration(cfg.PingInterval) * time.Second
	}
	remoteAddr := ""
	if socket != nil && socket.RemoteAddr() != nil {
		remoteAddr = socket.RemoteAddr().String()
	}
	return &Conn{
		sessionID:           fmt.Sprintf("ws_%d", nextSessionID.Add(1)),
		socket:              socket,
		hub:                 hub,
		cfg:                 cfg,
		heartbeatInterval:   heartbeatInterval,
		writeQueueFullGrace: resolvedWriteQueueFullGrace(cfg),
		remoteAddr:          remoteAddr,
		locale:              i18n.DefaultLocale,
		auth:                auth,
		inflightRequests:    map[string]struct{}{},
		outboundRequests:    map[string]*outboundRequest{},
		activeStreams:       map[string]*streamEntry{},
		cancelledStreams:    map[string]struct{}{},
		observingRuns:       map[string]string{},
		writeQueue:          make(chan outboundMessage, cfg.WriteQueueSize),
		closed:              make(chan struct{}),
	}
}

func resolvedWriteQueueFullGrace(cfg config.WebSocketConfig) time.Duration {
	grace := defaultWriteQueueFullGrace
	if cfg.WriteTimeout > 0 {
		writeTimeout := time.Duration(cfg.WriteTimeout) * time.Second
		if writeTimeout > 0 && writeTimeout < grace {
			grace = writeTimeout
		}
	}
	return grace
}

func (c *Conn) SessionID() string {
	if c == nil {
		return ""
	}
	return c.sessionID
}

func (c *Conn) ClientBoundaryKey() string {
	if c == nil {
		return ""
	}
	c.authMu.RLock()
	subject := strings.TrimSpace(c.auth.Subject)
	deviceID := strings.TrimSpace(c.auth.DeviceID)
	c.authMu.RUnlock()
	if subject != "" && deviceID != "" {
		return "subject:" + subject + "\x00device:" + deviceID
	}
	if deviceID != "" {
		return "device:" + deviceID
	}
	return "conn:" + c.SessionID()
}

func (c *Conn) SetLocale(locale string) bool {
	if c == nil {
		return false
	}
	normalized, ok := i18n.NormalizeLocale(locale)
	if !ok {
		return false
	}
	c.localeMu.Lock()
	c.locale = normalized
	c.localeMu.Unlock()
	return true
}

func (c *Conn) Locale() string {
	if c == nil {
		return i18n.DefaultLocale
	}
	c.localeMu.RLock()
	locale := c.locale
	c.localeMu.RUnlock()
	return i18n.ResolveLocale(locale)
}

func (c *Conn) Context() context.Context {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	if c.auth.Context == nil {
		return context.Background()
	}
	return c.auth.Context
}

func (c *Conn) UpdateAuth(auth AuthSession) {
	c.authMu.Lock()
	c.auth = auth
	c.authMu.Unlock()
}

func (c *Conn) Run(dispatch RouteHandler) {
	if c == nil || c.socket == nil {
		return
	}
	if c.hub != nil {
		c.hub.register(c)
	}
	defer c.close(gws.CloseNormalClosure, "connection closed")

	pongWait := 2 * time.Duration(c.cfg.PingInterval) * time.Second
	if pongWait <= 0 {
		pongWait = 60 * time.Second
	}
	c.socket.SetReadLimit(int64(c.cfg.MaxMessageSizeBytes))
	_ = c.socket.SetReadDeadline(time.Now().Add(pongWait))
	c.socket.SetPongHandler(func(string) error {
		return c.socket.SetReadDeadline(time.Now().Add(pongWait))
	})

	go c.writeLoop()
	if !c.silent {
		c.SendPush("connected", map[string]any{"sessionId": c.sessionID})
	}

	for {
		_, data, err := c.socket.ReadMessage()
		if err != nil {
			return
		}
		var req RequestFrame
		if err := json.Unmarshal(data, &req); err != nil {
			c.recordInboundMessage(data, RequestFrame{}, "invalid json frame")
			c.SendError("", "invalid_request", 400, "invalid json frame", nil)
			continue
		}
		if req.Frame != FrameRequest {
			if c.deliverOutboundFrame(req.ID, data) {
				c.recordInboundMessage(data, req, "")
				continue
			}
			// silent 模式（反向连到网关）下，网关会按 Java DownstreamAgentPush 协议
			// 主动发 push.connected / push.heartbeat 等 server-push 帧。platform 只是
			// 反向被动端，不需要主动消费 push，但也不应回 invalid_request 污染连接。
			// 对 push 帧静默放行，其他未知帧仍记录日志便于排查（不回 error 帧）。
			if c.silent {
				errText := ""
				if req.Frame != FramePush {
					errText = "unexpected frame"
					log.Printf("gateway-reverse: unexpected frame dropped: frame=%s type=%s id=%s", req.Frame, req.Type, req.ID)
				}
				c.recordInboundMessage(data, req, errText)
				continue
			}
			c.recordInboundMessage(data, req, "frame must be request")
			c.SendError(req.ID, "invalid_request", 400, "frame must be request", nil)
			continue
		}
		if strings.TrimSpace(req.ID) == "" {
			c.recordInboundMessage(data, req, "id is required")
			c.SendError("", "invalid_request", 400, "id is required", nil)
			continue
		}
		if strings.TrimSpace(req.Type) == "" {
			c.recordInboundMessage(data, req, "type is required")
			c.SendError(req.ID, "invalid_request", 400, "type is required", nil)
			continue
		}
		if err := c.reserveRequest(req.ID); err != nil {
			c.recordInboundMessage(data, req, err.Error())
			if protoErr, ok := err.(*ProtocolError); ok {
				c.SendProtocolError(req.ID, protoErr)
			}
			continue
		}
		c.recordInboundMessage(data, req, "")
		go dispatch(c.Context(), c, req)
	}
}

func (c *Conn) OpenOutboundRequest(req RequestFrame) (<-chan []byte, func(), error) {
	if c == nil {
		return nil, nil, fmt.Errorf("connection is nil")
	}
	req.Frame = FrameRequest
	req.ID = strings.TrimSpace(req.ID)
	req.Type = strings.TrimSpace(req.Type)
	if req.ID == "" {
		return nil, nil, fmt.Errorf("id is required")
	}
	if req.Type == "" {
		return nil, nil, fmt.Errorf("type is required")
	}
	outbound := newOutboundRequest(128)
	c.mu.Lock()
	if _, exists := c.outboundRequests[req.ID]; exists {
		c.mu.Unlock()
		return nil, nil, fmt.Errorf("outbound request id is already in flight")
	}
	c.outboundRequests[req.ID] = outbound
	c.mu.Unlock()
	cleanup := func() {
		c.closeOutboundRequest(req.ID)
	}
	if !c.SendFrame(req) {
		cleanup()
		return nil, nil, fmt.Errorf("websocket write queue is closed")
	}
	return outbound.ch, cleanup, nil
}

func (c *Conn) SendFrame(frame any) bool {
	return c.enqueue(outboundMessage{
		frame:   frame,
		msgType: gws.TextMessage,
	})
}

func (c *Conn) deliverOutboundFrame(id string, data []byte) bool {
	if c == nil || strings.TrimSpace(id) == "" {
		return false
	}
	c.mu.Lock()
	outbound := c.outboundRequests[strings.TrimSpace(id)]
	c.mu.Unlock()
	if outbound == nil {
		return false
	}
	return outbound.deliver(data, c.closed)
}

func (c *Conn) closeOutboundRequest(id string) {
	if c == nil || strings.TrimSpace(id) == "" {
		return
	}
	c.mu.Lock()
	outbound := c.outboundRequests[strings.TrimSpace(id)]
	delete(c.outboundRequests, strings.TrimSpace(id))
	c.mu.Unlock()
	if outbound != nil {
		outbound.close()
	}
}

func (c *Conn) reserveRequest(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.inflightRequests[id]; exists {
		return &ProtocolError{Code: 409, Type: "duplicate_id", Msg: "request id is already in flight"}
	}
	c.inflightRequests[id] = struct{}{}
	return nil
}

func (c *Conn) CompleteRequest(id string) {
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	delete(c.inflightRequests, id)
	c.mu.Unlock()
}

func (c *Conn) ReserveStream(requestID string, runID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if currentID, exists := c.observingRuns[runID]; exists {
		return "", &ProtocolError{Code: 409, Type: "duplicate_observe", Msg: fmt.Sprintf("already observing run %s on this connection", runID), Data: map[string]any{"runId": runID, "requestId": currentID}}
	}
	if c.cfg.MaxObservesPerConn > 0 && len(c.activeStreams) >= c.cfg.MaxObservesPerConn {
		return "", &ProtocolError{Code: 429, Type: "too_many_streams", Msg: "too many active streams"}
	}
	streamID := fmt.Sprintf("s_%d", c.nextStreamID.Add(1))
	c.activeStreams[requestID] = &streamEntry{runID: runID, streamID: streamID}
	c.observingRuns[runID] = requestID
	return streamID, nil
}

func (c *Conn) ReserveNamedStream(requestID string, streamID string) error {
	return c.reserveNamedStream(requestID, streamID, "", "")
}

func (c *Conn) ReserveTerminalStream(requestID string, terminalID string) error {
	return c.reserveNamedStream(requestID, terminalID, streamKindTerminal, terminalID)
}

func (c *Conn) reserveNamedStream(requestID string, streamID string, kind string, terminalID string) error {
	if c == nil {
		return &ProtocolError{Code: 500, Type: "internal_error", Msg: "connection is nil"}
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return &ProtocolError{Code: 400, Type: "invalid_request", Msg: "streamId is required"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, cancelled := c.cancelledStreams[requestID]; cancelled {
		delete(c.cancelledStreams, requestID)
		return &ProtocolError{Code: 409, Type: "stream_cancelled", Msg: fmt.Sprintf("request %s was cancelled", requestID)}
	}
	if _, exists := c.activeStreams[requestID]; exists {
		return &ProtocolError{Code: 409, Type: "duplicate_stream", Msg: fmt.Sprintf("request %s already has an active stream", requestID)}
	}
	if c.cfg.MaxObservesPerConn > 0 && len(c.activeStreams) >= c.cfg.MaxObservesPerConn {
		return &ProtocolError{Code: 429, Type: "too_many_streams", Msg: "too many active streams"}
	}
	c.activeStreams[requestID] = &streamEntry{
		streamID:   streamID,
		kind:       strings.TrimSpace(kind),
		terminalID: strings.TrimSpace(terminalID),
	}
	return nil
}

func (c *Conn) ReleaseStream(requestID string) {
	c.releaseStream(requestID, false, "", 0)
}

func (c *Conn) ReleaseTerminalStream(requestID string, terminalID string) (DetachedStream, bool) {
	if c == nil {
		return DetachedStream{}, false
	}
	requestID = strings.TrimSpace(requestID)
	terminalID = strings.TrimSpace(terminalID)
	if requestID == "" {
		return DetachedStream{}, false
	}
	c.mu.Lock()
	entry := c.activeStreams[requestID]
	if entry == nil && terminalID == "" {
		if _, inFlight := c.inflightRequests[requestID]; !inFlight || !isTerminalStreamRequestID(requestID) {
			c.mu.Unlock()
			return DetachedStream{}, false
		}
		c.cancelledStreams[requestID] = struct{}{}
		c.mu.Unlock()
		return DetachedStream{StreamRequestID: requestID}, true
	}
	matches := entry != nil &&
		entry.kind == streamKindTerminal &&
		(terminalID == "" || entry.terminalID == terminalID)
	c.mu.Unlock()
	if !matches {
		return DetachedStream{}, false
	}
	return c.releaseStream(requestID, false, "", 0)
}

func (c *Conn) TerminalIDForStream(requestID string) (string, bool) {
	if c == nil {
		return "", false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", false
	}
	c.mu.Lock()
	entry := c.activeStreams[requestID]
	c.mu.Unlock()
	if entry == nil || entry.kind != streamKindTerminal || strings.TrimSpace(entry.terminalID) == "" {
		return "", false
	}
	return entry.terminalID, true
}

func isTerminalStreamRequestID(requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	return strings.HasPrefix(requestID, "wss_") ||
		strings.HasPrefix(requestID, "wsstream_") ||
		strings.HasPrefix(requestID, "term_") ||
		strings.HasPrefix(requestID, "term-")
}

func (c *Conn) DetachRunStream(runID string) (DetachedStream, bool) {
	if c == nil {
		return DetachedStream{}, false
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return DetachedStream{}, false
	}
	c.mu.Lock()
	requestID := c.observingRuns[runID]
	c.mu.Unlock()
	if requestID == "" {
		return DetachedStream{}, false
	}
	return c.releaseStream(requestID, true, "detached", 0)
}

func (c *Conn) releaseStream(requestID string, sendTerminal bool, reason string, lastSeq int64) (DetachedStream, bool) {
	if c == nil || requestID == "" {
		return DetachedStream{}, false
	}
	var (
		entry  *streamEntry
		result DetachedStream
	)
	c.mu.Lock()
	entry = c.activeStreams[requestID]
	if entry != nil {
		delete(c.activeStreams, requestID)
		delete(c.cancelledStreams, requestID)
		delete(c.observingRuns, entry.runID)
		if lastSeq > entry.lastSeq {
			entry.lastSeq = lastSeq
		}
		result = DetachedStream{
			RunID:           entry.runID,
			StreamRequestID: requestID,
			StreamID:        entry.streamID,
			LastSeq:         entry.lastSeq,
		}
		if sendTerminal && !c.isClosed() {
			_ = c.enqueue(outboundMessage{
				frame: StreamFrame{
					Frame:    FrameStream,
					ID:       requestID,
					StreamID: entry.streamID,
					Reason:   reason,
					LastSeq:  entry.lastSeq,
				},
				msgType: gws.TextMessage,
			})
		}
	}
	delete(c.inflightRequests, requestID)
	c.mu.Unlock()
	if entry != nil && entry.detach != nil {
		entry.detachOnce.Do(entry.detach)
	}
	return result, entry != nil
}

func (c *Conn) AttachObserver(requestID string, observerID string, detach func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.activeStreams[requestID]; entry != nil {
		entry.observerID = observerID
		entry.detach = detach
	}
}

func (c *Conn) AttachStreamCleanup(requestID string, cleanup func()) {
	c.AttachObserver(requestID, "", cleanup)
}

func (c *Conn) StartStreamForward(requestID string, observer *stream.Observer) {
	if c == nil || observer == nil {
		return
	}
	go func() {
		defer observer.MarkDone()
		var (
			lastSeq int64
			reason  = "detached"
		)
		for {
			select {
			case <-c.closed:
				return
			case event, ok := <-observer.Events:
				if !ok {
					c.finishStream(requestID, reason, lastSeq)
					return
				}
				lastSeq = event.Seq
				switch event.Type {
				case "run.complete":
					reason = "done"
				case "run.error":
					reason = "error"
				case "run.cancel":
					reason = "cancelled"
				}
				if !c.sendStreamEvent(requestID, event) {
					return
				}
			}
		}
	}()
}

func (c *Conn) sendStreamEvent(requestID string, event stream.EventData) bool {
	if c == nil || requestID == "" {
		return false
	}
	event.Payload = i18n.LocalizeEventPayload(c.Locale(), event.Type, event.Payload)
	// Stream frames bypass SendResponse/SendPush, so validate the exact wire
	// representation before it reaches the writer. This keeps a bad upstream
	// timestamp from turning into a JSON-marshal failure that silently closes
	// the WebSocket.
	if _, err := json.Marshal(event); err != nil {
		if timecontract.IsViolation(err) {
			c.endStreamForTimeContractViolation(requestID, event, err)
			return false
		}
		return false
	}
	c.mu.Lock()
	entry := c.activeStreams[requestID]
	if entry == nil {
		c.mu.Unlock()
		return false
	}
	entry.lastSeq = event.Seq
	ok := c.enqueue(outboundMessage{
		frame: StreamFrame{
			Frame:    FrameStream,
			ID:       requestID,
			StreamID: entry.streamID,
			Event:    &event,
		},
		msgType: gws.TextMessage,
	})
	c.mu.Unlock()
	return ok
}

func (c *Conn) endStreamForTimeContractViolation(requestID string, source stream.EventData, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	entry := c.activeStreams[requestID]
	if entry == nil {
		c.mu.Unlock()
		c.sendTimeContractViolation(requestID, err)
		return
	}
	seq := source.Seq
	if seq <= entry.lastSeq {
		seq = entry.lastSeq + 1
	}
	if seq <= 0 {
		seq = 1
	}
	entry.lastSeq = seq
	contractData := timecontract.ErrorData(err)
	contractData["status"] = http.StatusUnprocessableEntity
	contractData["message"] = "time contract violation"
	payload := map[string]any{
		"runId":   entry.runID,
		"message": "time contract violation",
		"error":   contractData,
	}
	for _, key := range []string{"code", "field", "location", "expected"} {
		payload[key] = contractData[key]
	}
	local := stream.EventData{
		Seq:       seq,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
	ok := c.enqueue(outboundMessage{
		frame: StreamFrame{
			Frame:    FrameStream,
			ID:       requestID,
			StreamID: entry.streamID,
			Event:    &local,
		},
		msgType: gws.TextMessage,
	})
	c.mu.Unlock()
	if ok {
		c.finishStream(requestID, "error", seq)
	}
}

func (c *Conn) SendStreamEvent(requestID string, event stream.EventData) bool {
	return c.sendStreamEvent(requestID, event)
}

func (c *Conn) finishStream(requestID string, reason string, lastSeq int64) {
	c.releaseStream(requestID, true, reason, lastSeq)
}

func (c *Conn) FinishStream(requestID string, reason string, lastSeq int64) {
	c.finishStream(requestID, reason, lastSeq)
}

func (c *Conn) Done() <-chan struct{} {
	if c == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.closed
}

func (c *Conn) SendResponse(frameType string, id string, code int, msg string, data any) bool {
	if code == 0 && data != nil {
		if err := timecontract.ValidateJSONPayload(data, "ws.response."+frameType); err != nil {
			return c.sendTimeContractViolation(id, err)
		}
	}
	if msg == "" {
		msg = "success"
	}
	return c.enqueue(outboundMessage{
		frame: ResponseFrame{
			Frame: FrameResponse,
			Type:  frameType,
			ID:    id,
			Code:  code,
			Msg:   msg,
			Data:  data,
		},
		msgType: gws.TextMessage,
	})
}

func (c *Conn) SendPush(frameType string, data any) bool {
	if data != nil {
		if err := timecontract.ValidateJSONPayload(data, "ws.push."+frameType); err != nil {
			return c.sendTimeContractViolation("", err)
		}
	}
	return c.enqueue(outboundMessage{
		frame: PushFrame{
			Frame: FramePush,
			Type:  frameType,
			Data:  data,
		},
		msgType: gws.TextMessage,
	})
}

func (c *Conn) sendTimeContractViolation(id string, err error) bool {
	// Keep the contract diagnostics at the top level of WS error data. Clients
	// must not need to inspect a nested implementation payload to learn which
	// producer/storage field violated the public time contract.
	return c.SendError(id, string(apperrors.CodeTimeContractViolation), http.StatusUnprocessableEntity, "time contract violation", timecontract.ErrorData(err))
}

func (c *Conn) SendError(id string, frameType string, code int, msg string, data any) bool {
	locale := c.Locale()
	data = attachStructuredError(frameType, code, msg, data)
	return c.enqueue(outboundMessage{
		frame: ErrorFrame{
			Frame: FrameError,
			Type:  frameType,
			ID:    id,
			Code:  code,
			Msg:   i18n.Translate(locale, frameType, msg),
			Data:  i18n.LocalizeValue(locale, data),
		},
		msgType: gws.TextMessage,
	})
}

func attachStructuredError(frameType string, status int, msg string, data any) any {
	errorPayload := apperrors.Payload(apperrors.Code(frameType), msg, apperrors.WithStatus(status))
	if data == nil {
		return map[string]any{"error": errorPayload}
	}
	if typed, ok := data.(map[string]any); ok {
		out := make(map[string]any, len(typed)+1)
		for key, value := range typed {
			out[key] = value
		}
		if _, exists := out["error"]; !exists {
			out["error"] = errorPayload
		}
		return out
	}
	return data
}

func (c *Conn) SendProtocolError(id string, err *ProtocolError) bool {
	if err == nil {
		return false
	}
	return c.SendError(id, err.Type, err.Code, err.Msg, err.Data)
}

func (c *Conn) enqueue(message outboundMessage) bool {
	if c == nil || c.isClosed() {
		return false
	}
	select {
	case <-c.closed:
		return false
	case c.writeQueue <- message:
		return true
	default:
	}
	grace := c.writeQueueFullGrace
	if grace <= 0 {
		grace = defaultWriteQueueFullGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-c.closed:
		return false
	case c.writeQueue <- message:
		return true
	case <-timer.C:
		go c.close(gws.ClosePolicyViolation, "write queue full")
		return false
	}
}

func (c *Conn) writeLoop() {
	pingTicker := time.NewTicker(time.Duration(c.cfg.PingInterval) * time.Second)
	defer pingTicker.Stop()
	heartbeatTicker := time.NewTicker(c.heartbeatInterval)
	defer heartbeatTicker.Stop()

	authWarningSent := false
	for {
		select {
		case <-c.closed:
			return
		case <-heartbeatTicker.C:
			if !c.silent && !c.isClosed() {
				c.SendPush("heartbeat", map[string]any{"timestamp": time.Now().UnixMilli()})
			}
			expiresAt := c.expiresAt()
			if expiresAt > 0 {
				now := time.Now().UnixMilli()
				if !c.silent && !authWarningSent && expiresAt-now <= 30000 && expiresAt > now {
					authWarningSent = true
					c.SendPush("auth.expiring", map[string]any{"expiresAt": expiresAt})
				}
				if now >= expiresAt {
					c.close(gws.ClosePolicyViolation, "token expired")
					return
				}
			}
		case <-pingTicker.C:
			if err := c.writeControl(gws.PingMessage, nil); err != nil {
				c.close(gws.CloseAbnormalClosure, err.Error())
				return
			}
		case msg := <-c.writeQueue:
			switch msg.msgType {
			case gws.TextMessage:
				if err := c.writeJSON(msg.frame); err != nil {
					c.close(gws.CloseAbnormalClosure, err.Error())
					return
				}
				c.recordOutboundMessage(msg.frame)
			default:
				if err := c.writeControl(msg.msgType, []byte(msg.closeText)); err != nil {
					c.close(gws.CloseAbnormalClosure, err.Error())
					return
				}
			}
		}
	}
}

func (c *Conn) writeJSON(payload any) error {
	if c == nil || c.socket == nil {
		return nil
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(time.Duration(c.cfg.WriteTimeout) * time.Second))
	return c.socket.WriteJSON(payload)
}

func (c *Conn) writeControl(messageType int, payload []byte) error {
	if c == nil || c.socket == nil {
		return nil
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(time.Duration(c.cfg.WriteTimeout) * time.Second))
	return c.socket.WriteControl(messageType, payload, time.Now().Add(time.Duration(c.cfg.WriteTimeout)*time.Second))
}

func (c *Conn) expiresAt() int64 {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	return c.auth.ExpiresAt
}

func (c *Conn) isClosed() bool {
	if c == nil {
		return true
	}
	return c.closing.Load()
}

func (c *Conn) setCloseReason(reason string) {
	if c == nil {
		return
	}
	c.closeReasonMu.Lock()
	c.closeReason = strings.TrimSpace(reason)
	c.closeReasonMu.Unlock()
}

func (c *Conn) monitorCloseReason() string {
	if c == nil {
		return ""
	}
	c.closeReasonMu.RLock()
	defer c.closeReasonMu.RUnlock()
	return c.closeReason
}

func (c *Conn) close(code int, text string) {
	c.closeOnce.Do(func() {
		c.setCloseReason(text)
		c.closing.Store(true)
		close(c.closed)
		if c.hub != nil {
			c.hub.unregister(c)
		}
		c.mu.Lock()
		streams := make([]*streamEntry, 0, len(c.activeStreams))
		for _, entry := range c.activeStreams {
			streams = append(streams, entry)
		}
		c.activeStreams = map[string]*streamEntry{}
		c.observingRuns = map[string]string{}
		c.inflightRequests = map[string]struct{}{}
		outbound := make([]*outboundRequest, 0, len(c.outboundRequests))
		for _, req := range c.outboundRequests {
			outbound = append(outbound, req)
		}
		c.outboundRequests = map[string]*outboundRequest{}
		c.mu.Unlock()
		for _, req := range outbound {
			req.close()
		}
		for _, entry := range streams {
			if entry != nil && entry.detach != nil {
				entry.detachOnce.Do(entry.detach)
			}
		}
		if c.socket != nil {
			_ = c.socket.WriteControl(gws.CloseMessage, gws.FormatCloseMessage(code, text), time.Now().Add(time.Duration(c.cfg.WriteTimeout)*time.Second))
			_ = c.socket.Close()
		}
	})
}
