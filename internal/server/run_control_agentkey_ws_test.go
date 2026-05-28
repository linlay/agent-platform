package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestRunControlWSRequiresAndValidatesAgentKey(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-ws-agent-check",
		ChatID:   "chat-ws-agent-check",
		AgentKey: "mock-agent",
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	tests := []struct {
		name    string
		typ     string
		payload map[string]any
		code    int
	}{
		{
			name:    "attach missing agentKey",
			typ:     "/api/attach",
			payload: map[string]any{"runId": "run-ws-agent-check"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "attach mismatched agentKey",
			typ:     "/api/attach",
			payload: map[string]any{"agentKey": "other-agent", "runId": "run-ws-agent-check"},
			code:    http.StatusForbidden,
		},
		{
			name:    "submit missing agentKey",
			typ:     "/api/submit",
			payload: map[string]any{"runId": "run-ws-agent-check", "awaitingId": "await-ws-agent-check", "params": []any{}},
			code:    http.StatusBadRequest,
		},
		{
			name:    "submit mismatched agentKey",
			typ:     "/api/submit",
			payload: map[string]any{"agentKey": "other-agent", "runId": "run-ws-agent-check", "awaitingId": "await-ws-agent-check", "params": []any{}},
			code:    http.StatusForbidden,
		},
		{
			name:    "steer missing agentKey",
			typ:     "/api/steer",
			payload: map[string]any{"runId": "run-ws-agent-check", "message": "continue"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "steer mismatched agentKey",
			typ:     "/api/steer",
			payload: map[string]any{"agentKey": "other-agent", "runId": "run-ws-agent-check", "message": "continue"},
			code:    http.StatusForbidden,
		},
		{
			name:    "interrupt missing agentKey",
			typ:     "/api/interrupt",
			payload: map[string]any{"runId": "run-ws-agent-check"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "interrupt mismatched agentKey",
			typ:     "/api/interrupt",
			payload: map[string]any{"agentKey": "other-agent", "runId": "run-ws-agent-check"},
			code:    http.StatusForbidden,
		},
		{
			name:    "access level missing agentKey",
			typ:     "/api/access-level",
			payload: map[string]any{"runId": "run-ws-agent-check", "accessLevel": "auto_approve"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "access level mismatched agentKey",
			typ:     "/api/access-level",
			payload: map[string]any{"agentKey": "other-agent", "runId": "run-ws-agent-check", "accessLevel": "auto_approve"},
			code:    http.StatusForbidden,
		},
		{
			name:    "access level invalid value",
			typ:     "/api/access-level",
			payload: map[string]any{"agentKey": "mock-agent", "runId": "run-ws-agent-check", "accessLevel": "root"},
			code:    http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqID := strings.ReplaceAll(tc.name, " ", "_")
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    tc.typ,
				ID:      reqID,
				Payload: ws.MarshalPayload(tc.payload),
			}); err != nil {
				t.Fatalf("write websocket request: %v", err)
			}
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read websocket response: %v", err)
			}
			var frame ws.ErrorFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode error frame: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != reqID || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %s", string(raw))
			}
		})
	}
}
