package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWebSocketUpgradeAcceptsValidTokenThroughStatusRecorder(t *testing.T) {
	var privateKey *rsa.PrivateKey
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		setupRuntime: func(root string, cfg *config.Config) {
			var publicKeyPath string
			privateKey, publicKeyPath = writeTestJWTKeyPair(t, root)
			cfg.Auth = config.AuthConfig{
				Enabled:            true,
				LocalPublicKeyFile: publicKeyPath,
				Issuer:             "zenmind-local",
			}
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	token := mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "zenmind-local",
		"exp": float64(4102444800),
	})
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + url.QueryEscape(token)
	conn, resp, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		status := 0
		body := ""
		if resp != nil {
			status = resp.StatusCode
			if resp.Body != nil {
				data, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					body = readErr.Error()
				} else {
					body = string(data)
				}
				resp.Body.Close()
			}
		}
		t.Fatalf("expected websocket handshake to succeed, got err=%v status=%d body=%q", err, status, body)
	}
	defer conn.Close()
}

func TestWebSocketRequestFramesAreLogged(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})

	var buffer lockedLogBuffer
	originalWriter := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(originalWriter)

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	readConnectedPush(t, conn)

	for _, tc := range []struct {
		frameType string
		id        string
	}{
		{frameType: "/api/agents", id: "req_agents"},
		{frameType: "/api/teams", id: "req_teams"},
		{frameType: "/api/chats", id: "req_chats"},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{
			Frame:   ws.FrameRequest,
			Type:    tc.frameType,
			ID:      tc.id,
			Payload: ws.MarshalPayload(map[string]any{}),
		}); err != nil {
			t.Fatalf("write %s request: %v", tc.frameType, err)
		}
		var response ws.ResponseFrame
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatalf("read %s response: %v", tc.frameType, err)
		}
		if response.Frame != ws.FrameResponse || response.Type != tc.frameType || response.ID != tc.id || response.Code != 0 {
			t.Fatalf("unexpected %s response: %#v", tc.frameType, response)
		}
	}

	waitForLogText(t, &buffer, "WS /api/agents id=req_agents (arrived)")
	waitForLogText(t, &buffer, "WS /api/agents id=req_agents -> done")
	waitForLogText(t, &buffer, "WS /api/teams id=req_teams (arrived)")
	waitForLogText(t, &buffer, "WS /api/teams id=req_teams -> done")
	waitForLogText(t, &buffer, "WS /api/chats id=req_chats (arrived)")
	waitForLogText(t, &buffer, "WS /api/chats id=req_chats -> done")
	waitForLogText(t, &buffer, `"category":"ws.request"`)
	waitForLogText(t, &buffer, `"sessionId":"ws_`)
}

func waitForLogText(t *testing.T, buffer *lockedLogBuffer, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected log to contain %q, got %q", needle, buffer.String())
}

func TestWebSocketQueryAvailabilityAlwaysAllowsAgentQuery(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query/availability",
		ID:    "req_query_availability",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"chatId":   "chat-next",
		}),
	}); err != nil {
		t.Fatalf("write availability request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read availability response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/query/availability" || frame.Code != 0 {
		t.Fatalf("unexpected availability response: %#v", frame)
	}
	data, err := marshalResponseData[api.QueryAvailabilityResponse](frame.Data)
	if err != nil {
		t.Fatalf("decode availability data: %v", err)
	}
	if !data.CanQuery || data.Code != "ok" || data.AgentKey != "mock-agent" || data.ChatID != "chat-next" {
		t.Fatalf("expected availability ok, got %#v", data)
	}
}

func TestWebSocketChatReturnsActiveRunConflict(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		sandbox:       &recordingSandbox{},
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	if _, _, err := fixture.chats.EnsureChat("chat_ws_conflict", "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_1",
		ChatID:   "chat_ws_conflict",
		AgentKey: "mock-agent",
	})
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_2",
		ChatID:   "chat_ws_conflict",
		AgentKey: "mock-agent",
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/chat",
		ID:    "req_chat_conflict",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId": "chat_ws_conflict",
		}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var connected ws.PushFrame
	if err := json.Unmarshal(raw, &connected); err != nil {
		t.Fatalf("decode initial frame: %v", err)
	}
	if connected.Frame != ws.FramePush {
		t.Fatalf("expected initial push frame, got %s", string(raw))
	}

	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error frame: %v", err)
	}
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.Type != "active_run_conflict" || frame.Code != http.StatusConflict {
		t.Fatalf("unexpected websocket error frame: %s", string(raw))
	}
	if frame.Msg != activeRunConflictMessage {
		t.Fatalf("unexpected websocket error message: %#v", frame)
	}
	assertActiveRunConflictInfo(t, decodeWSChatErrorInfo(t, frame.Data), activeRunConflictMessage, "chat_ws_conflict", "run_ws_1", "run_ws_2")
}

