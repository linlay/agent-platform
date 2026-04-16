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
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for conn := range h.conns {
		conns = append(conns, conn)
	}
	h.mu.RUnlock()
	for _, conn := range conns {
		conn.SendPush(eventType, data)
	}
}
