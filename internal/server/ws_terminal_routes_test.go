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

func TestWebSocketTerminalOpen_reusesAgentTerminalAcrossChatsAndDetachReplaysOutput(t *testing.T) {
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
			writeTerminalTestAgentFile(t, cfg, "coder-terminal-shared", strings.Join([]string{
				"key: coder-terminal-shared",
				"name: Coder Terminal Shared",
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

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	openTerminalStream(t, conn, "term_open_chat_a", map[string]any{
		"agentKey":    "coder-terminal-shared",
		"chatId":      "chat-a",
		"terminalKey": "main",
		"cols":        80,
		"rows":        24,
	})
	openedA := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened"
	})
	terminalID := terminalIDFromStreamFrame(t, openedA)
	if terminalID == "" {
		t.Fatalf("expected terminalId in opened frame: %s", string(openedA))
	}
	if reused, ok := boolFieldFromStreamFrame(t, openedA, "reused"); !ok || reused {
		t.Fatalf("first open should include reused=false: %s", string(openedA))
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/input",
		ID:    "term_input_shared",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId": terminalID,
			"data":       "printf agent-terminal-shared\\n\n",
		}),
	}); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}
	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		return websocketStreamEventType(data) == "terminal.output" && strings.Contains(string(data), "agent-terminal-shared")
	})

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/detach",
		ID:    "term_detach_a",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId":      terminalID,
			"streamRequestId": "term_open_chat_a",
		}),
	}); err != nil {
		t.Fatalf("write terminal detach: %v", err)
	}
	waitForWebSocketResponse(t, conn, "term_detach_a")

	openTerminalStream(t, conn, "term_open_chat_b", map[string]any{
		"agentKey":    "coder-terminal-shared",
		"chatId":      "chat-b",
		"terminalKey": "main",
		"cols":        100,
		"rows":        30,
	})
	openedB := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		return websocketStreamEventType(data) == "terminal.opened" && strings.Contains(string(data), "term_open_chat_b")
	})
	if got := terminalIDFromStreamFrame(t, openedB); got != terminalID {
		t.Fatalf("reused terminalId = %q, want %q; frame=%s", got, terminalID, string(openedB))
	}
	if reused, ok := boolFieldFromStreamFrame(t, openedB, "reused"); !ok || !reused {
		t.Fatalf("second open should be reused: %s", string(openedB))
	}
	replay := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		return websocketStreamEventType(data) == "terminal.output" &&
			strings.Contains(string(data), "agent-terminal-shared") &&
			strings.Contains(string(data), `"replay":true`)
	})
	if !boolValueFromStreamFrame(t, replay, "replay") {
		t.Fatalf("expected replay payload: %s", string(replay))
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/input",
		ID:    "term_input_exit",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId": terminalID,
			"data":       "exit\n",
		}),
	}); err != nil {
		t.Fatalf("write terminal exit input: %v", err)
	}
	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.StreamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameStream && frame.ID == "term_open_chat_b" && frame.Reason == "exit"
	})
}