func TestWebSocketAgentsKeepsChatWithActiveRunConflictError(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		sandbox:       &recordingSandbox{},
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	if _, _, err := fixture.chats.EnsureChat("chat_ws_agents_conflict", "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_agents_1",
		ChatID:   "chat_ws_agents_conflict",
		AgentKey: "mock-agent",
	})
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_agents_2",
		ChatID:   "chat_ws_agents_conflict",
		AgentKey: "mock-agent",
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agents",
		ID:    "req_agents_conflict",
		Payload: ws.MarshalPayload(map[string]any{
			"includeChats": 1,
		}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("decode websocket frame metadata: %v", err)
		}
		return meta.ID == "req_agents_conflict" && (meta.Frame == ws.FrameResponse || meta.Frame == ws.FrameError)
	})
	var frame struct {
		Frame string             `json:"frame"`
		Type  string             `json:"type"`
		ID    string             `json:"id"`
		Code  int                `json:"code"`
		Msg   string             `json:"msg"`
		Data  []api.AgentSummary `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket agents frame: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/agents" || frame.Code != 0 {
		t.Fatalf("expected successful agents response, got %s", string(raw))
	}
	var conflicted *api.ChatSummaryResponse
	for _, agent := range frame.Data {
		for i := range agent.Chats {
			if agent.Chats[i].ChatID == "chat_ws_agents_conflict" {
				conflicted = &agent.Chats[i]
				break
			}
		}
	}
	if conflicted == nil {
		t.Fatalf("expected conflicted chat in agents response: %#v", frame.Data)
	}
	if conflicted.ActiveRun != nil {
		t.Fatalf("conflicted chat should not expose activeRun, got %#v", conflicted.ActiveRun)
	}
	if conflicted.Error == nil {
		t.Fatalf("expected chat error on conflicted chat, got %#v", conflicted)
	}
	assertActiveRunConflictInfo(t, *conflicted.Error, activeRunConflictMessage, "chat_ws_agents_conflict", "run_ws_agents_1", "run_ws_agents_2")
}

func TestWebSocketPushesChatReadAfterMarkRead(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	if _, _, err := fixture.chats.EnsureChat("chat_ws_read", "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := fixture.chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat_ws_read",
		RunID:           "loyw3v28",
		AssistantText:   "answer",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("persist run completion: %v", err)
	}

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	reqBody := bytes.NewBufferString(`{"chatId":"chat_ws_read","runId":"loyw3v28"}`)
	resp, err := http.Post(server.URL+"/api/read", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post read: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/read, got %d: %s", resp.StatusCode, readBodyString(t, resp.Body))
	}

	frame := waitForPushFrameType(t, conn, "chat.read")
	if frame.Frame != ws.FramePush {
		t.Fatalf("expected push frame, got %#v", frame)
	}
	data := pushFrameDataMap(t, frame)
	if data["chatId"] != "chat_ws_read" {
		t.Fatalf("expected chatId chat_ws_read, got %#v", data)
	}
	if data["agentKey"] != "mock-agent" {
		t.Fatalf("expected agentKey mock-agent, got %#v", data)
	}
	if data["lastRunId"] != "loyw3v28" {
		t.Fatalf("expected lastRunId loyw3v28, got %#v", data)
	}
	if data["readRunId"] != "loyw3v28" {
		t.Fatalf("expected readRunId loyw3v28, got %#v", data)
	}
	if got, ok := data["agentUnreadCount"].(float64); !ok || got != 0 {
		t.Fatalf("expected agentUnreadCount 0, got %#v", data)
	}
	if got, ok := data["readAt"].(float64); !ok || got <= 0 {
		t.Fatalf("expected positive readAt, got %#v", data)
	}
}

func TestWebSocketPushesChatUnreadAfterRunCompletion(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runtime test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	reqBody := bytes.NewBufferString(`{"chatId":"chat_ws_unread","runId":"loyw3v2s","agentKey":"mock-agent","message":"hello unread"}`)
	resp, err := http.Post(server.URL+"/api/query", "application/json", reqBody)
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/query, got %d: %s", resp.StatusCode, readBodyString(t, resp.Body))
	}
	_, _ = io.ReadAll(resp.Body)

	frame := waitForPushFrameType(t, conn, "chat.unread")
	if frame.Frame != ws.FramePush {
		t.Fatalf("expected push frame, got %#v", frame)
	}
	data := pushFrameDataMap(t, frame)
	if data["chatId"] != "chat_ws_unread" {
		t.Fatalf("expected chatId chat_ws_unread, got %#v", data)
	}
	if data["agentKey"] != "mock-agent" {
		t.Fatalf("expected agentKey mock-agent, got %#v", data)
	}
	if data["lastRunId"] != "loyw3v2s" {
		t.Fatalf("expected lastRunId loyw3v2s, got %#v", data)
	}
	if data["readRunId"] != "" {
		t.Fatalf("expected empty readRunId for fresh unread chat, got %#v", data)
	}
	if got, ok := data["agentUnreadCount"].(float64); !ok || got != 1 {
		t.Fatalf("expected agentUnreadCount 1, got %#v", data)
	}
	if got, ok := data["readAt"].(float64); !ok || got != 0 {
		t.Fatalf("expected readAt 0 for unread chat, got %#v", data)
	}
}

func TestWebSocketRunCompletionPushOrdering(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query",
		ID:    "req_query_order",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId":   "chat_ws_order",
			"runId":    "run_ws_order",
			"agentKey": "mock-agent",
			"message":  "hello ordering",
		}),
	}); err != nil {
		t.Fatalf("write websocket query: %v", err)
	}

	sequence := make([]string, 0, 4)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(sequence) < 4 {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame  string `json:"frame"`
			ID     string `json:"id"`
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame: %v", err)
		}
		switch {
		case meta.Frame == ws.FrameStream && meta.ID == "req_query_order" && meta.Reason != "":
			sequence = append(sequence, "stream.done")
		case meta.Frame == ws.FramePush && (meta.Type == "run.finished" || meta.Type == "chat.unread" || meta.Type == "chat.updated"):
			sequence = append(sequence, meta.Type)
		}
	}

	want := []string{"stream.done", "run.finished", "chat.unread", "chat.updated"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("unexpected websocket completion order: got %v want %v", sequence, want)
	}
}

func TestWebSocketProxyRunCompletionPushOrdering(t *testing.T) {
	workspace := t.TempDir()
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Fatalf("expected upstream websocket path /ws, got %s", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade upstream websocket: %v", err)
		}
		defer conn.Close()
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read upstream websocket frame: %v", err)
		}
		if err := conn.WriteJSON(map[string]any{
			"event": map[string]any{
				"seq":   1,
				"type":  "content.delta",
				"runId": "upstream-run",
				"delta": "proxy hello",
			},
		}); err != nil {
			t.Fatalf("write upstream websocket delta: %v", err)
		}
		if err := conn.WriteJSON(map[string]any{
			"event": map[string]any{
				"seq":   2,
				"type":  "run.complete",
				"runId": "upstream-run",
			},
		}); err != nil {
			t.Fatalf("write upstream websocket completion: %v", err)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"role: proxy test agent",
				"description: proxy websocket completion ordering test agent",
				"mode: PROXY",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
			})
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query",
		ID:    "req_proxy_query_order",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId":   "chat_proxy_ws_order",
			"runId":    "run_proxy_ws_order",
			"agentKey": "mock-agent",
			"message":  "hello proxy ordering",
		}),
	}); err != nil {
		t.Fatalf("write websocket proxy query: %v", err)
	}

	sequence := make([]string, 0, 4)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(sequence) < 4 {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame  string `json:"frame"`
			ID     string `json:"id"`
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame: %v", err)
		}
		switch {
		case meta.Frame == ws.FrameStream && meta.ID == "req_proxy_query_order" && meta.Reason != "":
			sequence = append(sequence, "stream.done")
		case meta.Frame == ws.FramePush && (meta.Type == "run.finished" || meta.Type == "chat.unread" || meta.Type == "chat.updated"):
			sequence = append(sequence, meta.Type)
		}
	}

	want := []string{"stream.done", "run.finished", "chat.unread", "chat.updated"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("unexpected proxy websocket completion order: got %v want %v", sequence, want)
	}
}

func TestWebSocketRunStreamClosesDuringShutdown(t *testing.T) {
	hub := ws.NewHub()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: hub,
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	runs := fixture.runs.(*contracts.InMemoryRunManager)
	runID := "run_ws_shutdown"
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    runID,
		ChatID:   "chat_ws_shutdown",
		AgentKey: "mock-agent",
	})

	server := newLoopbackServer(t, fixture.server)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if _, raw, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read initial push: %v", err)
	} else {
		var connected ws.PushFrame
		if err := json.Unmarshal(raw, &connected); err != nil {
			t.Fatalf("decode initial frame: %v", err)
		}
		if connected.Frame != ws.FramePush || connected.Type != "connected" {
			t.Fatalf("unexpected initial websocket frame: %s", string(raw))
		}
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/attach",
		ID:    "req_shutdown_stream",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"runId":    runID,
		}),
	}); err != nil {
		t.Fatalf("write run stream request: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		status, ok := runs.RunStatus(runID)
		if ok && status.ObserverCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("observer was not attached before shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- server.server.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server shutdown timed out")
	}

	hub.CloseAll(gws.CloseNormalClosure, "server shutting down")

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatalf("expected websocket to close after hub shutdown")
	}
	if !gws.IsCloseError(err, gws.CloseNormalClosure) && !gws.IsUnexpectedCloseError(err) {
		t.Fatalf("expected websocket close error, got %v", err)
	}

	status, ok := runs.RunStatus(runID)
	if !ok {
		t.Fatalf("expected run status to remain available")
	}
	if status.ObserverCount != 0 {
		t.Fatalf("expected observer to detach after shutdown, got %d", status.ObserverCount)
	}
}

func TestWebSocketDetachReleasesRunObserverWithoutFinishingRun(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})

	runs := fixture.runs.(*contracts.InMemoryRunManager)
	runID := "run_ws_detach"
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    runID,
		ChatID:   "chat_ws_detach",
		AgentKey: "mock-agent",
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/attach",
		ID:    "req_attach_detach",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"runId":    runID,
		}),
	}); err != nil {
		t.Fatalf("write attach request: %v", err)
	}
	waitForObserverCount(t, runs, runID, 1, 2*time.Second)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/detach",
		ID:    "req_detach",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"runId":    runID,
			"reason":   "chat_switch",
		}),
	}); err != nil {
		t.Fatalf("write detach request: %v", err)
	}

	detachResp, terminal := waitForWebSocketDetachFrames(t, conn, "req_detach", "req_attach_detach")
	if !detachResp.Accepted || detachResp.Status != "detached" || detachResp.RunID != runID {
		t.Fatalf("unexpected detach response %#v", detachResp)
	}
	if detachResp.StreamRequestID != "req_attach_detach" || detachResp.StreamID == "" {
		t.Fatalf("expected detach response to identify stream, got %#v", detachResp)
	}
	if terminal.Reason != "detached" || terminal.ID != "req_attach_detach" || terminal.StreamID != detachResp.StreamID {
		t.Fatalf("unexpected detached terminal frame %#v", terminal)
	}
	waitForObserverCount(t, runs, runID, 0, 2*time.Second)
	status, ok := runs.RunStatus(runID)
	if !ok {
		t.Fatalf("expected run status after detach")
	}
	if status.CompletedAt != 0 {
		t.Fatalf("detach should not finish run, got status %#v", status)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/detach",
		ID:    "req_detach_again",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"runId":    runID,
		}),
	}); err != nil {
		t.Fatalf("write second detach request: %v", err)
	}
	notObserving := waitForWebSocketResponseData[api.DetachResponse](t, conn, "req_detach_again")
	if notObserving.Accepted || notObserving.Status != "not_observing" || notObserving.RunID != runID {
		t.Fatalf("unexpected second detach response %#v", notObserving)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/attach",
		ID:    "req_attach_again",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey": "mock-agent",
			"runId":    runID,
		}),
	}); err != nil {
		t.Fatalf("write reattach request: %v", err)
	}
	waitForObserverCount(t, runs, runID, 1, 2*time.Second)
}

func TestWebSocketPushAwaitingAskAndAnswerSyncPendingChatSummary(t *testing.T) {
	flow := startAwaitingPushQuestionFlow(t, nil)
	defer flow.conn.Close()
	defer flow.resp.Body.Close()
	defer flow.server.Close()

	awaitAsk := waitForPushFrameType(t, flow.conn, "awaiting.asking")
	awaitAskData := pushFrameDataMap(t, awaitAsk)
	if awaitAskData["chatId"] != flow.chatID {
		t.Fatalf("expected chatId=%s in awaiting.asking push, got %#v", flow.chatID, awaitAskData)
	}
	if awaitAskData["runId"] != flow.runID {
		t.Fatalf("expected runId=%s in awaiting.asking push, got %#v", flow.runID, awaitAskData)
	}
	if awaitAskData["agentKey"] != "mock-agent" {
		t.Fatalf("expected agentKey in awaiting.asking push, got %#v", awaitAskData)
	}
	if awaitAskData["awaitingId"] != flow.awaitingID || awaitAskData["mode"] != "question" {
		t.Fatalf("unexpected awaiting.asking push payload %#v", awaitAskData)
	}
	if timeout, ok := awaitAskData["timeout"].(float64); !ok || timeout <= 0 {
		t.Fatalf("expected positive timeout in awaiting.asking push, got %#v", awaitAskData)
	}
	if createdAt, ok := awaitAskData["createdAt"].(float64); !ok || createdAt <= 0 {
		t.Fatalf("expected createdAt in awaiting.asking push, got %#v", awaitAskData)
	}

	summaries := loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary, got %#v", summaries)
	}
	if summaries[0].Awaiting == nil {
		t.Fatalf("expected awaiting in chat summary, got %#v", summaries[0])
	}
	if summaries[0].Awaiting.AwaitingID != flow.awaitingID || summaries[0].Awaiting.RunID != flow.runID || summaries[0].Awaiting.Mode != "question" || summaries[0].Awaiting.Status != "awaiting" || summaries[0].Awaiting.CreatedAt <= 0 {
		t.Fatalf("unexpected awaiting summary %#v", summaries[0].Awaiting)
	}
	persistedAsk, err := flow.fixture.chats.LoadAwaitingAsk(flow.chatID, flow.awaitingID)
	if err != nil {
		t.Fatalf("load persisted awaiting ask: %v", err)
	}
	if persistedAsk == nil || persistedAsk.AwaitingID != flow.awaitingID || persistedAsk.RunID != flow.runID || persistedAsk.Mode != "question" {
		t.Fatalf("expected awaiting.ask to be immediately persisted, got %#v", persistedAsk)
	}
	rawChatSummaries := loadChatSummariesRawForTest(t, flow.fixture.server)
	if !strings.Contains(rawChatSummaries, `"awaiting"`) {
		t.Fatalf("expected /api/chats response to serialize awaiting, got %s", rawChatSummaries)
	}
	if !strings.Contains(rawChatSummaries, `"status":"awaiting"`) {
		t.Fatalf("expected /api/chats response to serialize awaiting status, got %s", rawChatSummaries)
	}
	if strings.Contains(rawChatSummaries, `"pendingAwaiting"`) {
		t.Fatalf("did not expect /api/chats response to serialize pendingAwaiting, got %s", rawChatSummaries)
	}

	submitRec := httptest.NewRecorder()
	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+flow.runID+`","awaitingId":"`+flow.awaitingID+`","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	flow.fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answered")
	awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
	if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
		t.Fatalf("unexpected awaiting.answered push identity %#v", awaitAnswerData)
	}
	if awaitAnswerData["mode"] != "question" || awaitAnswerData["status"] != "answered" {
		t.Fatalf("unexpected awaiting.answered push payload %#v", awaitAnswerData)
	}
	if _, exists := awaitAnswerData["errorCode"]; exists {
		t.Fatalf("did not expect errorCode on answered awaiting.answered push, got %#v", awaitAnswerData)
	}
	if resolvedAt, ok := awaitAnswerData["resolvedAt"].(float64); !ok || resolvedAt <= 0 {
		t.Fatalf("expected resolvedAt in awaiting.answered push, got %#v", awaitAnswerData)
	}

	drainAwaitingPushQuestionStream(t, flow.reader, flow.streamBody)

	summaries = loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary after answer, got %#v", summaries)
	}
	if summaries[0].Awaiting != nil {
		t.Fatalf("expected awaiting to clear after answer, got %#v", summaries[0].Awaiting)
	}
}

