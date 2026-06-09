package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/stream"

	gws "github.com/gorilla/websocket"
)

type RouteHandler func(context.Context, *Conn, RequestFrame)

const defaultWriteQueueFullGrace = 5 * time.Second

type outboundMessage struct {
	frame     any
	msgType   int
	closeText string
}

type streamEntry struct {
	runID      string
	streamID   string
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

	// silent=true 时：Run 不主动发 push.connected，writeLoop 不发 push.heartbeat / auth.expiring。
	// 用于 agent-platform 反向连出到网关的场景——网关按自己的节奏发注册 ACK，我们只做被动应答。
	silent bool

	authMu sync.RWMutex
	auth   AuthSession

	mu               sync.Mutex
	inflightRequests map[string]struct{}
	activeStreams    map[string]*streamEntry
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
		cfg.MaxMessageSizeBytes = 1 << 20
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 15
	}
	if cfg.WriteQueueSize <= 0 {
		cfg.WriteQueueSize = 256
	}
	if cfg.MaxObservesPerConn <= 0 {
		cfg.MaxObservesPerConn = 8
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
		auth:                auth,
		inflightRequests:    map[string]struct{}{},
		activeStreams:       map[string]*streamEntry{},
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
	if c == nil {
		return &ProtocolError{Code: 500, Type: "internal_error", Msg: "connection is nil"}
	}
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return &ProtocolError{Code: 400, Type: "invalid_request", Msg: "streamId is required"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.activeStreams[requestID]; exists {
		return &ProtocolError{Code: 409, Type: "duplicate_stream", Msg: fmt.Sprintf("request %s already has an active stream", requestID)}
	}
	if c.cfg.MaxObservesPerConn > 0 && len(c.activeStreams) >= c.cfg.MaxObservesPerConn {
		return &ProtocolError{Code: 429, Type: "too_many_streams", Msg: "too many active streams"}
	}
	c.activeStreams[requestID] = &streamEntry{streamID: streamID}
	return nil
}

func (c *Conn) ReleaseStream(requestID string) {
	c.releaseStream(requestID, false, "", 0)
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
	return c.enqueue(outboundMessage{
		frame: PushFrame{
			Frame: FramePush,
			Type:  frameType,
			Data:  data,
		},
		msgType: gws.TextMessage,
	})
}

func (c *Conn) SendError(id string, frameType string, code int, msg string, data any) bool {
	return c.enqueue(outboundMessage{
		frame: ErrorFrame{
			Frame: FrameError,
			Type:  frameType,
			ID:    id,
			Code:  code,
			Msg:   msg,
			Data:  data,
		},
		msgType: gws.TextMessage,
	})
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
		c.mu.Unlock()
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