func TestWebSocketTerminalIsolatesSessionsAcrossConnections(t *testing.T) {
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
			writeTerminalTestAgentFile(t, cfg, "coder-terminal-isolated", strings.Join([]string{
				"key: coder-terminal-isolated",
				"name: Coder Terminal Isolated",
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

	connA := dialTestWebSocket(t, server.URL)
	defer connA.Close()
	waitForPushFrameType(t, connA, "connected")
	connB := dialTestWebSocket(t, server.URL)
	defer connB.Close()
	waitForPushFrameType(t, connB, "connected")

	openTerminalStream(t, connA, "term_open_owner_a", map[string]any{
		"agentKey":    "coder-terminal-isolated",
		"terminalKey": "main",
		"cols":        80,
		"rows":        24,
	})
	openedA := waitForWebSocketFrame(t, connA, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened"
	})
	terminalA := terminalIDFromStreamFrame(t, openedA)
	if terminalA == "" {
		t.Fatalf("expected first terminal id")
	}
	if reused, ok := boolFieldFromStreamFrame(t, openedA, "reused"); !ok || reused {
		t.Fatalf("first owner open should include reused=false: %s", string(openedA))
	}

	if err := connB.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/input",
		ID:    "cross_owner_input",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId": terminalA,
			"data":       "printf should-not-run\\n\n",
		}),
	}); err != nil {
		t.Fatalf("write cross-owner input: %v", err)
	}
	raw := waitForWebSocketFrame(t, connB, func(data []byte) bool {
		return websocketErrorType(data) == "terminal_not_found" && strings.Contains(string(data), "cross_owner_input")
	})
	if websocketErrorType(raw) != "terminal_not_found" {
		t.Fatalf("expected terminal_not_found, got %s", string(raw))
	}

	openTerminalStream(t, connB, "term_open_owner_b", map[string]any{
		"agentKey":    "coder-terminal-isolated",
		"terminalKey": "main",
		"cols":        80,
		"rows":        24,
	})
	openedB := waitForWebSocketFrame(t, connB, func(data []byte) bool {
		return websocketStreamEventType(data) == "terminal.opened" && strings.Contains(string(data), "term_open_owner_b")
	})
	terminalB := terminalIDFromStreamFrame(t, openedB)
	if terminalB == "" || terminalB == terminalA {
		t.Fatalf("expected isolated second terminal id, got first=%q second=%q", terminalA, terminalB)
	}
	if reused, ok := boolFieldFromStreamFrame(t, openedB, "reused"); !ok || reused {
		t.Fatalf("cross-connection open should include reused=false without shared auth subject: %s", string(openedB))
	}

	closeTerminalByID(t, connA, "term_close_owner_a", terminalA)
	closeTerminalByID(t, connB, "term_close_owner_b", terminalB)
}

func TestWebSocketTerminalDetachRequiresMatchingTerminalStream(t *testing.T) {
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
			writeTerminalTestAgentFile(t, cfg, "coder-terminal-detach", strings.Join([]string{
				"key: coder-terminal-detach",
				"name: Coder Terminal Detach",
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

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	openTerminalStream(t, conn, "term_open_detach", map[string]any{
		"agentKey":    "coder-terminal-detach",
		"terminalKey": "main",
		"cols":        80,
		"rows":        24,
	})
	opened := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened"
	})
	terminalID := terminalIDFromStreamFrame(t, opened)
	if terminalID == "" {
		t.Fatalf("expected terminal id")
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/detach",
		ID:    "term_detach_wrong",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId":      "other-terminal",
			"streamRequestId": "term_open_detach",
		}),
	}); err != nil {
		t.Fatalf("write wrong detach: %v", err)
	}
	waitForWebSocketFrame(t, conn, func(data []byte) bool {
		return websocketErrorType(data) == "invalid_request" && strings.Contains(string(data), "term_detach_wrong")
	})

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/detach",
		ID:    "term_detach_right",
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId":      terminalID,
			"streamRequestId": "term_open_detach",
		}),
	}); err != nil {
		t.Fatalf("write right detach: %v", err)
	}
	waitForWebSocketResponse(t, conn, "term_detach_right")
	closeTerminalByID(t, conn, "term_close_detach", terminalID)
}

func TestWebSocketTerminalOpensForAnyAgentModeWithDefaultWorkspace(t *testing.T) {
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
	wantCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}

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
	opened := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		if runtime.GOOS == "windows" && websocketErrorType(data) == "unsupported" {
			t.Skip("Windows ConPTY is unsupported on this host")
		}
		return websocketStreamEventType(data) == "terminal.opened" && strings.Contains(string(data), "term_react")
	})
	terminalID := terminalIDFromStreamFrame(t, opened)
	if terminalID == "" {
		t.Fatalf("expected terminal id: %s", string(opened))
	}
	if got := stringFieldFromStreamFrame(t, opened, "cwd"); got != wantCWD {
		t.Fatalf("terminal cwd = %q, want %q; frame=%s", got, wantCWD, string(opened))
	}
	closeTerminalByID(t, conn, "term_close_react", terminalID)
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

