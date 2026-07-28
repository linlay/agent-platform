package ws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"

	gws "github.com/gorilla/websocket"
)

func TestHubInvokeWebClientActionRoundTrip(t *testing.T) {
	hub := NewHub()
	handler := NewHandler(config.WebSocketConfig{
		WriteQueueSize: 8,
		PingInterval:   30,
	}, time.Second, hub, testAuthenticator{})
	server := httptest.NewServer(handler)
	defer server.Close()

	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?source=desktop-chat&deviceId=device-1&surfaceId=surface-1"
	client, _, err := gws.DefaultDialer.Dial(socketURL, nil)
	if err != nil {
		t.Fatalf("dial webclient websocket: %v", err)
	}
	defer client.Close()

	var connected PushFrame
	if err := client.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected push: %v", err)
	}
	if connected.Frame != FramePush || connected.Type != "connected" {
		t.Fatalf("unexpected connected frame: %#v", connected)
	}

	target := contracts.WebClientTarget{
		BoundaryKey: "device:device-1",
		SurfaceID:   "surface-1",
	}
	resultCh := make(chan contracts.WebClientActionResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		response, invokeErr := hub.InvokeWebClientAction(context.Background(), target, contracts.WebClientActionRequest{
			ID:   "wsa-1",
			Type: "webclient.sidebar.openUrl",
			Payload: map[string]any{
				"url":   "http://localhost:8088/docx/document-1",
				"title": "DOCX 文档",
			},
		})
		if invokeErr != nil {
			errCh <- invokeErr
			return
		}
		resultCh <- response
	}()

	var request RequestFrame
	if err := client.ReadJSON(&request); err != nil {
		t.Fatalf("read webclient action request: %v", err)
	}
	if request.Frame != FrameRequest || request.Type != "webclient.sidebar.openUrl" || request.ID != "wsa-1" {
		t.Fatalf("unexpected action request frame: %#v", request)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatalf("decode action payload: %v", err)
	}
	if payload["url"] != "http://localhost:8088/docx/document-1" || payload["title"] != "DOCX 文档" {
		t.Fatalf("unexpected flat action payload: %#v", payload)
	}
	if err := client.WriteJSON(ResponseFrame{
		Frame: FrameResponse,
		Type:  request.Type,
		ID:    request.ID,
		Code:  0,
		Msg:   "success",
		Data:  map[string]any{"applied": true},
	}); err != nil {
		t.Fatalf("write webclient action response: %v", err)
	}

	select {
	case invokeErr := <-errCh:
		t.Fatalf("invoke webclient action: %v", invokeErr)
	case response := <-resultCh:
		if response.Frame != FrameResponse || response.Type != request.Type || response.ID != request.ID || response.Code == nil || *response.Code != 0 {
			t.Fatalf("unexpected action response: %#v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webclient action result")
	}
}

func TestConnWebClientTargetDoesNotDependOnSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "webclient", source: "WebClient"},
		{name: "desktop chat", source: "desktop-chat"},
		{name: "desktop copilot", source: "desktop-copilot"},
		{name: "other client", source: "other-client"},
		{name: "empty", source: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 4}, time.Second, AuthSession{
				Context:  context.Background(),
				Subject:  "user-1",
				DeviceID: "device-1",
			})
			conn.SetClientMetadata(test.source, "device-1")
			conn.SetClientSurfaceID("surface-1")

			target := conn.WebClientTarget()
			if target.SessionID != conn.SessionID() ||
				target.BoundaryKey != "subject:user-1\x00device:device-1" ||
				target.Subject != "user-1" ||
				target.SurfaceID != "surface-1" {
				t.Fatalf("source %q changed webclient target: %#v", test.source, target)
			}
		})
	}
}