func TestWebSocketPushAwaitingAnswerEmitsErrorStatuses(t *testing.T) {
	testCases := []struct {
		name      string
		configure func(*config.Config)
		act       func(t *testing.T, flow *awaitingPushQuestionFlow, awaitAskData map[string]any)
		errorCode string
	}{
		{
			name: "user dismissed",
			act: func(t *testing.T, flow *awaitingPushQuestionFlow, awaitAskData map[string]any) {
				t.Helper()
				submitRec := httptest.NewRecorder()
				submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+flow.runID+`","awaitingId":"`+flow.awaitingID+`","params":[]}`))
				submitReq.Header.Set("Content-Type", "application/json")
				flow.fixture.server.ServeHTTP(submitRec, submitReq)
				if submitRec.Code != http.StatusOK {
					t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
				}
			},
			errorCode: "user_dismissed",
		},
		{
			name: "timeout",
			configure: func(cfg *config.Config) {
				cfg.Defaults.Budget.Hitl.Timeout = 1
				cfg.Defaults.Budget.Tool.Timeout = 1
			},
			act: func(t *testing.T, flow *awaitingPushQuestionFlow, awaitAskData map[string]any) {
				t.Helper()
				if timeout, ok := awaitAskData["timeout"].(float64); !ok || timeout != 1 {
					t.Fatalf("expected awaiting.asking timeout 1, got %#v", awaitAskData)
				}
			},
			errorCode: "timeout",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			flow := startAwaitingPushQuestionFlow(t, tc.configure)
			defer flow.conn.Close()
			defer flow.resp.Body.Close()
			defer flow.server.Close()

			awaitAsk := waitForPushFrameType(t, flow.conn, "awaiting.asking")
			tc.act(t, &flow, pushFrameDataMap(t, awaitAsk))

			awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answered")
			awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
			if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
				t.Fatalf("unexpected awaiting.answered push identity %#v", awaitAnswerData)
			}
			if awaitAnswerData["status"] != "error" || awaitAnswerData["errorCode"] != tc.errorCode {
				t.Fatalf("unexpected awaiting.answered error payload %#v", awaitAnswerData)
			}

			drainAwaitingPushQuestionStream(t, flow.reader, flow.streamBody)
		})
	}
}

