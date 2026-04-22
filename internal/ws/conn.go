package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/stream"

	gws "github.com/gorilla/websocket"
)

type RouteHandler func(context.Context, *Conn, RequestFrame)

type outboundMessage struct {
	frame     any
	msgType   int
	closeCode int
	closeText string
}

type streamEntry struct {
	runID      string
	streamID   string
	observerID string
	detach     func()
	detachOnce sync.Once
}

type Conn struct {
	sessionID         string
	socket            *gws.Conn
	hub               *Hub
	cfg               config.WebSocketConfig
	heartbeatInterval time.Duration

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
	if cfg.PingIntervalMs <= 0 {
		cfg.PingIntervalMs = 30000
	}
	if cfg.WriteTimeoutMs <= 0 {
		cfg.WriteTimeoutMs = 15000
	}
	if cfg.WriteQueueSize <= 0 {
		cfg.WriteQueueSize = 256
	}
	if cfg.MaxObservesPerConn <= 0 {
		cfg.MaxObservesPerConn = 8
	}
	if heartbeatInterval <= 0 {
		heartbeatInterval = time.Duration(cfg.PingIntervalMs) * time.Millisecond
	}
	return &Conn{
		sessionID:         fmt.Sprintf("ws_%d", nextSessionID.Add(1)),
		socket:            socket,
		hub:               hub,
		cfg:               cfg,
		heartbeatInterval: heartbeatInterval,
		auth:              auth,
		inflightRequests:  map[string]struct{}{},
		activeStreams:     map[string]*streamEntry{},
		observingRuns:     map[string]string{},
		writeQueue:        make(chan outboundMessage, cfg.WriteQueueSize),
		closed:            make(chan struct{}),
	}
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

	pongWait := 2 * time.Duration(c.cfg.PingIntervalMs) * time.Millisecond
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
			c.SendError("", "invalid_request", 400, "invalid json frame", nil)
			continue
		}
		if req.Frame != FrameRequest {
			c.SendError(req.ID, "invalid_request", 400, "frame must be request", nil)
			continue
		}
		if strings.TrimSpace(req.ID) == "" {
			c.SendError("", "invalid_request", 400, "id is required", nil)
			continue
		}
		if strings.TrimSpace(req.Type) == "" {
			c.SendError(req.ID, "invalid_request", 400, "type is required", nil)
			continue
		}
		if err := c.reserveRequest(req.ID); err != nil {
			if protoErr, ok := err.(*ProtocolError); ok {
				c.SendProtocolError(req.ID, protoErr)
			}
			continue
		}
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

func (c *Conn) ReleaseStream(requestID string) {
	if c == nil || requestID == "" {
		return
	}
	var entry *streamEntry
	c.mu.Lock()
	entry = c.activeStreams[requestID]
	if entry != nil {
		delete(c.activeStreams, requestID)
		delete(c.observingRuns, entry.runID)
	}
	delete(c.inflightRequests, requestID)
	c.mu.Unlock()
	if entry != nil && entry.detach != nil {
		entry.detachOnce.Do(entry.detach)
	}
}

func (c *Conn) AttachObserver(requestID string, observerID string, detach func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.activeStreams[requestID]; entry != nil {
		entry.observerID = observerID
		entry.detach = detach
	}
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
				case "run.expired":
					reason = "expired"
				}
				entry := c.lookupStream(requestID)
				if entry == nil {
					return
				}
				if !c.enqueue(outboundMessage{
					frame: StreamFrame{
						Frame:    FrameStream,
						ID:       requestID,
						StreamID: entry.streamID,
						Event:    &event,
					},
					msgType: gws.TextMessage,
				}) {
					return
				}
			}
		}
	}()
}

func (c *Conn) lookupStream(requestID string) *streamEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeStreams[requestID]
}

func (c *Conn) finishStream(requestID string, reason string, lastSeq int64) {
	entry := c.lookupStream(requestID)
	if entry == nil {
		return
	}
	if !c.isClosed() {
		_ = c.enqueue(outboundMessage{
			frame: StreamFrame{
				Frame:    FrameStream,
				ID:       requestID,
				StreamID: entry.streamID,
				Reason:   reason,
				LastSeq:  lastSeq,
			},
			msgType: gws.TextMessage,
		})
	}
	c.ReleaseStream(requestID)
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
		go c.close(gws.ClosePolicyViolation, "write queue full")
		return false
	}
}

func (c *Conn) writeLoop() {
	pingTicker := time.NewTicker(time.Duration(c.cfg.PingIntervalMs) * time.Millisecond)
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
	_ = c.socket.SetWriteDeadline(time.Now().Add(time.Duration(c.cfg.WriteTimeoutMs) * time.Millisecond))
	return c.socket.WriteJSON(payload)
}

func (c *Conn) writeControl(messageType int, payload []byte) error {
	if c == nil || c.socket == nil {
		return nil
	}
	_ = c.socket.SetWriteDeadline(time.Now().Add(time.Duration(c.cfg.WriteTimeoutMs) * time.Millisecond))
	return c.socket.WriteControl(messageType, payload, time.Now().Add(time.Duration(c.cfg.WriteTimeoutMs)*time.Millisecond))
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

func (c *Conn) close(code int, text string) {
	c.closeOnce.Do(func() {
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
			_ = c.socket.WriteControl(gws.CloseMessage, gws.FormatCloseMessage(code, text), time.Now().Add(time.Duration(c.cfg.WriteTimeoutMs)*time.Millisecond))
			_ = c.socket.Close()
		}
	})
}
