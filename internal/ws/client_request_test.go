package ws

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"

	gws "github.com/gorilla/websocket"
)

func TestHubInvokeClientRequestStreamsUntilTerminalResponse(t *testing.T) {
	hub, server, client, target := newClientRequestTestConnection(t)
	defer server.Close()
	defer client.Close()

	frames := make(chan contracts.ClientResponseFrame, 3)
	errCh := make(chan error, 1)
	go func() {
		errCh <- hub.InvokeClientRequest(context.Background(), target, contracts.ClientRequest{
			ID: "desktop-stream-1", Type: "desktop.action.call", Payload: map[string]any{"action": "desktop.theme.get"},
		}, func(frame contracts.ClientResponseFrame) error {
			frames <- frame
			return nil
		})
	}()

	var request RequestFrame
	if err := client.ReadJSON(&request); err != nil {
		t.Fatalf("read reverse request: %v", err)
	}
	if request.Frame != FrameRequest || request.ID != "desktop-stream-1" || request.Type != "desktop.action.call" {
		t.Fatalf("unexpected reverse request: %#v", request)
	}
	if err := client.WriteJSON(map[string]any{
		"frame": "stream", "id": request.ID, "streamId": "stream-1",
		"event": map[string]any{"seq": 1, "type": "desktop.bridge.response.delta", "timestamp": 1771888000000, "encoding": "base64", "chunk": "e30="},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(ResponseFrame{Frame: FrameResponse, Type: request.Type, ID: request.ID, Code: 0, Msg: "success", Data: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("invoke reverse request: %v", err)
	}
	close(frames)
	var got []contracts.ClientResponseFrame
	for frame := range frames {
		got = append(got, frame)
	}
	if len(got) != 2 || got[0].Frame != FrameStream || got[0].StreamID != "stream-1" || got[1].Frame != FrameResponse {
		t.Fatalf("unexpected delivered frames: %#v", got)
	}
}

func TestHubInvokeClientRequestTimeoutCancelsOnceAndDiscardsLateFrame(t *testing.T) {
	hub, server, client, target := newClientRequestTestConnection(t)
	defer server.Close()
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- hub.InvokeClientRequest(ctx, target, contracts.ClientRequest{
			ID: "desktop-timeout-1", Type: "desktop.action.call", Payload: map[string]any{"action": "desktop.theme.set"},
		}, nil)
	}()

	var request RequestFrame
	if err := client.ReadJSON(&request); err != nil {
		t.Fatalf("read reverse request: %v", err)
	}
	var cancelFrame PushFrame
	if err := client.ReadJSON(&cancelFrame); err != nil {
		t.Fatalf("read cancellation push: %v", err)
	}
	if cancelFrame.Frame != FramePush || cancelFrame.Type != "desktop.bridge.cancel" {
		t.Fatalf("unexpected cancellation frame: %#v", cancelFrame)
	}
	cancelData, _ := cancelFrame.Data.(map[string]any)
	if cancelData["requestId"] != request.ID {
		t.Fatalf("unexpected cancellation request id: %#v", cancelFrame.Data)
	}
	if err := <-errCh; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}

	if err := client.WriteJSON(ResponseFrame{Frame: FrameResponse, Type: request.Type, ID: request.ID, Code: 0, Data: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var unexpected map[string]any
	if err := client.ReadJSON(&unexpected); err == nil {
		t.Fatalf("late response produced an unexpected frame: %#v", unexpected)
	}
}

func newClientRequestTestConnection(t *testing.T) (*Hub, *httptest.Server, *gws.Conn, contracts.ClientTarget) {
	t.Helper()
	hub := NewHub()
	handler := NewHandler(config.WebSocketConfig{WriteQueueSize: 8, PingInterval: 30}, hub, testAuthenticator{})
	server := httptest.NewServer(handler)
	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?source=desktop-main&deviceId=device-1&surfaceId=surface-1"
	client, _, err := gws.DefaultDialer.Dial(socketURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial websocket: %v", err)
	}
	var connected PushFrame
	if err := client.ReadJSON(&connected); err != nil {
		client.Close()
		server.Close()
		t.Fatalf("read connected push: %v", err)
	}
	return hub, server, client, contracts.ClientTarget{BoundaryKey: "device:device-1", SurfaceID: "surface-1"}
}