func TestWebSocketPushAwaitingAnswerRunInterruptedClearsPendingChatSummary(t *testing.T) {
	flow := startAwaitingPushQuestionFlow(t, nil)
	defer flow.conn.Close()
	defer flow.resp.Body.Close()
	defer flow.server.Close()

	waitForPushFrameType(t, flow.conn, "awaiting.asking")

	summaries := loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 || summaries[0].Awaiting == nil {
		t.Fatalf("expected awaiting before interrupt, got %#v", summaries)
	}

	interruptRec := httptest.NewRecorder()
	interruptReq := httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+flow.runID+`"}`))
	interruptReq.Header.Set("Content-Type", "application/json")
	flow.fixture.server.ServeHTTP(interruptRec, interruptReq)
	if interruptRec.Code != http.StatusOK {
		t.Fatalf("interrupt expected 200, got %d: %s", interruptRec.Code, interruptRec.Body.String())
	}

	awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answered")
	awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
	if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
		t.Fatalf("unexpected interrupt awaiting.answered push identity %#v", awaitAnswerData)
	}
	if awaitAnswerData["status"] != "error" || awaitAnswerData["errorCode"] != "run_interrupted" {
		t.Fatalf("unexpected interrupt awaiting.answered push payload %#v", awaitAnswerData)
	}

	drainAwaitingPushQuestionStream(t, flow.reader, flow.streamBody)

	summaries = loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary after interrupt, got %#v", summaries)
	}
	if summaries[0].Awaiting != nil {
		t.Fatalf("expected awaiting cleared after interrupt, got %#v", summaries[0].Awaiting)
	}
}

