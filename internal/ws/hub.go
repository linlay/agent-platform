package ws

import (
	"sort"
	"strings"
	"sync"
)

type Hub struct {
	mu              sync.RWMutex
	conns           map[*Conn]struct{}
	gatewayConns    map[string]*Conn
	gatewayConnMeta map[*Conn]gatewayConnectionState
	gatewayConnSeq  int64

	monitorMu          sync.RWMutex
	monitorConns       map[string]*monitorConnectionState
	latestConnectionID string
	monitorMessages    []MonitorMessage
	monitorSeq         int64
	monitorConnSeq     int64
}

type gatewayConnectionState struct {
	channel string
	seq     int64
}

func NewHub() *Hub {
	return &Hub{
		conns:           map[*Conn]struct{}{},
		gatewayConns:    map[string]*Conn{},
		gatewayConnMeta: map[*Conn]gatewayConnectionState{},
		monitorConns:    map[string]*monitorConnectionState{},
	}
}

func (h *Hub) register(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	if gateway, ok := GatewayFromContext(conn.Context()); ok {
		channel := strings.TrimSpace(gateway.Channel)
		if channel != "" {
			h.gatewayConnSeq++
			state := gatewayConnectionState{channel: channel, seq: h.gatewayConnSeq}
			h.gatewayConnMeta[conn] = state
			h.gatewayConns[channel] = conn
		}
	}
	h.mu.Unlock()
	h.monitorRegister(conn)
}

func (h *Hub) unregister(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	delete(h.conns, conn)
	if state, ok := h.gatewayConnMeta[conn]; ok {
		delete(h.gatewayConnMeta, conn)
		if h.gatewayConns[state.channel] == conn {
			delete(h.gatewayConns, state.channel)
			var latest *Conn
			var latestSeq int64
			for candidate, candidateState := range h.gatewayConnMeta {
				if candidateState.channel != state.channel || candidate == nil || candidate.isClosed() {
					continue
				}
				if candidateState.seq > latestSeq {
					latest = candidate
					latestSeq = candidateState.seq
				}
			}
			if latest != nil {
				h.gatewayConns[state.channel] = latest
			}
		}
	}
	h.mu.Unlock()
	h.monitorClose(conn)
}

func (h *Hub) Broadcast(eventType string, data map[string]any) {
	if h == nil {
		return
	}
	conns := h.snapshotConnections()
	for _, conn := range conns {
		conn.SendPush(eventType, data)
	}
}

func (h *Hub) CloseAll(code int, text string) {
	if h == nil {
		return
	}
	for _, conn := range h.snapshotConnections() {
		conn.close(code, text)
	}
}

func (h *Hub) snapshotConnections() []*Conn {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns := make([]*Conn, 0, len(h.conns))
	for conn := range h.conns {
		conns = append(conns, conn)
	}
	return conns
}

func (h *Hub) GatewayConnection(channelID string) (*Conn, bool) {
	channelID = strings.TrimSpace(channelID)
	if h == nil || channelID == "" {
		return nil, false
	}
	h.mu.RLock()
	conn := h.gatewayConns[channelID]
	h.mu.RUnlock()
	if conn == nil || conn.isClosed() {
		return nil, false
	}
	return conn, true
}

func (h *Hub) GatewayConnections(channelID string) []MonitorConnection {
	channelID = strings.TrimSpace(channelID)
	if h == nil || channelID == "" {
		return nil
	}
	type activeGatewayConn struct {
		conn *Conn
		seq  int64
	}
	h.mu.RLock()
	active := make([]activeGatewayConn, 0)
	for conn, state := range h.gatewayConnMeta {
		if state.channel != channelID || conn == nil || conn.isClosed() {
			continue
		}
		active = append(active, activeGatewayConn{conn: conn, seq: state.seq})
	}
	h.mu.RUnlock()
	sort.Slice(active, func(i, j int) bool {
		return active[i].seq > active[j].seq
	})
	if len(active) == 0 {
		return nil
	}

	h.monitorMu.RLock()
	defer h.monitorMu.RUnlock()
	connections := make([]MonitorConnection, 0, len(active))
	for _, item := range active {
		if item.conn == nil {
			continue
		}
		sessionID := item.conn.SessionID()
		var snapshot MonitorConnection
		if state := h.monitorConns[sessionID]; state != nil {
			snapshot = state.snapshot()
		} else {
			kind, subject, gatewayID, channel := item.conn.monitorIdentity()
			remoteAddr, userAgent, source, deviceID := item.conn.monitorClientInfo()
			snapshot = MonitorConnection{
				SessionID:   sessionID,
				Kind:        kind,
				Active:      true,
				Subject:     monitorSanitizeText(subject, monitorUserAgentMaxRunes),
				GatewayID:   monitorSanitizeText(gatewayID, monitorUserAgentMaxRunes),
				Channel:     monitorSanitizeText(channel, monitorUserAgentMaxRunes),
				Source:      monitorNormalizeSource(source),
				DeviceID:    monitorNormalizeDeviceID(deviceID),
				RemoteAddr:  monitorSanitizeRemoteAddr(remoteAddr),
				UserAgent:   monitorSanitizeText(userAgent, monitorUserAgentMaxRunes),
				ConnectedAt: 0,
				LastSeenAt:  0,
			}
		}
		details := item.conn.monitorRuntimeDetails()
		snapshot.InflightRequests = details.InflightRequests
		snapshot.ActiveStreams = details.ActiveStreams
		snapshot.WriteQueueDepth = details.WriteQueueDepth
		connections = append(connections, snapshot)
	}
	return connections
}
