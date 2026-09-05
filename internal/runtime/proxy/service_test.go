package proxy

import (
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestServiceOwnsActiveRoutes(t *testing.T) {
	service := NewService()
	route := NewRoute("run-1", "chat-1", "agent-1")
	service.Register(route)
	if got, ok := service.Lookup("run-1"); !ok || got != route {
		t.Fatalf("lookup = %#v, %t", got, ok)
	}
	service.Unregister("run-1", NewRoute("run-1", "chat-1", "agent-1"))
	if _, ok := service.Lookup("run-1"); !ok {
		t.Fatal("a different route must not unregister the active route")
	}
	service.Unregister("run-1", route)
	if _, ok := service.Lookup("run-1"); ok {
		t.Fatal("route still registered")
	}
}

func TestRouteOwnsProtocolAndSendQueue(t *testing.T) {
	route := NewRoute("run-1", "chat-1", "agent-1")
	route.Protocol = config.ChannelProtocolPlatformWS
	if got := route.RequestType("interrupt"); got != "/api/interrupt" {
		t.Fatalf("request type = %q", got)
	}
	want := map[string]any{"type": "request.interrupt"}
	if !route.Send(want) {
		t.Fatal("send rejected")
	}
	select {
	case got := <-route.SendQueue:
		if got["type"] != want["type"] {
			t.Fatalf("message = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for route message")
	}
}