func TestWebSocketQueryDebugVisibilityFollowsStreamConfig(t *testing.T) {
	testCases := []struct {
		name         string
		includeDebug bool
		wantDebug    bool
	}{
		{name: "hidden by default", includeDebug: false, wantDebug: false},
		{name: "visible when enabled", includeDebug: true, wantDebug: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w,
					`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
					`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
					`[DONE]`,
				)
			}, testFixtureOptions{
				notifications: ws.NewHub(),
				configure: func(cfg *config.Config) {
					cfg.WebSocket.WriteQueueSize = 8
					cfg.WebSocket.PingInterval = 30000
					cfg.Stream.DebugEventsEnabled = tc.includeDebug
				},
			})

			server := httptest.NewServer(fixture.server)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer conn.Close()

			if err := conn.WriteJSON(ws.RequestFrame{
				Frame: ws.FrameRequest,
				Type:  "/api/query",
				ID:    "req_query_debug",
				Payload: ws.MarshalPayload(map[string]any{
					"message": "websocket debug",
				}),
			}); err != nil {
				t.Fatalf("write websocket query: %v", err)
			}

			eventTypes := collectWebSocketStreamEventTypes(t, conn, "req_query_debug")
			if tc.wantDebug {
				assertStringSliceContains(t, eventTypes, "debug.preCall", "debug.postCall")
				return
			}
			assertStringSliceExcludes(t, eventTypes, "debug.preCall", "debug.postCall")
		})
	}
}

