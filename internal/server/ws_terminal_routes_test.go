package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestWebSocketTerminalOpenInputAndExit(t *testing.T) {
	workspace := t.TempDir()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 16
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-terminal")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir coder agent: %v", err)
			}
			content := strings.Join([]string{
				"key: coder-terminal",
				"name: Coder Terminal",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(content), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/open",
		ID:    "term_open",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "coder-terminal",
			"cols":     80,
			"rows":     24,
		}),
	}); err != nil {
		t.Fatalf("write terminal open: %v", err)
	}

	openedRaw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened"
	})
	terminalID := terminalIDFromStreamFrame(t, openedRaw)
	if terminalID == "" {
		t.Fatalf("expected terminalId in opened frame: %s", string(openedRaw))
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/input",
		ID:    "term_input",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId": terminalID,
			"data":       terminalReadyInput(),
		}),
	}); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}

	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.ResponseFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameResponse && frame.ID == "term_input" && frame.Code == 0
	})
	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		return websocketStreamEventType(data) == "terminal.output" && strings.Contains(string(data), "ws-terminal-ready")
	})
	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.StreamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameStream && frame.ID == "term_open" && frame.Reason == "exit"
	})
}

func TestWebSocketTerminalRejectsUnsupportedTargets(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/open",
		ID:    "term_react",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"cols":     80,
			"rows":     24,
		}),
	}); err != nil {
		t.Fatalf("write terminal open: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.ErrorFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameError && frame.ID == "term_react"
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode error frame: %v", err)
	}
	if frame.Type != "forbidden" {
		t.Fatalf("expected forbidden, got %s", string(raw))
	}
}

func TestWebSocketTerminalUnknownSessionControlsReturnNotFound(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	requests := []ws.RequestFrame{
		{Frame: ws.FrameRequest, Type: "/api/terminal/input", ID: "input_missing", Payload: ws.MarshalPayload(map[string]any{"terminalId": "missing", "data": "x"})},
		{Frame: ws.FrameRequest, Type: "/api/terminal/resize", ID: "resize_missing", Payload: ws.MarshalPayload(map[string]any{"terminalId": "missing", "cols": 80, "rows": 24})},
		{Frame: ws.FrameRequest, Type: "/api/terminal/close", ID: "close_missing", Payload: ws.MarshalPayload(map[string]any{"terminalId": "missing"})},
	}
	for _, request := range requests {
		if err := conn.WriteJSON(request); err != nil {
			t.Fatalf("write %s: %v", request.ID, err)
		}
		raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
			var frame ws.ErrorFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				return false
			}
			return frame.Frame == ws.FrameError && frame.ID == request.ID
		})
		var frame ws.ErrorFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode %s: %v", request.ID, err)
		}
		if frame.Type != "terminal_not_found" {
			t.Fatalf("expected terminal_not_found for %s, got %s", request.ID, string(raw))
		}
	}
}

func TestOpenTerminalSessionRejectsInvalidAgentWorkspace(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeTerminalTestAgentFile(t, cfg, "coder-empty", strings.Join([]string{
				"key: coder-empty",
				"name: Empty Workspace",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
			}, "\n"))
			writeTerminalTestAgentFile(t, cfg, "coder-chat", strings.Join([]string{
				"key: coder-chat",
				"name: Chat Workspace",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: \"@chat\"",
			}, "\n"))
			writeTerminalTestAgentFile(t, cfg, "coder-sandbox", strings.Join([]string{
				"key: coder-sandbox",
				"name: Sandbox Workspace",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  environmentId: toolbox",
				"  level: run",
			}, "\n"))
		},
	})

	tests := []struct {
		name       string
		payload    terminalOpenPayload
		wantStatus int
		wantText   string
	}{
		{
			name:       "missing agent",
			payload:    terminalOpenPayload{AgentKey: "missing-agent", Cols: 80, Rows: 24},
			wantStatus: http.StatusBadRequest,
			wantText:   "agent not found",
		},
		{
			name:       "empty workspace",
			payload:    terminalOpenPayload{AgentKey: "coder-empty", Cols: 80, Rows: 24},
			wantStatus: http.StatusBadRequest,
			wantText:   "agent workspace is empty",
		},
		{
			name:       "@chat without chatId",
			payload:    terminalOpenPayload{AgentKey: "coder-chat", Cols: 80, Rows: 24},
			wantStatus: http.StatusBadRequest,
			wantText:   "chatId is required",
		},
		{
			name:       "sandbox coder",
			payload:    terminalOpenPayload{AgentKey: "coder-sandbox", Cols: 80, Rows: 24},
			wantStatus: http.StatusNotImplemented,
			wantText:   "native local CODER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, statusErr := fixture.server.openTerminalSession(tt.payload)
			if session != nil {
				session.Close("closed")
				t.Fatalf("expected no session")
			}
			if statusErr == nil {
				t.Fatalf("expected status error")
			}
			if statusErr.status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", statusErr.status, tt.wantStatus, statusErr.message)
			}
			if !strings.Contains(statusErr.message, tt.wantText) {
				t.Fatalf("message = %q, want contains %q", statusErr.message, tt.wantText)
			}
		})
	}
}

func TestResolveTerminalShellDefaultsByPlatform(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		envShell   string
		goos       string
		want       string
	}{
		{
			name:       "configured wins",
			configured: "pwsh.exe",
			envShell:   "/bin/zsh",
			goos:       "windows",
			want:       "pwsh.exe",
		},
		{
			name:     "windows defaults to powershell",
			envShell: "/bin/zsh",
			goos:     "windows",
			want:     "powershell.exe",
		},
		{
			name:     "unix uses shell env",
			envShell: "/bin/zsh",
			goos:     "darwin",
			want:     "/bin/zsh",
		},
		{
			name: "unix falls back to bash",
			goos: "linux",
			want: "/bin/bash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTerminalShellForGOOS(tt.configured, tt.envShell, tt.goos); got != tt.want {
				t.Fatalf("shell = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeTerminalTestAgentFile(t *testing.T, cfg *config.Config, agentKey string, content string) {
	t.Helper()
	agentDir := filepath.Join(cfg.Paths.AgentsDir, agentKey)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", agentKey, err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s agent: %v", agentKey, err)
	}
}

func dialTestWebSocket(t *testing.T, serverURL string) *gws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func websocketStreamEventType(data []byte) string {
	var frame ws.StreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return ""
	}
	if frame.Frame != ws.FrameStream || frame.Event == nil {
		return ""
	}
	return frame.Event.Type
}

func websocketErrorType(data []byte) string {
	var frame ws.ErrorFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return ""
	}
	if frame.Frame != ws.FrameError {
		return ""
	}
	return frame.Type
}

func terminalIDFromStreamFrame(t *testing.T, data []byte) string {
	t.Helper()
	var frame ws.StreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode stream frame: %v", err)
	}
	if frame.Event == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(frame.Event.Payload["terminalId"]))
}

func terminalReadyInput() string {
	if runtime.GOOS == "windows" {
		return "Write-Output ws-terminal-ready\r\nexit\r\n"
	}
	return "printf ws-terminal-ready\\n\nexit\n"
}
