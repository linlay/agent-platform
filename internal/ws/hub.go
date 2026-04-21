package ws

import "sync"

type Hub struct {
	mu    sync.RWMutex
	conns map[*Conn]struct{}
}

func NewHub() *Hub {
	return &Hub{conns: map[*Conn]struct{}{}}
}

func (h *Hub) register(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) unregister(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	delete(h.conns, conn)
	h.mu.Unlock()
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