func TestWebSocketQueryToolPayloadVisibilityFollowsStreamConfig(t *testing.T) {
	testCases := []struct {
		name           string
		includePayload bool
		wantPayload    bool
	}{
		{name: "hidden when disabled", includePayload: false, wantPayload: false},
		{name: "visible when enabled", includePayload: true, wantPayload: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var providerCallCount atomic.Int32
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				call := providerCallCount.Add(1)
				switch call {
				case 1:
					writeProviderSSE(t, w,
						`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"datetime","arguments":"{"}}]}}]}`,
						`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
						`[DONE]`,
					)
				case 2:
					writeProviderSSE(t, w,
						`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
						`[DONE]`,
					)
				default:
					t.Fatalf("unexpected provider call %d", call)
				}
			}, testFixtureOptions{
				notifications: ws.NewHub(),
				configure: func(cfg *config.Config) {
					cfg.WebSocket.WriteQueueSize = 8
					cfg.WebSocket.PingInterval = 30000
					cfg.Stream.IncludeToolPayloadEvents = tc.includePayload
				},
			})

			server := httptest.NewServer(fixture.server)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer conn.Close()

			if err := conn.WriteJSON(ws.RequestFrame{
				Frame: ws.FrameRequest,
				Type:  "/api/query",
				ID:    "req_query_payload",
				Payload: ws.MarshalPayload(map[string]any{
					"message": "websocket tool payload",
				}),
			}); err != nil {
				t.Fatalf("write websocket query: %v", err)
			}

			eventTypes := collectWebSocketStreamEventTypes(t, conn, "req_query_payload")
			assertStringSliceContains(t, eventTypes, "tool.start", "tool.end")
			if tc.wantPayload {
				assertStringSliceContains(t, eventTypes, "tool.args", "tool.result")
				return
			}
			assertStringSliceExcludes(t, eventTypes, "tool.args", "tool.result")
		})
	}
}

