package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type terminalStatusSessionForTest struct {
	TerminalID  string
	AgentKey    string
	TerminalKey string
	Status      string
}

func TestWebSocketTerminalStatusStreamPublishesSessionSnapshot(t *testing.T) {
	workspace := t.TempDir()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 32
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeTerminalTestAgentFile(t, cfg, "coder-terminal-status", strings.Join([]string{
				"key: coder-terminal-status",
				"name: Coder Terminal Status",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n"))
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn := dialTestWebSocketWithQuery(t, server.URL, "deviceId=terminal-status-device")
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/terminal/status",
		ID:      "term_status_watch",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write terminal status stream: %v", err)
	}
	waitForTerminalStatusFrame(t, conn, "term_status_watch", func(sessions []terminalStatusSessionForTest) bool {
		return len(sessions) == 0
	})

	openTerminalStream(t, conn, "term_open_status", map[string]any{
		"agentKey":    "coder-terminal-status",
		"terminalKey": "main",
		"cols":        80,
		"rows":        24,
	})
	opened := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened" && strings.Contains(string(data), "term_open_status")
	})
	terminalID := terminalIDFromStreamFrame(t, opened)
	if terminalID == "" {
		t.Fatalf("expected terminal id in opened frame")
	}

	waitForTerminalStatusFrame(t, conn, "term_status_watch", func(sessions []terminalStatusSessionForTest) bool {
		for _, session := range sessions {
			if session.TerminalID == terminalID && session.AgentKey == "coder-terminal-status" && session.TerminalKey == "main" {
				return true
			}
		}
		return false
	})

	closeTerminalByID(t, conn, "term_close_status", terminalID)
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/status/detach",
		ID:    "term_status_detach",
		Payload: ws.MarshalPayload(map[string]any{
			"streamRequestId": "term_status_watch",
		}),
	}); err != nil {
		t.Fatalf("write terminal status detach: %v", err)
	}
	waitForWebSocketResponse(t, conn, "term_status_detach")
}

func waitForTerminalStatusFrame(t *testing.T, conn *gws.Conn, requestID string, match func([]terminalStatusSessionForTest) bool) []terminalStatusSessionForTest {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.StreamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		if frame.Frame != ws.FrameStream || frame.ID != requestID || frame.Event == nil || frame.Event.Type != "terminal.status" {
			return false
		}
		sessions := terminalStatusSessionsFromFrame(t, data)
		return match(sessions)
	})
	return terminalStatusSessionsFromFrame(t, raw)
}

func terminalStatusSessionsFromFrame(t *testing.T, data []byte) []terminalStatusSessionForTest {
	t.Helper()
	var frame ws.StreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode terminal status frame: %v", err)
	}
	if frame.Event == nil {
		t.Fatalf("terminal status frame missing event: %s", string(data))
	}
	rawSessions, ok := frame.Event.Payload["sessions"].([]any)
	if !ok {
		t.Fatalf("terminal status frame missing sessions: %s", string(data))
	}
	sessions := make([]terminalStatusSessionForTest, 0, len(rawSessions))
	for _, raw := range rawSessions {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("terminal status session must be object: %#v", raw)
		}
		sessions = append(sessions, terminalStatusSessionForTest{
			TerminalID:  strings.TrimSpace(stringValue(item["terminalId"])),
			AgentKey:    strings.TrimSpace(stringValue(item["agentKey"])),
			TerminalKey: strings.TrimSpace(stringValue(item["terminalKey"])),
			Status:      strings.TrimSpace(stringValue(item["status"])),
		})
	}
	return sessions
}