func TestOpenTerminalSessionUsesWorkspaceFallbackForAnyAgent(t *testing.T) {
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
	wantCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}

	successes := []struct {
		name    string
		payload terminalOpenPayload
	}{
		{
			name:    "empty workspace falls back to process cwd",
			payload: terminalOpenPayload{AgentKey: "coder-empty", TerminalKey: "empty", Cols: 80, Rows: 24},
		},
		{
			name:    "@chat workspace falls back to process cwd",
			payload: terminalOpenPayload{AgentKey: "coder-chat", TerminalKey: "chat", Cols: 80, Rows: 24},
		},
		{
			name:    "sandbox agent uses the same terminal path",
			payload: terminalOpenPayload{AgentKey: "coder-sandbox", TerminalKey: "sandbox", Cols: 80, Rows: 24},
		},
	}
	for _, tt := range successes {
		t.Run(tt.name, func(t *testing.T) {
			result, statusErr := fixture.server.openTerminalSession(tt.payload, "test-owner")
			if runtime.GOOS == "windows" && statusErr != nil && statusErr.status == http.StatusNotImplemented {
				t.Skip("Windows ConPTY is unsupported on this host")
			}
			if statusErr != nil {
				t.Fatalf("expected terminal session, got %d %s", statusErr.status, statusErr.message)
			}
			if result.Session == nil {
				t.Fatalf("expected terminal session")
			}
			defer fixture.server.terminals.Discard(result.Session)
			if result.Session.CWD() != wantCWD {
				t.Fatalf("cwd = %q, want %q", result.Session.CWD(), wantCWD)
			}
		})
	}

	failures := []struct {
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
	}

	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			result, statusErr := fixture.server.openTerminalSession(tt.payload, "test-owner")
			if result.Session != nil {
				result.Session.Close("closed")
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
	return stringFieldFromStreamFrame(t, data, "terminalId")
}

func stringFieldFromStreamFrame(t *testing.T, data []byte, key string) string {
	t.Helper()
	var frame ws.StreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode stream frame: %v", err)
	}
	if frame.Event == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(frame.Event.Payload[key]))
}

func boolValueFromStreamFrame(t *testing.T, data []byte, key string) bool {
	t.Helper()
	value, _ := boolFieldFromStreamFrame(t, data, key)
	return value
}

func boolFieldFromStreamFrame(t *testing.T, data []byte, key string) (bool, bool) {
	t.Helper()
	var frame ws.StreamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode stream frame: %v", err)
	}
	if frame.Event == nil {
		return false, false
	}
	value, ok := frame.Event.Payload[key].(bool)
	return value, ok
}

func openTerminalStream(t *testing.T, conn *gws.Conn, requestID string, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/terminal/open",
		ID:      requestID,
		Payload: ws.MarshalPayload(payload),
	}); err != nil {
		t.Fatalf("write terminal open %s: %v", requestID, err)
	}
}

func closeTerminalByID(t *testing.T, conn *gws.Conn, requestID string, terminalID string) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/terminal/close",
		ID:    requestID,
		Payload: ws.MarshalPayload(map[string]any{
			"terminalId": terminalID,
		}),
	}); err != nil {
		t.Fatalf("write terminal close %s: %v", requestID, err)
	}
	waitForWebSocketResponse(t, conn, requestID)
}

func waitForWebSocketResponse(t *testing.T, conn *gws.Conn, requestID string) {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame ws.ResponseFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameResponse && frame.ID == requestID
	})
	var frame ws.ResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode response %s: %v", requestID, err)
	}
	if frame.Code != 0 {
		t.Fatalf("response %s code = %d, frame=%s", requestID, frame.Code, string(raw))
	}
}

func terminalReadyInput() string {
	if runtime.GOOS == "windows" {
		return "Write-Output ws-terminal-ready\r\nexit\r\n"
	}
	return "printf ws-terminal-ready\\n\nexit\n"
}