func TestWebSocketCoderPlanningEmitsLifecycleEvents(t *testing.T) {
	var providerCallCount atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		switch call := providerCallCount.Add(1); call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan", "planning_write", map[string]any{
					"markdown": `# WebSocket Planning

## Summary
Plan should stream over websocket.

## Public Events And Storage
- Emit planning delta events

## Implementation Changes
- Confirm and execute the plan

## Interfaces
- Use websocket planning events

## Test Plan
- Assert websocket event types

## Assumptions
- The user approves the plan
`,
				}),
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"execution completed"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		case 3:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"summary completed"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 16
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(root string, cfg *config.Config) {
			workspace := filepath.Join(root, "workspace")
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-ws")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir coder agent: %v", err)
			}
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: coder-ws",
				"name: Coder WS",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	requestID := "req_query_planning"
	chatID := "chat_ws_planning"
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query",
		ID:    requestID,
		Payload: ws.MarshalPayload(map[string]any{
			"message":      "please plan over websocket",
			"agentKey":     "coder-ws",
			"chatId":       chatID,
			"planningMode": true,
		}),
	}); err != nil {
		t.Fatalf("write websocket query: %v", err)
	}

	eventTypes, runID, awaitingID := collectWebSocketEventsUntilPlanningApproval(t, conn, requestID)
	assertStringSliceContains(t, eventTypes, "planning.delta", "awaiting.ask")
	assertStringSliceExcludes(t, eventTypes, "planning.start", "planning.end", "planning.snapshot")
	if got := countStrings(eventTypes, "planning.delta"); got <= 1 {
		t.Fatalf("expected multiple websocket planning.delta events, got %d in %#v", got, eventTypes)
	}
	planningFile := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID, chat.ToolRootDirName, chat.ToolPlansDirName, runID+"_planning_1.md")
	planningBytes, readPlanningErr := os.ReadFile(planningFile)
	if readPlanningErr != nil {
		t.Fatalf("expected websocket planning markdown file before confirmation: %v", readPlanningErr)
	}
	if planningText := string(planningBytes); !strings.Contains(planningText, "# WebSocket Planning") ||
		!strings.Contains(planningText, "## Assumptions") {
		t.Fatalf("unexpected websocket planning markdown file:\n%s", planningText)
	}

	submitBody := `{"agentKey":"coder-ws","runId":"` + runID + `","awaitingId":"` + awaitingID + `","params":[{"id":"confirm","decision":"approve"}]}`
	submitRec, err := http.Post(server.URL+"/api/submit", "application/json", bytes.NewBufferString(submitBody))
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}
	defer submitRec.Body.Close()
	if submitRec.StatusCode != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.StatusCode, readBodyString(t, submitRec.Body))
	}

	eventTypes = append(eventTypes, collectWebSocketStreamEventTypes(t, conn, requestID)...)
	assertStringSliceContains(t, eventTypes, "run.complete")
	assertStringSliceExcludes(t, eventTypes, "planning.start", "planning.end", "planning.snapshot")
}

type awaitingPushQuestionFlow struct {
	fixture    testFixture
	server     *loopbackServer
	conn       *gws.Conn
	resp       *http.Response
	reader     *bufio.Reader
	streamBody *strings.Builder
	chatID     string
	runID      string
	awaitingID string
}

