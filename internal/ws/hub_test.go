package ws

import (
	"testing"

	gws "github.com/gorilla/websocket"
)

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()
	conn := &Conn{
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	hub.register(conn)

	hub.Broadcast("catalog.updated", map[string]any{"reason": "agents"})

	msg := <-conn.writeQueue
	push, ok := msg.frame.(PushFrame)
	if !ok {
		t.Fatalf("expected push frame, got %T", msg.frame)
	}
	if push.Type != "catalog.updated" {
		t.Fatalf("unexpected push type: %#v", push)
	}
}

func TestHubCloseAllClosesRegisteredConnections(t *testing.T) {
	hub := NewHub()
	first := &Conn{
		hub:        hub,
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	second := &Conn{
		hub:        hub,
		writeQueue: make(chan outboundMessage, 1),
		closed:     make(chan struct{}),
	}
	hub.register(first)
	hub.register(second)

	hub.CloseAll(gws.CloseNormalClosure, "server shutting down")

	if !first.isClosed() {
		t.Fatalf("expected first connection to close")
	}
	if !second.isClosed() {
		t.Fatalf("expected second connection to close")
	}
	if got := len(hub.conns); got != 0 {
		t.Fatalf("expected hub to be empty after CloseAll, got %d", got)
	}
}