func startAwaitingPushQuestionFlow(t *testing.T, configure func(*config.Config)) awaitingPushQuestionFlow {
	t.Helper()

	var providerCallCount atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue with the request\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox:       &recordingSandbox{},
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
			if configure != nil {
				configure(cfg)
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
				"key: mock-agent",
				"name: Mock Agent",
				"role: 测试代理",
				"description: test agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - ask_user_question",
				"mode: REACT",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write helper agent config: %v", err)
			}
		},
	})

	server := newLoopbackServer(t, fixture.server)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial websocket: %v", err)
	}
	connected := waitForPushFrameType(t, conn, "connected")
	if connected.Frame != ws.FramePush {
		conn.Close()
		server.Close()
		t.Fatalf("unexpected initial websocket frame %#v", connected)
	}

	chatID := "chat_ws_awaiting"
	resp, err := http.Post(server.URL+"/api/query", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"please confirm first"}`))
	if err != nil {
		conn.Close()
		server.Close()
		t.Fatalf("post query: %v", err)
	}

	flow := awaitingPushQuestionFlow{
		fixture:    fixture,
		server:     server,
		conn:       conn,
		resp:       resp,
		reader:     bufio.NewReader(resp.Body),
		streamBody: &strings.Builder{},
		chatID:     chatID,
	}
	for {
		line, readErr := flow.reader.ReadString('\n')
		flow.streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				flow.runID, _ = payload["runId"].(string)
				flow.awaitingID, _ = payload["awaitingId"].(string)
				break
			}
		}
		if readErr != nil {
			flow.conn.Close()
			flow.resp.Body.Close()
			flow.server.Close()
			t.Fatalf("read query stream before awaiting.ask: %v", readErr)
		}
	}
	if flow.runID == "" || flow.awaitingID == "" {
		flow.conn.Close()
		flow.resp.Body.Close()
		flow.server.Close()
		t.Fatalf("expected awaiting.ask identifiers, got stream %s", flow.streamBody.String())
	}
	assertEventOrder(t, flow.streamBody.String(), "tool.start", "tool.args", "tool.end", "awaiting.ask")
	return flow
}

func drainAwaitingPushQuestionStream(t *testing.T, reader *bufio.Reader, body *strings.Builder) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		body.WriteString(line)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("read query stream: %v", err)
		}
	}
}

func waitForPushFrameType(t *testing.T, conn *gws.Conn, eventType string) ws.PushFrame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var frame ws.PushFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode websocket push frame: %v", err)
		}
		if frame.Frame == ws.FramePush && frame.Type == eventType {
			return frame
		}
	}
	t.Fatalf("timed out waiting for websocket push frame %s", eventType)
	return ws.PushFrame{}
}

func pushFrameDataMap(t *testing.T, frame ws.PushFrame) map[string]any {
	t.Helper()
	data, ok := frame.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected push frame data object, got %#v", frame.Data)
	}
	return data
}

func readBodyString(t *testing.T, body io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

func loadChatSummariesForTest(t *testing.T, handler http.Handler) []api.ChatSummaryResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list chats expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	return response.Data
}

func loadChatSummariesRawForTest(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list chats expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func collectWebSocketEventsUntilPlanningApproval(t *testing.T, conn *gws.Conn, requestID string) ([]string, string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	types := make([]string, 0)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame: %v", err)
		}
		if meta.Frame != ws.FrameStream || meta.ID != requestID {
			continue
		}
		var frame ws.StreamFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode websocket stream frame: %v", err)
		}
		if frame.Event == nil {
			continue
		}
		types = append(types, frame.Event.Type)
		if frame.Event.Type == "awaiting.ask" && frame.Event.String("mode") == "plan" {
			if timeout, ok := frame.Event.Payload["timeout"].(float64); !ok || timeout != 0 {
				t.Fatalf("expected websocket planning confirmation timeout 0, got %#v", frame.Event.Payload)
			}
			return types, frame.Event.String("runId"), frame.Event.String("awaitingId")
		}
	}
	t.Fatalf("timed out waiting for websocket planning confirmation for %s", requestID)
	return nil, "", ""
}

func collectWebSocketStreamEventTypes(t *testing.T, conn *gws.Conn, requestID string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	types := make([]string, 0)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame  string `json:"frame"`
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame: %v", err)
		}
		if meta.Frame != ws.FrameStream || meta.ID != requestID {
			continue
		}
		var frame ws.StreamFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode websocket stream frame: %v", err)
		}
		if frame.Event != nil {
			types = append(types, frame.Event.Type)
		}
		if frame.Reason != "" {
			return types
		}
	}
	t.Fatalf("timed out waiting for websocket stream completion for %s", requestID)
	return nil
}

func countStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func decodeWSChatErrorInfo(t *testing.T, data any) api.ChatErrorInfo {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal websocket error data: %v", err)
	}
	var info api.ChatErrorInfo
	if err := json.Unmarshal(encoded, &info); err != nil {
		t.Fatalf("decode websocket chat error data: %v", err)
	}
	return info
}

func assertActiveRunConflictInfo(t *testing.T, info api.ChatErrorInfo, message string, chatID string, runIDs ...string) {
	t.Helper()
	if info.Code != activeRunConflictCode || info.Message != message || info.ChatID != chatID {
		t.Fatalf("unexpected chat error info: %#v", info)
	}
	if len(info.RunIDs) != len(runIDs) {
		t.Fatalf("expected run ids %v, got %v", runIDs, info.RunIDs)
	}
	counts := map[string]int{}
	for _, runID := range info.RunIDs {
		counts[runID]++
	}
	for _, runID := range runIDs {
		counts[runID]--
	}
	for runID, count := range counts {
		if count != 0 {
			t.Fatalf("expected run ids %v, got %v; mismatch %s=%d", runIDs, info.RunIDs, runID, count)
		}
	}
}

func waitForWebSocketDetachFrames(t *testing.T, conn *gws.Conn, detachID string, streamRequestID string) (api.DetachResponse, ws.StreamFrame) {
	t.Helper()
	var (
		response    api.DetachResponse
		gotResponse bool
		terminal    ws.StreamFrame
		gotTerminal bool
		deadline    = time.Now().Add(5 * time.Second)
	)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame metadata: %v", err)
		}
		switch {
		case meta.Frame == ws.FrameResponse && meta.ID == detachID:
			var frame ws.ResponseFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode detach response frame: %v", err)
			}
			if frame.Code != 0 {
				t.Fatalf("expected successful detach response, got %#v", frame)
			}
			var decodeErr error
			response, decodeErr = marshalResponseData[api.DetachResponse](frame.Data)
			if decodeErr != nil {
				t.Fatalf("decode detach response data: %v", decodeErr)
			}
			gotResponse = true
		case meta.Frame == ws.FrameStream && meta.ID == streamRequestID:
			var frame ws.StreamFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode detached terminal frame: %v", err)
			}
			if frame.Reason != "" {
				terminal = frame
				gotTerminal = true
			}
		}
		if gotResponse && gotTerminal {
			return response, terminal
		}
	}
	t.Fatalf("timed out waiting for detach response and terminal stream frame")
	return api.DetachResponse{}, ws.StreamFrame{}
}

func waitForWebSocketResponseData[T any](t *testing.T, conn *gws.Conn, requestID string) T {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame metadata: %v", err)
		}
		if meta.Frame != ws.FrameResponse || meta.ID != requestID {
			continue
		}
		var frame ws.ResponseFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode websocket response frame: %v", err)
		}
		if frame.Code != 0 {
			t.Fatalf("expected successful websocket response, got %#v", frame)
		}
		data, decodeErr := marshalResponseData[T](frame.Data)
		if decodeErr != nil {
			t.Fatalf("decode websocket response data: %v", decodeErr)
		}
		return data
	}
	t.Fatalf("timed out waiting for websocket response %s", requestID)
	var zero T
	return zero
}

func waitForWebSocketFrame(t *testing.T, conn *gws.Conn, match func([]byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		if match(raw) {
			return raw
		}
	}
	t.Fatalf("timed out waiting for websocket frame")
	return nil
}
