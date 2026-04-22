package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/reload"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/stream"
	"agent-platform-runner-go/internal/tools"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

var disallowedPersistedEventTypes = []string{
	"reasoning.start",
	"reasoning.delta",
	"reasoning.end",
	"content.start",
	"content.delta",
	"content.end",
	"tool.start",
	"tool.args",
	"tool.end",
	"action.start",
	"action.args",
	"action.end",
}

func TestStatusRecorderExposesFlusherWhenUnderlyingWriterSupportsIt(t *testing.T) {
	base := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	flusher, ok := any(rec).(http.Flusher)
	if !ok {
		t.Fatalf("expected statusRecorder to implement http.Flusher")
	}

	flusher.Flush()
	if !base.Flushed {
		t.Fatalf("expected Flush to be forwarded to underlying response writer")
	}
}

func TestStatusRecorderExposesHijackerWhenUnderlyingWriterSupportsIt(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	base := &hijackableResponseWriter{
		header: make(http.Header),
		conn:   serverConn,
		rw:     bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)),
	}
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	hijacker, ok := any(rec).(http.Hijacker)
	if !ok {
		t.Fatalf("expected statusRecorder to implement http.Hijacker")
	}

	gotConn, gotRW, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	if gotConn != base.conn {
		t.Fatalf("expected hijacked conn to match underlying conn")
	}
	if gotRW != base.rw {
		t.Fatalf("expected hijacked read writer to match underlying read writer")
	}
}

func TestStatusRecorderHijackReturnsErrorWhenUnderlyingWriterDoesNotSupportIt(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}

	_, _, err := rec.Hijack()
	if err == nil {
		t.Fatalf("expected hijack to fail when underlying writer does not implement http.Hijacker")
	}
	if !strings.Contains(err.Error(), "underlying ResponseWriter does not implement http.Hijacker") {
		t.Fatalf("unexpected hijack error: %v", err)
	}
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
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
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

func TestWebSocketChatReturnsActiveRunConflict(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		sandbox:       &recordingSandbox{},
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})

	if _, _, err := fixture.chats.EnsureChat("chat_ws_conflict", "mock-runner", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	runs := fixture.runs.(*runctl.InMemoryRunManager)
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_1",
		ChatID:   "chat_ws_conflict",
		AgentKey: "mock-runner",
	})
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_ws_2",
		ChatID:   "chat_ws_conflict",
		AgentKey: "mock-runner",
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
}

func TestWebSocketPushesChatReadAfterMarkRead(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})

	if _, _, err := fixture.chats.EnsureChat("chat_ws_read", "mock-runner", "", "hello"); err != nil {
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
	if data["agentKey"] != "mock-runner" {
		t.Fatalf("expected agentKey mock-runner, got %#v", data)
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
			`{"choices":[{"delta":{"content":"Go runner test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
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

	reqBody := bytes.NewBufferString(`{"chatId":"chat_ws_unread","runId":"loyw3v2s","agentKey":"mock-runner","message":"hello unread"}`)
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
	if data["agentKey"] != "mock-runner" {
		t.Fatalf("expected agentKey mock-runner, got %#v", data)
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
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingIntervalMs = 30000
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
			"agentKey": "mock-runner",
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

func TestWebSocketRunStreamClosesDuringShutdown(t *testing.T) {
	hub := ws.NewHub()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: hub,
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})

	runs := fixture.runs.(*runctl.InMemoryRunManager)
	runID := "run_ws_shutdown"
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    runID,
		ChatID:   "chat_ws_shutdown",
		AgentKey: "mock-runner",
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
			"runId": runID,
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

func TestWebSocketPushAwaitingAskAndAnswerSyncPendingChatSummary(t *testing.T) {
	flow := startAwaitingPushQuestionFlow(t, nil)
	defer flow.conn.Close()
	defer flow.resp.Body.Close()
	defer flow.server.Close()

	awaitAsk := waitForPushFrameType(t, flow.conn, "awaiting.ask")
	awaitAskData := pushFrameDataMap(t, awaitAsk)
	if awaitAskData["chatId"] != flow.chatID {
		t.Fatalf("expected chatId=%s in awaiting.ask push, got %#v", flow.chatID, awaitAskData)
	}
	if awaitAskData["runId"] != flow.runID {
		t.Fatalf("expected runId=%s in awaiting.ask push, got %#v", flow.runID, awaitAskData)
	}
	if awaitAskData["agentKey"] != "mock-runner" {
		t.Fatalf("expected agentKey in awaiting.ask push, got %#v", awaitAskData)
	}
	if awaitAskData["awaitingId"] != flow.awaitingID || awaitAskData["mode"] != "question" {
		t.Fatalf("unexpected awaiting.ask push payload %#v", awaitAskData)
	}
	if timeout, ok := awaitAskData["timeout"].(float64); !ok || timeout <= 0 {
		t.Fatalf("expected positive timeout in awaiting.ask push, got %#v", awaitAskData)
	}
	if createdAt, ok := awaitAskData["createdAt"].(float64); !ok || createdAt <= 0 {
		t.Fatalf("expected createdAt in awaiting.ask push, got %#v", awaitAskData)
	}

	summaries := loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary, got %#v", summaries)
	}
	if summaries[0].PendingAwaiting == nil {
		t.Fatalf("expected pendingAwaiting in chat summary, got %#v", summaries[0])
	}
	if summaries[0].PendingAwaiting.AwaitingID != flow.awaitingID || summaries[0].PendingAwaiting.RunID != flow.runID || summaries[0].PendingAwaiting.Mode != "question" || summaries[0].PendingAwaiting.CreatedAt <= 0 {
		t.Fatalf("unexpected pendingAwaiting summary %#v", summaries[0].PendingAwaiting)
	}

	submitRec := httptest.NewRecorder()
	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+flow.runID+`","awaitingId":"`+flow.awaitingID+`","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	flow.fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answer")
	awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
	if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
		t.Fatalf("unexpected awaiting.answer push identity %#v", awaitAnswerData)
	}
	if awaitAnswerData["mode"] != "question" || awaitAnswerData["status"] != "answered" {
		t.Fatalf("unexpected awaiting.answer push payload %#v", awaitAnswerData)
	}
	if _, exists := awaitAnswerData["errorCode"]; exists {
		t.Fatalf("did not expect errorCode on answered awaiting.answer push, got %#v", awaitAnswerData)
	}
	if resolvedAt, ok := awaitAnswerData["resolvedAt"].(float64); !ok || resolvedAt <= 0 {
		t.Fatalf("expected resolvedAt in awaiting.answer push, got %#v", awaitAnswerData)
	}

	drainAwaitingPushQuestionStream(t, flow.reader, flow.streamBody)

	summaries = loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary after answer, got %#v", summaries)
	}
	if summaries[0].PendingAwaiting != nil {
		t.Fatalf("expected pendingAwaiting to clear after answer, got %#v", summaries[0].PendingAwaiting)
	}
}

func TestWebSocketPushAwaitingAnswerEmitsErrorStatuses(t *testing.T) {
	testCases := []struct {
		name      string
		configure func(*config.Config)
		act       func(t *testing.T, flow *awaitingPushQuestionFlow)
		errorCode string
	}{
		{
			name: "user dismissed",
			act: func(t *testing.T, flow *awaitingPushQuestionFlow) {
				t.Helper()
				submitRec := httptest.NewRecorder()
				submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+flow.runID+`","awaitingId":"`+flow.awaitingID+`","params":[]}`))
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
				cfg.Defaults.Budget.Tool.TimeoutMs = 20
			},
			act: func(t *testing.T, flow *awaitingPushQuestionFlow) {
				t.Helper()
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

			waitForPushFrameType(t, flow.conn, "awaiting.ask")
			tc.act(t, &flow)

			awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answer")
			awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
			if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
				t.Fatalf("unexpected awaiting.answer push identity %#v", awaitAnswerData)
			}
			if awaitAnswerData["status"] != "error" || awaitAnswerData["errorCode"] != tc.errorCode {
				t.Fatalf("unexpected awaiting.answer error payload %#v", awaitAnswerData)
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

	waitForPushFrameType(t, flow.conn, "awaiting.ask")

	summaries := loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 || summaries[0].PendingAwaiting == nil {
		t.Fatalf("expected pendingAwaiting before interrupt, got %#v", summaries)
	}

	interruptRec := httptest.NewRecorder()
	interruptReq := httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"runId":"`+flow.runID+`"}`))
	interruptReq.Header.Set("Content-Type", "application/json")
	flow.fixture.server.ServeHTTP(interruptRec, interruptReq)
	if interruptRec.Code != http.StatusOK {
		t.Fatalf("interrupt expected 200, got %d: %s", interruptRec.Code, interruptRec.Body.String())
	}

	awaitAnswer := waitForPushFrameType(t, flow.conn, "awaiting.answer")
	awaitAnswerData := pushFrameDataMap(t, awaitAnswer)
	if awaitAnswerData["chatId"] != flow.chatID || awaitAnswerData["runId"] != flow.runID || awaitAnswerData["awaitingId"] != flow.awaitingID {
		t.Fatalf("unexpected interrupt awaiting.answer push identity %#v", awaitAnswerData)
	}
	if awaitAnswerData["status"] != "error" || awaitAnswerData["errorCode"] != "run_interrupted" {
		t.Fatalf("unexpected interrupt awaiting.answer push payload %#v", awaitAnswerData)
	}

	drainAwaitingPushQuestionStream(t, flow.reader, flow.streamBody)

	summaries = loadChatSummariesForTest(t, flow.fixture.server)
	if len(summaries) != 1 {
		t.Fatalf("expected one chat summary after interrupt, got %#v", summaries)
	}
	if summaries[0].PendingAwaiting != nil {
		t.Fatalf("expected pendingAwaiting cleared after interrupt, got %#v", summaries[0].PendingAwaiting)
	}
}

func TestWebSocketQueryDebugVisibilityFollowsSSEConfig(t *testing.T) {
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
					cfg.WebSocket.Enabled = true
					cfg.WebSocket.WriteQueueSize = 8
					cfg.WebSocket.PingIntervalMs = 30000
					cfg.SSE.IncludeDebugEvents = tc.includeDebug
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

type hijackableResponseWriter struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (w *hijackableResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *hijackableResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *hijackableResponseWriter) WriteHeader(status int) {}

func (w *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.rw, nil
}

func TestQuerySSEPersistsChatHistory(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	body := bytes.NewBufferString(`{"message":"元素碳的简介，100字","agentKey":"mock-runner"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("expected sse content type, got %q", got)
	}
	bodyText := rec.Body.String()
	if !strings.Contains(bodyText, `"type":"request.query"`) {
		t.Fatalf("expected request.query event, got %s", bodyText)
	}
	if strings.Contains(bodyText, `.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", bodyText)
	}
	if !strings.Contains(bodyText, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", bodyText)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, bodyText)
	assertSSEEventOrder(t, bodyText, "request.query", "chat.start", "run.start")

	chatsReq := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, chatsReq)

	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}
	chatID := chatsResp.Data[0].ChatID
	assertUUIDLike(t, chatID)

	chatReq := httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil)
	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, chatReq)

	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if len(chatResp.Data.Events) < 4 {
		t.Fatalf("expected persisted events, got %#v", chatResp.Data.Events)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"content.snapshot",
		"run.complete",
	)
	assertBodyContainsOrderedEvent(t, chatRec.Body.String(), `"type":"request.query"`, []string{
		`"seq":`,
		`"type":"request.query"`,
		`"requestId":`,
		`"runId":`,
		`"chatId":`,
		`"timestamp":`,
	})
	if len(chatResp.Data.RawMessages) != 2 {
		t.Fatalf("expected 2 raw messages, got %#v", chatResp.Data.RawMessages)
	}
}

func TestQueryUsesProvidedRunIDAndPersistsItEverywhere(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server
	runID := "loyw3v28"

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"reuse run id","runId":"`+runID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	messages := decodeSSEMessages(t, body)
	if len(messages) < 3 {
		t.Fatalf("expected bootstrap messages, got %#v", messages)
	}
	if messages[0]["type"] != "request.query" || messages[0]["runId"] != runID {
		t.Fatalf("expected request.query to carry provided run id, got %#v", messages[0])
	}
	if messages[2]["type"] != "run.start" || messages[2]["runId"] != runID {
		t.Fatalf("expected run.start to carry provided run id, got %#v", messages[2])
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 || chatsResp.Data[0].LastRunID != runID {
		t.Fatalf("expected summary lastRunId=%s, got %#v", runID, chatsResp.Data)
	}

	chatReq := httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID+"&includeRawMessages=true", nil)
	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, chatReq)
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	foundRequest := false
	for _, event := range chatResp.Data.Events {
		if event.Type != "request.query" {
			continue
		}
		foundRequest = true
		if got := event.String("runId"); got != runID {
			t.Fatalf("expected persisted request.query run id, got %#v", event)
		}
	}
	if !foundRequest {
		t.Fatalf("expected persisted request.query event, got %#v", chatResp.Data.Events)
	}
	for _, message := range chatResp.Data.RawMessages {
		if got := message["runId"]; got != runID {
			t.Fatalf("expected raw message runId=%s, got %#v", runID, message)
		}
	}
}

func TestQueryGeneratesBase36RunIDWhenMissing(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"generate run id"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	messages := decodeSSEMessages(t, rec.Body.String())
	if len(messages) < 3 {
		t.Fatalf("expected bootstrap messages, got %#v", messages)
	}
	runID, _ := messages[2]["runId"].(string)
	if runID == "" || strings.HasPrefix(runID, "run_") {
		t.Fatalf("expected new base36 run id, got %q", runID)
	}
	if millis, ok := chat.ParseRunIDMillis(runID); !ok || millis <= 0 {
		t.Fatalf("expected generated run id to parse as epoch millis, got %q millis=%d ok=%v", runID, millis, ok)
	}
}

func TestUploadAndResourceRoundTrip(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello world")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("requestId", "req_upload"); err != nil {
		t.Fatalf("write requestId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	assertUUIDLike(t, response.Data.ChatID)
	resourceReq := httptest.NewRequest(http.MethodGet, response.Data.Upload.URL, nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 resource, got %d", resourceRec.Code)
	}
	if got := resourceRec.Body.String(); got != "hello world" {
		t.Fatalf("unexpected resource content: %q", got)
	}

	matches, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*", "notes.txt"))
	if err != nil {
		t.Fatalf("glob upload path: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected uploaded file under %s, got %v", fixture.cfg.Paths.ChatsDir, matches)
	}
}

func TestRememberEndpointReturnsStoredMemory(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	queryReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"记住这个答案"}`))
	queryReq.Header.Set("Content-Type", "application/json")
	queryRec := httptest.NewRecorder()
	server.ServeHTTP(queryRec, queryReq)

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))

	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	chatID := chatsResp.Data[0].ChatID

	rememberReq := httptest.NewRequest(http.MethodPost, "/api/remember", bytes.NewBufferString(`{"requestId":"req_remember","chatId":"`+chatID+`"}`))
	rememberReq.Header.Set("Content-Type", "application/json")
	rememberRec := httptest.NewRecorder()
	server.ServeHTTP(rememberRec, rememberReq)

	if rememberRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rememberRec.Code, rememberRec.Body.String())
	}
	var rememberResp api.ApiResponse[api.RememberResponse]
	if err := json.Unmarshal(rememberRec.Body.Bytes(), &rememberResp); err != nil {
		t.Fatalf("decode remember response: %v", err)
	}
	if !rememberResp.Data.Accepted {
		t.Fatalf("expected remember accepted, got %#v", rememberResp.Data)
	}
	if rememberResp.Data.MemoryCount != 1 {
		t.Fatalf("expected one memory item, got %#v", rememberResp.Data)
	}
	if !strings.HasPrefix(rememberResp.Data.MemoryPath, fixture.cfg.Paths.MemoryDir+string(os.PathSeparator)) {
		t.Fatalf("expected memory path under %s, got %s", fixture.cfg.Paths.MemoryDir, rememberResp.Data.MemoryPath)
	}
}

func TestChatSnapshotDeduplicatesChatStartAcrossMultipleQueries(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	firstReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"first turn"}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first query expected 200, got %d: %s", firstRec.Code, firstRec.Body.String())
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat after first query, got %#v", chatsResp.Data)
	}
	chatID := chatsResp.Data[0].ChatID

	secondReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"second turn"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second query expected 200, got %d: %s", secondRec.Code, secondRec.Body.String())
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
	if chatRec.Code != http.StatusOK {
		t.Fatalf("chat detail expected 200, got %d: %s", chatRec.Code, chatRec.Body.String())
	}

	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}

	chatStartCount := 0
	runStartCount := 0
	prevSeq := int64(0)
	for _, event := range chatResp.Data.Events {
		eventType := event.Type
		switch eventType {
		case "chat.start":
			chatStartCount++
		case "run.start":
			runStartCount++
		}
		seq := event.Seq
		if seq != prevSeq+1 {
			t.Fatalf("expected contiguous seq values, got prev=%d current=%d events=%#v", prevSeq, seq, chatResp.Data.Events)
		}
		prevSeq = seq
	}
	if chatStartCount != 1 {
		t.Fatalf("expected one chat.start in snapshot, got %d events=%#v", chatStartCount, chatResp.Data.Events)
	}
	if runStartCount != 2 {
		t.Fatalf("expected two run.start events, got %d events=%#v", runStartCount, chatResp.Data.Events)
	}
	if len(chatResp.Data.Events) != 13 {
		t.Fatalf("expected 13 persisted events for two turns, got %d events=%#v", len(chatResp.Data.Events), chatResp.Data.Events)
	}
	if len(chatResp.Data.RawMessages) != 4 {
		t.Fatalf("expected four raw messages for two turns, got %#v", chatResp.Data.RawMessages)
	}
}

func TestServeHTTPLogsArrivalBeforeCompletion(t *testing.T) {
	fixture := newTestFixture(t)

	var buffer bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&buffer)
	defer log.SetOutput(originalWriter)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	logText := buffer.String()
	arrived := strings.Index(logText, "GET /api/agents (arrived)")
	completed := strings.Index(logText, "GET /api/agents -> 200")
	if arrived < 0 {
		t.Fatalf("expected arrival log, got %q", logText)
	}
	if completed < 0 {
		t.Fatalf("expected completion log, got %q", logText)
	}
	if arrived > completed {
		t.Fatalf("expected arrival log before completion log, got %q", logText)
	}
}

func TestAgentEndpointReturnsDetail(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=mock-runner", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if response.Data.Key != "mock-runner" {
		t.Fatalf("expected mock-runner key, got %#v", response.Data)
	}
	if response.Data.Model != "mock-model-id" {
		t.Fatalf("expected resolved model id, got %#v", response.Data)
	}
	if response.Data.Mode != "REACT" {
		t.Fatalf("expected REACT mode, got %#v", response.Data)
	}
	wantWonders := []string{
		"帮我演示提问式确认",
		"帮我演示 Bash HITL 审批确认\n并说明用户接下来会看到什么",
	}
	if !reflect.DeepEqual(response.Data.Wonders, wantWonders) {
		t.Fatalf("expected wonders in detail response, got %#v", response.Data.Wonders)
	}
	if len(response.Data.Tools) != 6 ||
		response.Data.Tools[0] != "_datetime_" ||
		response.Data.Tools[1] != "_ask_user_question_" ||
		response.Data.Tools[2] != "_bash_" ||
		response.Data.Tools[3] != "_memory_write_" ||
		response.Data.Tools[4] != "_memory_read_" ||
		response.Data.Tools[5] != "_memory_search_" {
		t.Fatalf("expected tools in detail response, got %#v", response.Data.Tools)
	}
	if len(response.Data.Skills) != 1 || response.Data.Skills[0] != "mock-skill" {
		t.Fatalf("expected skills in detail response, got %#v", response.Data.Skills)
	}
	if len(response.Data.Controls) != 1 || response.Data.Controls[0]["key"] != "tone" {
		t.Fatalf("expected controls in detail response, got %#v", response.Data.Controls)
	}
	if response.Data.Meta["modelKey"] != "mock-model" {
		t.Fatalf("expected modelKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["providerKey"] != "mock" {
		t.Fatalf("expected providerKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["protocol"] != "OPENAI" {
		t.Fatalf("expected protocol meta, got %#v", response.Data.Meta)
	}
	modelKeys, ok := response.Data.Meta["modelKeys"].([]any)
	if !ok || len(modelKeys) != 1 || modelKeys[0] != "mock-model" {
		t.Fatalf("expected modelKeys meta, got %#v", response.Data.Meta["modelKeys"])
	}
	perAgentSkills, ok := response.Data.Meta["perAgentSkills"].([]any)
	if !ok || len(perAgentSkills) != 1 || perAgentSkills[0] != "mock-skill" {
		t.Fatalf("expected perAgentSkills meta, got %#v", response.Data.Meta["perAgentSkills"])
	}
	sandbox, ok := response.Data.Meta["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox meta, got %#v", response.Data.Meta)
	}
	if sandbox["level"] != "RUN" {
		t.Fatalf("expected sandbox level RUN, got %#v", sandbox["level"])
	}
	if _, exists := sandbox["env"]; exists {
		t.Fatalf("expected sandbox env to stay private, got %#v", sandbox)
	}
	extraMounts, ok := sandbox["extraMounts"].([]any)
	if !ok || len(extraMounts) != 1 {
		t.Fatalf("expected sandbox extraMounts, got %#v", sandbox)
	}
	firstMount, ok := extraMounts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first sandbox mount map, got %#v", extraMounts[0])
	}
	if _, exists := firstMount["source"]; !exists || firstMount["source"] != nil {
		t.Fatalf("expected sandbox mount source=null, got %#v", firstMount)
	}
	if firstMount["destination"] != "/skills" {
		t.Fatalf("expected sandbox mount destination /skills, got %#v", firstMount)
	}
}

func TestAgentEndpointRequiresAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestToolEndpointReturnsCanonicalJavaBuiltinSchemas(t *testing.T) {
	fixture := newTestFixture(t)

	for _, tc := range []struct {
		toolName         string
		requiredProperty string
	}{
		{toolName: "_memory_read_", requiredProperty: "sort"},
		{toolName: "_datetime_", requiredProperty: "timezone"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tool?toolName="+tc.toolName, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", tc.toolName, rec.Code, rec.Body.String())
		}

		var response api.ApiResponse[api.ToolDetailResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode tool response for %s: %v", tc.toolName, err)
		}
		if response.Data.Name != tc.toolName {
			t.Fatalf("expected tool %s, got %#v", tc.toolName, response.Data)
		}
		properties, _ := response.Data.Parameters["properties"].(map[string]any)
		if _, ok := properties[tc.requiredProperty]; !ok {
			t.Fatalf("expected property %s in %s schema, got %#v", tc.requiredProperty, tc.toolName, response.Data.Parameters)
		}
	}
}

func TestAgentEndpointRejectsBlankAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=%20%20%20", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEndpointReturnsNotFoundForUnknownAgent(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=missing-agent", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogEndpoints(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	for _, path := range []string{"/api/agents", "/api/agent?agentKey=mock-runner", "/api/teams", "/api/skills", "/api/tools", "/api/tool?toolName=_bash_"} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", path, rec.Code)
		}
	}
}

func TestQueryCanExecuteBackendToolLoop(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		hasToolMessage := false
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if role, _ := message["role"].(string); role == "tool" {
				hasToolMessage = true
				break
			}
		}
		if !hasToolMessage {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"_datetime_","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"完成工具调用后"}}]}`,
			`{"choices":[{"delta":{"content":"的最终回答"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	server := fixture.server

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"tool.start"`) {
		t.Fatalf("expected tool.start event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.args"`) {
		t.Fatalf("expected tool.args event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool.end event, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.snapshot"`) || strings.Contains(body, `"type":"content.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if strings.Contains(body, `"toolType":`) {
		t.Fatalf("did not expect toolType in live sse, got %s", body)
	}
	if strings.Contains(body, `"viewportKey":`) {
		t.Fatalf("did not expect viewportKey for backend tool, got %s", body)
	}
	if !strings.Contains(body, "完成工具调用后") || !strings.Contains(body, "的最终回答") {
		t.Fatalf("expected live sse deltas for final assistant content, got %s", body)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, body)
	assertSSEPayloadOrder(t, body, "tool.start", []string{
		`"seq":`,
		`"type":"tool.start"`,
		`"toolId":"`,
		`"runId":"`,
		`"timestamp":`,
	})

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"content.snapshot",
		"run.complete",
	)
}

func TestQueryDecryptsAESProviderAPIKeyBeforeSendingAuthorizationHeader(t *testing.T) {
	const envPart = "server-test-env-secret"
	const plainAPIKey = "test-key"

	t.Setenv("PROVIDER_APIKEY_KEY_PART", envPart)

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+plainAPIKey {
			t.Fatalf("expected decrypted Authorization header, got %q", got)
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		setupRuntime: func(root string, _ *config.Config) {
			providerConfig := strings.Join([]string{
				"key: mock",
				"baseUrl: http://placeholder.invalid",
				"apiKey: " + mustEncryptProviderAPIKeyForServerTest(t, envPart, plainAPIKey),
				"defaultModel: mock-model",
			}, "\n")
			providerPath := filepath.Join(root, "registries", "providers", "mock.yml")
			if err := os.WriteFile(providerPath, []byte(providerConfig), 0o644); err != nil {
				t.Fatalf("write encrypted provider config: %v", err)
			}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hello","agentKey":"mock-runner"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", rec.Body.String())
	}
}

func TestQueryAndRunStreamHideDebugEventsByDefaultButPersistThem(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hide debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertStringSliceExcludes(t, decodeEventTypesFromSSE(t, body), "debug.preCall", "debug.postCall")

	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got %s", body)
	}
	runID, _ := messages[0]["runId"].(string)
	chatID, _ := messages[0]["chatId"].(string)
	if runID == "" || chatID == "" {
		t.Fatalf("expected runId/chatId in first sse message, got %#v", messages[0])
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID, nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	assertStringSliceExcludes(t, decodeEventTypesFromSSE(t, runRec.Body.String()), "debug.preCall", "debug.postCall")

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertEventTypesInclude(t, chatResp.Data.Events, "debug.preCall", "debug.postCall")
}

func TestQueryAndRunStreamIncludeDebugEventsWhenEnabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.SSE.IncludeDebugEvents = true
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"show debug"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertStringSliceContains(t, decodeEventTypesFromSSE(t, body), "debug.preCall", "debug.postCall")

	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got %s", body)
	}
	var preCall map[string]any
	for _, message := range messages {
		if eventType, _ := message["type"].(string); eventType == "debug.preCall" {
			preCall = message
			break
		}
	}
	if preCall == nil {
		t.Fatalf("expected debug.preCall in sse stream, got %#v", messages)
	}
	preCallData, _ := preCall["data"].(map[string]any)
	provider, _ := preCallData["provider"].(map[string]any)
	model, _ := preCallData["model"].(map[string]any)
	requestBody, _ := preCallData["requestBody"].(map[string]any)
	if provider["key"] != "mock" {
		t.Fatalf("expected provider key mock, got %#v", provider)
	}
	if !strings.HasSuffix(stringValue(provider["endpoint"]), "/v1/chat/completions") {
		t.Fatalf("unexpected provider endpoint %#v", provider)
	}
	if model["key"] != "mock-model" || model["id"] != "mock-model-id" {
		t.Fatalf("unexpected model payload %#v", model)
	}
	if len(requestBody) == 0 {
		t.Fatalf("expected requestBody payload, got %#v", preCallData)
	}
	if _, exists := preCallData["systemPrompt"]; exists {
		t.Fatalf("did not expect systemPrompt in debug.preCall payload, got %#v", preCallData)
	}
	if _, exists := preCallData["tools"]; exists {
		t.Fatalf("did not expect tools in debug.preCall payload, got %#v", preCallData)
	}
	runID, _ := messages[0]["runId"].(string)
	if runID == "" {
		t.Fatalf("expected runId in first sse message, got %#v", messages[0])
	}

	runRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(runRec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID, nil))
	if runRec.Code != http.StatusOK {
		t.Fatalf("expected run stream 200, got %d: %s", runRec.Code, runRec.Body.String())
	}
	assertStringSliceContains(t, decodeEventTypesFromSSE(t, runRec.Body.String()), "debug.preCall", "debug.postCall")
}

func TestQueryPersistsToolSnapshotWhenSSEPayloadEventsDisabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		hasToolMessage := false
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if role, _ := message["role"].(string); role == "tool" {
				hasToolMessage = true
				break
			}
		}
		if !hasToolMessage {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"_datetime_","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"payload hidden"}}]}`,
			`{"choices":[{"delta":{"content":" from sse"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	fixture.cfg.SSE.IncludeToolPayloadEvents = false
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          nil,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `.snapshot"`) {
		t.Fatalf("expected live sse to exclude snapshot events, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.start"`) || !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool lifecycle to remain in sse, got %s", body)
	}

	chatsRec := httptest.NewRecorder()
	server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"content.snapshot",
		"run.complete",
	)
}

func TestQueryFailsRunWhenProviderOmitsToolCallID(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"_datetime_","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"run.error"`) {
		t.Fatalf("expected run.error when toolCallId is missing, got %s", body)
	}
	if strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("did not expect tool.result when toolCallId is missing, got %s", body)
	}
	if strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("did not expect run.complete after toolCallId error, got %s", body)
	}
}

func TestQueryEmitsRunErrorOnInvalidFirstFrame(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"broken":true}`, `[DONE]`)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"bad stream"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	// LLM stream now starts after bootstrap events, so the response is
	// always SSE (200).  An invalid first frame produces run.error via SSE
	// instead of a JSON 500 — consistent with Java behaviour.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "run.start") {
		t.Fatalf("expected bootstrap events before error, got %s", body)
	}
	if !strings.Contains(body, "run.error") {
		t.Fatalf("expected run.error event, got %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected [DONE] sentinel, got %s", body)
	}
}

func TestQueryEmitsRunErrorWhenStreamFailsMidFlight(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"partial"}}]}`)
		_, _ = io.WriteString(w, "data: {not-json}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"mid stream error"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected streamed content delta, got %s", body)
	}
	if !strings.Contains(body, `"type":"run.error"`) {
		t.Fatalf("expected run.error event, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", body)
	}
	assertSSEMessagesHaveSeqAndTimestamp(t, body)
}

func TestQueryStreamsBeforeRunCompleteOverHTTP(t *testing.T) {
	if os.Getenv("RUN_SOCKET_TESTS") != "1" {
		t.Skip("set RUN_SOCKET_TESTS=1 to run real loopback SSE test")
	}
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"first "}}]}`,
			`{"choices":[{"delta":{"content":"second"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"stream please"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	seenDelta := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			t.Fatalf("read sse line: %v", err)
		}
		if strings.Contains(line, `"type":"content.delta"`) {
			seenDelta = true
		}
		if strings.Contains(line, `"type":"run.complete"`) && !seenDelta {
			t.Fatalf("expected content.delta before run.complete")
		}
		if err == io.EOF {
			break
		}
	}
	if !seenDelta {
		t.Fatalf("expected to observe streamed content delta before completion")
	}
}

func TestInterruptCancelsActiveRunAndSkipsRunComplete(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		if _, err := io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"partial"}}]}`+"\n\n"); err != nil {
			t.Fatalf("write partial delta: %v", err)
		}
		flusher.Flush()
		<-r.Context().Done()
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"interrupt me"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "run.start" {
				runID, _ = payload["runId"].(string)
			}
			if payload["type"] == "content.delta" && runID != "" {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before interrupt: %v", readErr)
		}
	}

	interruptRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(interruptRec, httptest.NewRequest(http.MethodPost, "/api/interrupt", bytes.NewBufferString(`{"runId":"`+runID+`"}`)))
	if interruptRec.Code != http.StatusOK {
		t.Fatalf("interrupt expected 200, got %d: %s", interruptRec.Code, interruptRec.Body.String())
	}
	var interruptResp api.ApiResponse[api.InterruptResponse]
	if err := json.Unmarshal(interruptRec.Body.Bytes(), &interruptResp); err != nil {
		t.Fatalf("decode interrupt response: %v", err)
	}
	if !interruptResp.Data.Accepted || interruptResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted interrupt, got %#v", interruptResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after interrupt: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"run.cancel"`) {
		t.Fatalf("expected run.cancel event, got %s", body)
	}
	if strings.Contains(body, `"type":"run.complete"`) {
		t.Fatalf("did not expect run.complete after interrupt, got %s", body)
	}

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat, got %#v", chatsResp.Data)
	}
	if chatsResp.Data[0].LastRunID != "" || chatsResp.Data[0].LastRunContent != "" {
		t.Fatalf("expected interrupted run to skip completion summary, got %#v", chatsResp.Data[0])
	}
}

func TestFrontendSubmitAndSteerAreConsumedBeforeNextTurn(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue with the request\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please confirm first"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var toolStartPayload map[string]any
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.start" && payload["toolName"] == "_ask_user_question_" {
				toolStartPayload = payload
				toolID, _ = payload["toolId"].(string)
			}
			if payload["type"] == "awaiting.ask" {
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if toolStartPayload == nil {
		t.Fatalf("expected frontend tool.start before awaiting.ask, got %s", streamBody.String())
	}
	if _, exists := toolStartPayload["toolType"]; exists {
		t.Fatalf("did not expect toolType on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["viewportKey"]; exists {
		t.Fatalf("did not expect viewportKey on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on tool.start, got %#v", toolStartPayload)
	}
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask before submit, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["viewportType"]; exists {
		t.Fatalf("did not expect viewportType on question awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["viewportKey"]; exists {
		t.Fatalf("did not expect viewportKey on question awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["timeout"] != float64(210000) {
		t.Fatalf("expected await question timeout 210000, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected await question mode question, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("expected question awaiting.ask questions length 1, got %#v", awaitQuestionPayload)
	}
	firstQuestion, _ := questions[0].(map[string]any)
	if firstQuestion["id"] != "q1" || firstQuestion["question"] != "Need confirmation" {
		t.Fatalf("unexpected inline question payload %#v", firstQuestion)
	}

	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"runId":"`+runID+`","message":"Please keep it short."}`))
	steerReq.Header.Set("Content-Type", "application/json")
	steerRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(steerRec, steerReq)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("steer expected 200, got %d: %s", steerRec.Code, steerRec.Body.String())
	}
	var steerResp api.ApiResponse[api.SteerResponse]
	if err := json.Unmarshal(steerRec.Body.Bytes(), &steerResp); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if !steerResp.Data.Accepted || steerResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted steer, got %#v", steerResp.Data)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	var submitResp api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(submitRec.Body.Bytes(), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !submitResp.Data.Accepted || submitResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted submit, got %#v", submitResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event for question mode, got %s", body)
	}
	if !strings.Contains(body, `"questions":[`) {
		t.Fatalf("expected top-level questions in question awaiting.ask event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":[{"id":"q1","answer":"Approve"}]`) {
		t.Fatalf("expected request.submit params, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.steer"`) {
		t.Fatalf("expected request.steer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if !strings.Contains(body, `"mode":"question"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"answers":[{"answer":"Approve","id":"q1","question":"Need confirmation"}]`) {
		t.Fatalf("expected normalized question awaiting.answer, got %s", body)
	}
	if !strings.Contains(body, `"result":[{"id":"q1","answer":"Approve"}]`) {
		t.Fatalf("expected raw submit array in tool.result, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected final answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result", "request.steer")

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	foundFrontendSnapshot := false
	foundAwaitAsk := false
	foundRequestSubmit := false
	foundAwaitingAnswer := false
	for _, event := range chatResp.Data.Events {
		switch event.Type {
		case "tool.snapshot":
			if event.String("toolName") != "_ask_user_question_" {
				continue
			}
			foundFrontendSnapshot = true
			if _, exists := event.Payload["toolType"]; exists {
				t.Fatalf("did not expect frontend snapshot toolType, got %#v", event)
			}
			if _, exists := event.Payload["viewportKey"]; exists {
				t.Fatalf("did not expect frontend snapshot viewportKey, got %#v", event)
			}
			if _, exists := event.Payload["toolTimeout"]; exists {
				t.Fatalf("did not expect frontend snapshot toolTimeout, got %#v", event)
			}
		case "awaiting.ask":
			foundAwaitAsk = true
			if event.String("mode") != "question" || event.Value("viewportKey") != nil || event.Value("viewportType") != nil {
				t.Fatalf("unexpected awaiting.ask payload %#v", event)
			}
			if _, exists := event.Payload["awaitName"]; exists {
				t.Fatalf("did not expect awaitName on awaiting.ask in chat detail, got %#v", event)
			}
			if _, exists := event.Payload["chatId"]; exists {
				t.Fatalf("did not expect chatId on awaiting.ask in chat detail, got %#v", event)
			}
			questions, _ := event.Payload["questions"].([]any)
			if len(questions) != 1 {
				t.Fatalf("expected question awaiting.ask questions length 1, got %#v", event)
			}
		case "request.submit":
			foundRequestSubmit = true
			if event.Value("params") == nil {
				t.Fatalf("expected params on request.submit in chat detail, got %#v", event)
			}
		case "awaiting.answer":
			foundAwaitingAnswer = true
			answers, _ := event.Value("answers").([]any)
			if event.String("mode") != "question" || event.String("status") != "answered" || len(answers) != 1 {
				t.Fatalf("unexpected awaiting.answer in chat detail %#v", event)
			}
		}
	}
	if !foundFrontendSnapshot {
		t.Fatalf("expected _ask_user_question_ tool.snapshot in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitAsk {
		t.Fatalf("expected awaiting.ask in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundRequestSubmit {
		t.Fatalf("expected request.submit in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitingAnswer {
		t.Fatalf("expected awaiting.answer in chat detail, got %#v", chatResp.Data.Events)
	}

	select {
	case messages := <-secondTurnMessages:
		toolIndex := -1
		steerIndex := -1
		for i, message := range messages {
			role, _ := message["role"].(string)
			content, _ := message["content"].(string)
			if role == "tool" {
				toolIndex = i
			}
			if role == "user" && content == "Please keep it short." {
				steerIndex = i
			}
		}
		if toolIndex < 0 {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if steerIndex <= toolIndex {
			t.Fatalf("expected steer message after tool message, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionAwaitFollowsToolStartAndPrecedesToolArgs(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Notification topics\",\"type\":\"multi-select\",\"options\":[{\"label\":\"产品更新\",\"description\":\"Release notes and new features\"},{\"label\":\"使用教程\",\"description\":\"How-to guides and walkthroughs\"}],\"allowFreeText\":false},{\"question\":\"How many people?\",\"type\":\"number\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var awaitQuestionPayload map[string]any
	var toolStartPayload map[string]any
	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				goto questionSubmit
			case "tool.start":
				if payload["toolName"] == "_ask_user_question_" {
					toolStartPayload = payload
					toolID, _ = payload["toolId"].(string)
				}
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

questionSubmit:
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask after tool.start and before tool.args, got %s", streamBody.String())
	}
	if toolStartPayload == nil {
		t.Fatalf("expected tool.start for _ask_user_question_, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]`) {
		t.Fatalf("expected request.submit params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"answers":[{"answers":["产品更新","使用教程"],"id":"q1","question":"Notification topics"},{"answer":2,"id":"q2","question":"How many people?"}]`) {
		t.Fatalf("expected normalized awaiting.answer answers, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if strings.Contains(body, `"result":{"mode":"question"`) {
		t.Fatalf("did not expect normalized question wrapper in tool.result, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	resultItems, ok := toolResultPayload["result"].([]any)
	if !ok || len(resultItems) != 2 {
		t.Fatalf("expected raw submit array in tool.result, got %#v", toolResultPayload)
	}
	firstItem, _ := resultItems[0].(map[string]any)
	secondItem, _ := resultItems[1].(map[string]any)
	firstAnswers, _ := firstItem["answers"].([]any)
	if firstItem["id"] != "q1" || len(firstAnswers) != 2 || firstAnswers[0] != "产品更新" || firstAnswers[1] != "使用教程" {
		t.Fatalf("unexpected first tool.result item: %#v", firstItem)
	}
	if secondItem["id"] != "q2" || secondItem["answer"] != float64(2) {
		t.Fatalf("unexpected second tool.result item: %#v", secondItem)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if toolContent != "问题：Notification topics\n回答：产品更新, 使用教程\n问题：How many people?\n回答：2" {
			t.Fatalf("expected qa-formatted tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionChunkedArgsEmitAwaitAfterFirstToolArgs(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Notification topics\",\"type\":\"multi-select\","}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"options\":[{\"label\":\"产品更新\",\"description\":\"Release notes and new features\"},{\"label\":\"使用教程\",\"description\":\"How-to guides and walkthroughs\"}],\"allowFreeText\":false},{\"question\":\"How many people?\",\"type\":\"number\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"chunked question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var awaitQuestionPayload map[string]any
	var toolStartPayload map[string]any
	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				goto chunkedQuestionSubmit
			case "tool.start":
				if payload["toolName"] == "_ask_user_question_" {
					toolStartPayload = payload
					toolID, _ = payload["toolId"].(string)
				}
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

chunkedQuestionSubmit:
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask after chunked tool args, got %s", streamBody.String())
	}
	if toolStartPayload == nil {
		t.Fatalf("expected tool.start for _ask_user_question_, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"chunkIndex":0`) || !strings.Contains(body, `"chunkIndex":1`) {
		t.Fatalf("expected tool.args chunks 0 and 1, got %s", body)
	}
	firstToolArgsIndex := strings.Index(body, `"chunkIndex":0`)
	awaitAskIndex := strings.Index(body, `"type":"awaiting.ask"`)
	secondToolArgsIndex := strings.Index(body, `"chunkIndex":1`)
	toolEndIndex := strings.Index(body, `"type":"tool.end"`)
	if firstToolArgsIndex < 0 || awaitAskIndex < 0 || secondToolArgsIndex < 0 || toolEndIndex < 0 {
		t.Fatalf("expected chunked question flow markers, got %s", body)
	}
	if !(firstToolArgsIndex < awaitAskIndex && awaitAskIndex < secondToolArgsIndex && secondToolArgsIndex < toolEndIndex) {
		t.Fatalf("expected chunked event order tool.args(0) -> awaiting.ask -> tool.args(1) -> tool.end, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if toolContent != "问题：Notification topics\n回答：产品更新, 使用教程\n问题：How many people?\n回答：2" {
			t.Fatalf("expected qa-formatted tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionInvalidSelectOptionsFailsBeforeAwait(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick a plan\",\"type\":\"select\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"invalid question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"tool.start"`) {
		t.Fatalf("expected tool.start event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.args"`) {
		t.Fatalf("expected tool.args event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool.end event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("did not expect awaiting.ask for invalid question args, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload for invalid question args, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) || !strings.Contains(body, `invalid tool arguments: Pick a plan: options is required for select and multi-select questions`) {
		t.Fatalf("expected invalid tool arguments tool.result, got %s", body)
	}

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if !strings.Contains(toolContent, "invalid tool arguments: Pick a plan: options is required for select and multi-select questions") {
			t.Fatalf("expected invalid tool arguments in second turn tool message, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionAwaitDismissReturnsCancelledStructuredResult(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick a plan\",\"type\":\"select\",\"options\":[{\"label\":\"Weekend\",\"description\":\"2 days\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question cancel flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				runID, _ = payload["runId"].(string)
				toolID, _ = payload["awaitingId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.submit"`) || !strings.Contains(body, `"params":[]`) {
		t.Fatalf("expected request.submit with empty params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `"code":"user_dismissed"`) {
		t.Fatalf("expected dismissed awaiting.answer in stream, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	if toolResultPayload["result"] != nil {
		t.Fatalf("expected dismissed tool.result payload to be omitted, got %#v", toolResultPayload)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if !strings.Contains(toolContent, `"status":"error"`) || !strings.Contains(toolContent, `"mode":"question"`) || !strings.Contains(toolContent, `"code":"user_dismissed"`) {
			t.Fatalf("expected dismissed JSON tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

type recordingSandbox struct {
	commands []string
	envs     []map[string]string
}

type scriptedSandbox struct {
	execute func(command string, cwd string, env map[string]string) contracts.SandboxExecutionResult
}

type recordingMCPClient struct {
	commands []string
}

type stubMCPToolCatalog struct {
	defs []api.ToolDetailResponse
}

func (s *recordingSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *recordingSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	s.commands = append(s.commands, command)
	s.envs = append(s.envs, contracts.CloneStringMap(env))
	return contracts.SandboxExecutionResult{
		ExitCode:         0,
		Stdout:           "executed: " + command,
		Stderr:           "",
		WorkingDirectory: cwd,
	}, nil
}

func (s *recordingSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (s *scriptedSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *scriptedSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	if s.execute == nil {
		return contracts.SandboxExecutionResult{ExitCode: 0, WorkingDirectory: cwd}, nil
	}
	return s.execute(command, cwd, env), nil
}

func (s *scriptedSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (m *recordingMCPClient) CallTool(_ context.Context, _ string, toolName string, args map[string]any, _ map[string]any) (any, error) {
	command, _ := args["command"].(string)
	m.commands = append(m.commands, command)
	return map[string]any{
		"structuredContent": map[string]any{
			"tool":    toolName,
			"command": command,
			"status":  "ok",
		},
	}, nil
}

func (c stubMCPToolCatalog) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), c.defs...)
}

func (c stubMCPToolCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, def := range c.defs {
		if strings.EqualFold(def.Name, name) || strings.EqualFold(def.Key, name) {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func TestBashHITLApproveFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "approve"})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, defaultBashHITLCommand()))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, defaultBashHITLCommand()))
	if err != nil {
		t.Fatalf("marshal expected await payload: %v", err)
	}
	expectedSubmitPayload := string(expectedAwaitPayload)
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"leave_form"`) {
		t.Fatalf("expected leave_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"mode":"form"`) || !strings.Contains(body, `"forms":[`) {
		t.Fatalf("expected form awaiting.ask payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"submit"`) ||
		!strings.Contains(body, `"id":"form-1"`) ||
		!strings.Contains(body, `"payload":`+expectedSubmitPayload) {
		t.Fatalf("expected approve awaiting.answer in stream, got %s", body)
	}
	if !strings.Contains(body, `"payload":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected form awaiting.ask payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"title":"mock 请假申请"`) {
		t.Fatalf("expected form awaiting.ask title in stream, got %s", body)
	}
	if strings.Contains(body, `"initialPayload":`) || strings.Contains(body, `"viewportPayload":`) {
		t.Fatalf("did not expect legacy form payload fields in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowReplaysApprovalSummaryInChatRawMessages(t *testing.T) {
	var providerCallCount atomic.Int32
	command := "docker rmi nginx:latest"
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", "_bash_", map[string]any{
					"command":     command,
					"description": "执行测试命令",
					"cwd":         "/workspace",
				}),
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"bash hitl complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox: &recordingSandbox{},
		configure: func(cfg *config.Config) {
			cfg.BashHITL.DefaultTimeoutMs = 15000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			root := filepath.Join(cfg.Paths.SkillsMarketDir, "mock-skill", ".bash-hooks")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir skill bash-hooks dir: %v", err)
			}
			rulesContent := strings.Join([]string{
				"commands:",
				"  - command: docker",
				"    subcommands:",
				"      - match: rmi",
				"        level: 1",
				"        viewportType: builtin",
				"        viewportKey: confirm_dialog",
				"        ruleKey: dangerous-commands::docker-rmi",
			}, "\n")
			if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(rulesContent), 0o644); err != nil {
				t.Fatalf("write skill bash hook rule: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	awaitingID := ""
	approvalID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				awaitingID, _ = payload["awaitingId"].(string)
				if approvals, ok := payload["approvals"].([]any); ok && len(approvals) > 0 {
					if firstApproval, ok := approvals[0].(map[string]any); ok {
						approvalID, _ = firstApproval["id"].(string)
					}
				}
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if awaitingID == "" || approvalID == "" {
		t.Fatalf("expected approval awaiting payload, got %s", streamBody.String())
	}

	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+awaitingID+`","params":[{"id":"`+approvalID+`","decision":"approve"}]}`)))
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		_, readErr := reader.ReadString('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat, got %#v", chatsResp)
	}

	chatID := chatsResp.Data[0].ChatID
	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}

	hitlIndex := -1
	hitlCount := 0
	for i, message := range chatResp.Data.RawMessages {
		if message["role"] == "user" && strings.Contains(stringValue(message["content"]), "[HITL]") {
			hitlIndex = i
			hitlCount++
		}
	}
	if hitlCount != 1 {
		t.Fatalf("expected exactly one replayed HITL summary raw message, got %#v", chatResp.Data.RawMessages)
	}
	if hitlIndex == 0 || chatResp.Data.RawMessages[hitlIndex-1]["role"] != "tool" {
		t.Fatalf("expected HITL raw message to follow tool result, got %#v", chatResp.Data.RawMessages)
	}
	if !strings.Contains(stringValue(chatResp.Data.RawMessages[hitlIndex]["content"]), `[HITL] docker rmi nginx:latest → approve`) {
		t.Fatalf("expected replayed HITL summary content, got %#v", chatResp.Data.RawMessages[hitlIndex])
	}
}

func TestSandboxBashResultShapeAcrossStreamBoundaries(t *testing.T) {
	t.Run("success uses plain stdout for sse and tool message", func(t *testing.T) {
		body, secondTurn := runSandboxBashQueryForResultShape(t, &scriptedSandbox{
			execute: func(command string, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
				return contracts.SandboxExecutionResult{
					ExitCode:         0,
					Stdout:           "listed from " + cwd + ": " + command + "\n",
					WorkingDirectory: cwd,
				}
			},
		})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		if got, ok := resultPayload["result"].(string); !ok || got != "listed from /workspace: ls sample\n" {
			t.Fatalf("expected string tool.result payload, got %#v", resultPayload["result"])
		}
		toolContent := findToolMessageContent(t, secondTurn, "_bash_")
		if toolContent != "listed from /workspace: ls sample\n" {
			t.Fatalf("expected plain stdout tool message, got %q", toolContent)
		}
	})

	t.Run("failure uses structured object for sse and json for tool message", func(t *testing.T) {
		body, secondTurn := runSandboxBashQueryForResultShape(t, &scriptedSandbox{
			execute: func(_ string, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
				return contracts.SandboxExecutionResult{
					ExitCode:         2,
					Stdout:           "",
					Stderr:           "ls: sample: No such file or directory\n",
					WorkingDirectory: cwd,
				}
			},
		})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		resultObject, ok := resultPayload["result"].(map[string]any)
		if !ok {
			t.Fatalf("expected object tool.result payload, got %#v", resultPayload["result"])
		}
		if resultObject["exitCode"] != float64(2) {
			t.Fatalf("expected exitCode=2, got %#v", resultObject)
		}
		if resultObject["stderr"] != "ls: sample: No such file or directory\n" {
			t.Fatalf("expected stderr in result payload, got %#v", resultObject)
		}
		toolContent := findToolMessageContent(t, secondTurn, "_bash_")
		if !strings.HasPrefix(toolContent, "{") || !strings.Contains(toolContent, `"exitCode":2`) || !strings.Contains(toolContent, `"stderr":"ls: sample: No such file or directory\n"`) {
			t.Fatalf("expected JSON tool message for failure, got %q", toolContent)
		}
	})

	t.Run("html hitl success keeps stdout in result without approval sidecar", func(t *testing.T) {
		body, _ := runBashHITLFlow(t, bashHITLFlowOptions{action: "approve"})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		if got, ok := resultPayload["result"].(string); !ok || got == "" {
			t.Fatalf("expected stdout string tool.result payload, got %#v", resultPayload["result"])
		}
		if _, ok := resultPayload["approval"]; ok {
			t.Fatalf("did not expect approval sidecar for html form HITL, got %#v", resultPayload)
		}
	})
}

func TestBashHITLModifyFlow(t *testing.T) {
	modified := `mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"personal","start_date":"2026-04-21","end_date":"2026-04-22","days":2,"reason":"family_trip"}`
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "modify", modifiedCommand: modified})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, modified))
	expectedSubmitPayload, err := json.Marshal(payloadFromCommandForTest(t, modified))
	if err != nil {
		t.Fatalf("marshal modified payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected modified command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"submit"`) ||
		!strings.Contains(body, `"id":"form-1"`) ||
		!strings.Contains(body, `"payload":`+string(expectedSubmitPayload)) {
		t.Fatalf("expected modify awaiting.answer in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLRejectFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "reject"})
	if len(executed) != 0 {
		t.Fatalf("expected rejected command not to execute, got %#v", executed)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("expected hard-stop rejected tool result, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"reject"`) ||
		!strings.Contains(body, `"id":"form-1"`) ||
		!strings.Contains(body, `"reason":"user_cancelled"`) {
		t.Fatalf("expected reject awaiting.answer in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLTimeoutFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		skipSubmit: true,
		timeoutMs:  20,
	})
	if len(executed) != 0 {
		t.Fatalf("expected timed out command not to execute, got %#v", executed)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `"code":"timeout"`) {
		t.Fatalf("expected timeout awaiting.answer in stream, got %s", body)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "hitl_timeout: command execution timed out while waiting for user approval" {
		t.Fatalf("expected timeout tool.result in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLSimpleBashApproveFlow(t *testing.T) {
	mcpClient := &recordingMCPClient{}
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		toolName: "simple-bash",
		action:   "approve",
		mcp:      mcpClient,
		mcpTools: stubMCPToolCatalog{defs: []api.ToolDetailResponse{
			{
				Key:         "simple-bash",
				Name:        "simple-bash",
				Label:       "Simple Bash",
				Description: "Execute mock bash command",
				Parameters:  map[string]any{"type": "object"},
				Meta: map[string]any{
					"kind":          "backend",
					"sourceType":    "mcp",
					"sourceKey":     "mock",
					"serverKey":     "mock",
					"clientVisible": true,
				},
			},
		}},
	})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, defaultBashHITLCommand()))
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected simple-bash command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"leave_form"`) {
		t.Fatalf("expected leave_form viewport in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowForExpenseCreate(t *testing.T) {
	command := `mock create-expense --payload {"employee":{"id":"E1001","name":"张三"},"department":{"code":"engineering","name":"工程部"},"expense_type":"travel","currency":"CNY","total_amount":1280.5,"items":[{"category":"transport","amount":800,"invoice_id":"INV-001","occurred_on":"2026-04-10","description":"flight"},{"category":"hotel","amount":480.5,"invoice_id":"INV-002","occurred_on":"2026-04-11","description":"hotel"}],"submitted_at":"2026-04-14T10:30:00+08:00"}`
	rules := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: create-expense",
		"        level: 1",
		"        viewportType: html",
		"        viewportKey: expense_form",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	expectedCommand := rebuildPayloadCommandForTest(t, command, payloadFromCommandForTest(t, command))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, command))
	if err != nil {
		t.Fatalf("marshal expected expense await payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved expense command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"expense_form"`) {
		t.Fatalf("expected expense_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"payload":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected expense approval payload in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowForProcurementCreate(t *testing.T) {
	command := `mock create-procurement --payload {"requester_id":"E1001","department":"engineering","budget_code":"RD-2026-001","reason":"team expansion","delivery_city":"Shanghai","items":[{"name":"MacBook Pro","quantity":2,"unit_price":18999,"vendor":"Apple"}],"approvers":["MGR100","FIN200"],"requested_at":"2026-04-14T11:00:00+08:00"}`
	rules := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: create-procurement",
		"        level: 1",
		"        viewportType: html",
		"        viewportKey: procurement_form",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	expectedCommand := rebuildPayloadCommandForTest(t, command, payloadFromCommandForTest(t, command))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, command))
	if err != nil {
		t.Fatalf("marshal expected procurement await payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved procurement command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"procurement_form"`) {
		t.Fatalf("expected procurement_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"payload":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected procurement approval payload in stream, got %s", body)
	}
}

func TestBashHITLDockerRMIApproveFlow(t *testing.T) {
	command := "docker rmi nginx:latest"
	rules := strings.Join([]string{
		"commands:",
		"  - command: docker",
		"    subcommands:",
		"      - match: rmi",
		"        level: 1",
		"        viewportType: builtin",
		"        viewportKey: confirm_dialog",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	if len(executed) != 1 || executed[0] != command {
		t.Fatalf("expected approved docker rmi to execute once, got %#v", executed)
	}
	if strings.Contains(body, `"viewportKey":"confirm_dialog"`) {
		t.Fatalf("did not expect confirm_dialog viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"mode":"approval"`) ||
		!strings.Contains(body, `"approvals":[`) ||
		!strings.Contains(body, `"command":"docker rmi nginx:latest"`) ||
		!strings.Contains(body, `"ruleKey":"dangerous::docker::rmi::1::builtin::confirm_dialog"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"description":"`) ||
		!strings.Contains(body, `"allowFreeText":true`) {
		t.Fatalf("expected approval awaiting.ask payload in stream, got %s", body)
	}
	if strings.Contains(body, `"level":1`) {
		t.Fatalf("did not expect level in approval awaiting.ask payload, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) ||
		!strings.Contains(body, `"params":[{"id":"tool_bash","decision":"approve"}]`) {
		t.Fatalf("expected approval request.submit payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"decision":"approve"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"command":"docker rmi nginx:latest"`) {
		t.Fatalf("expected normalized approval awaiting.answer payload in stream, got %s", body)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got == "" {
		t.Fatalf("expected stdout string tool.result payload, got %#v", resultPayload["result"])
	}
	approvalPayload, ok := resultPayload["approval"].(map[string]any)
	if !ok || approvalPayload["decision"] != "approve" {
		t.Fatalf("expected approval sidecar on tool.result, got %#v", resultPayload)
	}
	if _, ok := resultPayload["hitl"]; ok {
		t.Fatalf("did not expect legacy hitl key, got %#v", resultPayload)
	}
	if strings.Contains(body, `"frontend_submit_invalid_payload"`) {
		t.Fatalf("did not expect frontend_submit_invalid_payload, got %s", body)
	}
}

func TestBashHITLDockerImageRMRejectFlow(t *testing.T) {
	command := "docker image rm nginx:latest"
	rules := strings.Join([]string{
		"commands:",
		"  - command: docker",
		"    subcommands:",
		"      - match: image rm",
		"        level: 1",
		"        viewportType: builtin",
		"        viewportKey: confirm_dialog",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "reject",
		command:      command,
		rulesContent: rules,
	})
	if len(executed) != 0 {
		t.Fatalf("expected rejected docker image rm not to execute, got %#v", executed)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("expected hard-stop rejected tool result, got %s", body)
	}
	if strings.Contains(body, `"viewportKey":"confirm_dialog"`) {
		t.Fatalf("did not expect confirm_dialog viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"decision":"reject"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"command":"docker image rm nginx:latest"`) {
		t.Fatalf("expected reject approval answer in stream, got %s", body)
	}
}

type bashHITLFlowOptions struct {
	toolName        string
	action          string
	modifiedCommand string
	command         string
	rulesContent    string
	skipSubmit      bool
	timeoutMs       int
	mcp             contracts.McpClient
	mcpTools        stubMCPToolCatalog
}

func runBashHITLFlow(t *testing.T, options bashHITLFlowOptions) (string, []string) {
	t.Helper()
	toolName := options.toolName
	if toolName == "" {
		toolName = "_bash_"
	}
	command := defaultBashHITLCommand()
	if strings.TrimSpace(options.command) != "" {
		command = options.command
	}
	rulesContent := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: create-leave",
		"        level: 1",
		"        title: mock 请假申请",
		"        viewportType: html",
		"        viewportKey: leave_form",
	}, "\n")
	if strings.TrimSpace(options.rulesContent) != "" {
		rulesContent = options.rulesContent
	}

	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)
	sandbox := &recordingSandbox{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", toolName, map[string]any{
					"command":     command,
					"description": "执行测试命令",
					"cwd":         "/workspace",
				}),
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"bash hitl complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox:  sandbox,
		mcp:      options.mcp,
		mcpTools: options.mcpTools,
		configure: func(cfg *config.Config) {
			cfg.BashHITL.DefaultTimeoutMs = 15000
			if options.timeoutMs > 0 {
				cfg.BashHITL.DefaultTimeoutMs = options.timeoutMs
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			root := filepath.Join(cfg.Paths.SkillsMarketDir, "mock-skill", ".bash-hooks")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir skill bash-hooks dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(rulesContent), 0o644); err != nil {
				t.Fatalf("write skill bash hook rule: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	originalToolID := ""
	awaitingID := ""
	approvalID := ""
	var awaitAskPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "tool.start":
				switch payload["toolName"] {
				case "_bash_":
					originalToolID, _ = payload["toolId"].(string)
				case "simple-bash":
					originalToolID, _ = payload["toolId"].(string)
				}
			case "awaiting.ask":
				awaitAskPayload = payload
				awaitingID, _ = payload["awaitingId"].(string)
				if approvals, ok := payload["approvals"].([]any); ok && len(approvals) > 0 {
					if firstApproval, ok := approvals[0].(map[string]any); ok {
						approvalID, _ = firstApproval["id"].(string)
					}
				}
				goto submit
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

submit:
	if !options.skipSubmit {
		var submitPayload string
		if strings.EqualFold(stringValue(awaitAskPayload["mode"]), "form") {
			if options.action == "reject" {
				submitPayload = `[{"id":"form-1","reason":"user_cancelled"}]`
			} else {
				submitCommand := command
				if options.action == "modify" {
					submitCommand = options.modifiedCommand
				}
				payloadJSON, err := json.Marshal([]map[string]any{{
					"id":      "form-1",
					"payload": payloadFromCommandForTest(t, submitCommand),
				}})
				if err != nil {
					t.Fatalf("marshal html submit payload: %v", err)
				}
				submitPayload = string(payloadJSON)
			}
		} else {
			if strings.TrimSpace(approvalID) == "" {
				t.Fatalf("expected approval id in awaiting.ask payload, got %#v", awaitAskPayload)
			}
			submitPayload = `[{"id":"` + approvalID + `","decision":"` + options.action + `"}]`
		}
		submitRec := httptest.NewRecorder()
		fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+awaitingID+`","params":`+submitPayload+`}`)))
		if submitRec.Code != http.StatusOK {
			t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
		}
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	messages := decodeSSEMessages(t, streamBody.String())
	assertSpecificEventOrder(t, messages, originalToolID, awaitingID)
	select {
	case secondTurn := <-secondTurnMessages:
		toolMessages := 0
		hitlSummaries := 0
		seenUserAfterTool := false
		for _, message := range secondTurn {
			role, _ := message["role"].(string)
			if role == "tool" {
				toolMessages++
				if seenUserAfterTool {
					t.Fatalf("expected tool results to stay contiguous before HITL summary, got %#v", secondTurn)
				}
				continue
			}
			if role == "user" {
				content, _ := message["content"].(string)
				if strings.Contains(content, "[HITL]") {
					hitlSummaries++
					seenUserAfterTool = true
				}
			}
		}
		if toolMessages < 1 {
			t.Fatalf("expected second turn to receive original bash tool result, got %#v", secondTurn)
		}
		if strings.EqualFold(stringValue(awaitAskPayload["mode"]), "approval") {
			if hitlSummaries != 1 {
				t.Fatalf("expected one HITL summary user message for approval flow, got %#v", secondTurn)
			}
		} else if hitlSummaries != 0 {
			t.Fatalf("did not expect HITL summary user message for form flow, got %#v", secondTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}

	if toolName == "simple-bash" {
		client, ok := options.mcp.(*recordingMCPClient)
		if !ok {
			return streamBody.String(), nil
		}
		return streamBody.String(), append([]string(nil), client.commands...)
	}
	return streamBody.String(), append([]string(nil), sandbox.commands...)
}

func runSandboxBashQueryForResultShape(t *testing.T, sandbox contracts.SandboxClient) (string, []map[string]any) {
	t.Helper()

	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", "_bash_", map[string]any{
					"command":     "ls sample",
					"description": "列出 sample",
					"cwd":         "/workspace",
				}),
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"query complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox: sandbox,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"list sample","agentKey":"mock-runner"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case messages := <-secondTurnMessages:
		return rec.Body.String(), messages
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for second provider request, body=%s", rec.Body.String())
	}
	return "", nil
}

func defaultBashHITLCommand() string {
	return `mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"annual","start_date":"2026-04-20","end_date":"2026-04-22","days":3,"reason":"family_trip"}`
}

func payloadFromCommandForTest(t *testing.T, command string) map[string]any {
	t.Helper()
	idx := strings.Index(command, "--payload ")
	if idx < 0 {
		t.Fatalf("expected --payload in command %q", command)
	}
	raw := strings.TrimSpace(command[idx+len("--payload "):])
	if strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'") && len(raw) >= 2 {
		raw = raw[1 : len(raw)-1]
		raw = strings.ReplaceAll(raw, `'"'"'`, `'`)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload from command %q: %v", command, err)
	}
	return payload
}

func rebuildPayloadCommandForTest(t *testing.T, originalCommand string, payload map[string]any) string {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	idx := strings.Index(originalCommand, "--payload ")
	if idx < 0 {
		t.Fatalf("expected --payload in command %q", originalCommand)
	}
	return originalCommand[:idx+len("--payload ")] + "'" + strings.ReplaceAll(string(payloadJSON), "'", `'"'"'`) + "'"
}

func extractRunIDFromStream(t *testing.T, body string) string {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if runID, _ := message["runId"].(string); runID != "" {
			return runID
		}
	}
	t.Fatalf("expected runId in stream body: %s", body)
	return ""
}

func assertSpecificEventOrder(t *testing.T, messages []map[string]any, originalToolID string, awaitingID string) {
	t.Helper()
	originalStart := -1
	awaitAsk := -1
	requestSubmit := -1
	awaitingAnswer := -1
	originalResult := -1
	for idx, message := range messages {
		eventType, _ := message["type"].(string)
		switch eventType {
		case "tool.start":
			if message["toolId"] == originalToolID {
				originalStart = idx
			}
		case "awaiting.ask":
			if message["awaitingId"] == awaitingID {
				awaitAsk = idx
			}
		case "request.submit":
			if message["awaitingId"] == awaitingID {
				requestSubmit = idx
			}
		case "awaiting.answer":
			if message["awaitingId"] == awaitingID {
				awaitingAnswer = idx
			}
		case "tool.result":
			if message["toolId"] == originalToolID {
				originalResult = idx
			}
		}
	}
	if requestSubmit >= 0 {
		if !(originalStart >= 0 && awaitAsk > originalStart && requestSubmit > awaitAsk && awaitingAnswer > requestSubmit && originalResult > awaitingAnswer) {
			t.Fatalf("unexpected HITL event order: %#v", messages)
		}
		return
	}
	if !(originalStart >= 0 && awaitAsk > originalStart && awaitingAnswer > awaitAsk && originalResult > awaitingAnswer) {
		t.Fatalf("unexpected HITL event order: %#v", messages)
	}
}

func TestSubmitReturnsUnmatchedWhenNoActiveWaiter(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"missing-run","awaitingId":"missing-awaiting","params":[{"id":"q1","answer":"ok"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown awaitingId") {
		t.Fatalf("expected unknown awaitingId error, got %s", rec.Body.String())
	}
}

func mustEncodeSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestValidateSubmitParamsAllowsOrderedItemsWithoutIDs(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		itemCount int
		params    api.SubmitParams
	}{
		{
			name:      "question",
			mode:      "question",
			itemCount: 2,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"answer": "Weekend"},
				{"answers": []string{"产品更新", "使用教程"}},
			}),
		},
		{
			name:      "approval",
			mode:      "approval",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"decision": "approve"},
			}),
		},
		{
			name:      "approval batch",
			mode:      "approval",
			itemCount: 3,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"decision": "approve"},
				{"decision": "approve_prefix_run"},
				{"decision": "reject"},
			}),
		},
		{
			name:      "form",
			mode:      "form",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"payload": map[string]any{"days": 2}},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  tt.itemCount,
			}, tt.params)
			if err != nil {
				t.Fatalf("validateSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateSubmitParamsIgnoresSubmittedIDsWhenCountMatches(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		itemCount int
		params    api.SubmitParams
	}{
		{
			name:      "question",
			mode:      "question",
			itemCount: 2,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-1", "answer": "Weekend"},
				{"id": "wrong-2", "answer": 2},
			}),
		},
		{
			name:      "approval",
			mode:      "approval",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-cmd", "decision": "approve"},
			}),
		},
		{
			name:      "form",
			mode:      "form",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-form", "reason": "user_cancelled"},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  tt.itemCount,
			}, tt.params)
			if err != nil {
				t.Fatalf("validateSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateSubmitParamsRejectsCountMismatch(t *testing.T) {
	err := validateSubmitParams(contracts.AwaitingSubmitContext{
		AwaitingID: "await_1",
		Mode:       "question",
		ItemCount:  2,
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
	}))
	if err == nil || !strings.Contains(err.Error(), "expected 2 submit items, got 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSubmitParamsRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		item       map[string]any
		wantSubstr string
	}{
		{
			name:       "question decision",
			mode:       "question",
			item:       map[string]any{"decision": "approve"},
			wantSubstr: "items[0]: question items require exactly one of answer or answers",
		},
		{
			name:       "approval missing decision",
			mode:       "approval",
			item:       map[string]any{"reason": "nope"},
			wantSubstr: "items[0]: approval items require decision",
		},
		{
			name:       "form payload not object",
			mode:       "form",
			item:       map[string]any{"payload": "bad"},
			wantSubstr: "items[0]: form payload must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  1,
			}, mustEncodeSubmitParams(t, []map[string]any{tt.item}))
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func assertEventOrder(t *testing.T, body string, eventTypes ...string) {
	t.Helper()
	prev := -1
	for _, eventType := range eventTypes {
		needle := `"type":"` + eventType + `"`
		index := strings.Index(body, needle)
		if index < 0 {
			t.Fatalf("expected event %s in stream body: %s", eventType, body)
		}
		if index <= prev {
			t.Fatalf("expected event order %v in stream body: %s", eventTypes, body)
		}
		prev = index
	}
}

func TestServerRejectsInvalidLocalJWTConfigAtStartup(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: filepath.Join(fixture.cfg.Paths.ChatsDir, "missing.pem"),
	}

	_, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err == nil {
		t.Fatal("expected startup auth config error")
	}
	if !strings.Contains(err.Error(), "load local jwt public key") {
		t.Fatalf("expected local key error, got %v", err)
	}
}

func TestQueryAcceptsValidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "zenmind-local",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", rec.Body.String())
	}
}

func TestQueryRejectsInvalidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "wrong-issuer",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestQueryRejectsMissingBearerWhenLocalJWTEnabled(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestExecuteInternalQueryBypassesHTTPAuth(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	status, body, err := server.ExecuteInternalQuery(context.Background(), api.QueryRequest{
		Message:  "计划任务内部执行",
		AgentKey: "mock-runner",
	})
	if err != nil {
		t.Fatalf("execute internal query: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", body)
	}
}

func newServerFromFixture(t *testing.T, fixture testFixture) *Server {
	t.Helper()
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server
}

type testFixture struct {
	server          *Server
	cfg             config.Config
	chats           chat.Store
	memories        memory.Store
	registry        catalog.Registry
	runs            contracts.RunManager
	agent           contracts.AgentEngine
	tools           contracts.ToolExecutor
	sandbox         contracts.SandboxClient
	mcp             contracts.McpClient
	viewport        contracts.ViewportClient
	catalogReloader contracts.CatalogReloader
}

type testFixtureOptions struct {
	sandbox       contracts.SandboxClient
	mcp           contracts.McpClient
	mcpTools      stubMCPToolCatalog
	notifications contracts.NotificationSink
	configure     func(*config.Config)
	setupRuntime  func(root string, cfg *config.Config)
}

func newTestFixture(t *testing.T) testFixture {
	return newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runner "}}]}`,
			`{"choices":[{"delta":{"content":"test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
}

func newTestFixtureWithModelHandler(t *testing.T, modelHandler http.HandlerFunc) testFixture {
	return newTestFixtureWithModelHandlerAndOptions(t, modelHandler, testFixtureOptions{})
}

func newTestFixtureWithModelHandlerAndOptions(t *testing.T, modelHandler http.HandlerFunc, options testFixtureOptions) testFixture {
	t.Helper()
	root := t.TempDir()
	providerServer := newLoopbackServer(t, modelHandler)
	containerHubServer := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/environments/") && strings.HasSuffix(r.URL.Path, "/agent-prompt") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"environmentName":"shell","hasPrompt":true,"prompt":"Mock sandbox prompt"}`))
			return
		}
		http.NotFound(w, r)
	}))

	registriesDir := filepath.Join(root, "registries")
	agentsDir := filepath.Join(root, "agents")
	teamsDir := filepath.Join(root, "teams")
	skillsDir := filepath.Join(root, "skills-market")
	providersDir := filepath.Join(registriesDir, "providers")
	modelsDir := filepath.Join(registriesDir, "models")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, "mock-runner"), 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatalf("mkdir teams dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "mock-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + providerServer.URL,
		"apiKey: test-key",
		"defaultModel: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"isFunction: true",
		"isReasoner: false",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "mock-runner", "agent.yml"), []byte(strings.Join([]string{
		"key: mock-runner",
		"name: Mock Runner",
		"role: 测试代理",
		"description: test agent",
		"wonders:",
		"  - 帮我演示提问式确认",
		"  - |",
		"    帮我演示 Bash HITL 审批确认",
		"    并说明用户接下来会看到什么",
		"modelConfig:",
		"  modelKey: mock-model",
		"toolConfig:",
		"  tools:",
		"    - _datetime_",
		"    - _ask_user_question_",
		"skillConfig:",
		"  skills:",
		"    - mock-skill",
		"controls:",
		"  - key: tone",
		"    type: select",
		"    label: 输出语气",
		"    defaultValue: concise",
		"    options:",
		"      - value: concise",
		"        label: 简洁",
		"sandboxConfig:",
		"  environmentId: shell",
		"  level: RUN",
		"  env:",
		"    HTTP_PROXY: http://agent-proxy",
		"    TZ: Asia/Shanghai",
		"  extraMounts:",
		"    - platform: skills-market",
		"      destination: /skills",
		"      mode: ro",
		"mode: REACT",
		"budget:",
		"  tool:",
		"    timeoutMs: 210000",
		"react:",
		"  maxSteps: 6",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamsDir, "default.demo.yml"), []byte(strings.Join([]string{
		"name: Default Team",
		"defaultAgentKey: mock-runner",
		"agentKeys:",
		"  - mock-runner",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write team config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "mock-skill", "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill config: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Port: "18080",
		},
		Paths: config.PathsConfig{
			RegistriesDir:   registriesDir,
			AgentsDir:       agentsDir,
			TeamsDir:        teamsDir,
			SkillsMarketDir: skillsDir,
			ChatsDir:        filepath.Join(root, "custom-chats"),
			MemoryDir:       filepath.Join(root, "custom-memory"),
		},
		Auth: config.AuthConfig{
			Enabled: false,
		},
		ChatImage: config.ChatImageTokenConfig{
			ResourceTicketEnabled: false,
		},
		SSE: config.SSEConfig{
			IncludeToolPayloadEvents: true,
		},
		Defaults: config.DefaultsConfig{
			React: config.ReactDefaultsConfig{MaxSteps: 6},
		},
		Logging: config.LoggingConfig{
			Request: config.ToggleConfig{Enabled: true},
		},
		Skills: config.SkillCatalogConfig{
			CatalogConfig:  config.CatalogConfig{ExternalDir: skillsDir},
			MaxPromptChars: 8000,
		},
		Bash: config.BashConfig{
			WorkingDirectory:        root,
			AllowedPaths:            []string{root, "/tmp"},
			AllowedCommands:         []string{"pwd", "echo", "ls", "cat"},
			PathCheckedCommands:     []string{"ls", "cat"},
			PathCheckBypassCommands: []string{},
			ShellExecutable:         "bash",
			ShellTimeoutMs:          30000,
			MaxCommandChars:         16000,
		},
		ContainerHub: config.ContainerHubConfig{
			Enabled:          true,
			BaseURL:          containerHubServer.URL,
			RequestTimeoutMs: 1000,
			ResolvedEngine:   "local",
		},
	}
	if options.configure != nil {
		options.configure(&cfg)
	}
	if options.setupRuntime != nil {
		options.setupRuntime(root, &cfg)
	}

	chats, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(cfg.Paths.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	sandboxClient := options.sandbox
	if sandboxClient == nil {
		sandboxClient = contracts.NewNoopSandboxClient()
	}
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, chats, memories, nil)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	mcp := options.mcp
	if mcp == nil {
		mcp = contracts.NewNoopMcpClient()
	}
	frontendRegistry := frontendtools.NewDefaultRegistry()
	var mcpTools interface {
		Definitions() []api.ToolDetailResponse
		Tool(name string) (api.ToolDetailResponse, bool)
	}
	if len(options.mcpTools.defs) > 0 {
		mcpTools = options.mcpTools
	}
	toolExecutor := tools.NewToolRouter(backendTools, mcp, mcpTools, llm.NewFrontendSubmitCoordinator(frontendRegistry), contracts.NewNoopActionInvoker())
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	notifications := options.notifications
	if notifications == nil {
		notifications = contracts.NewNoopNotificationSink()
	}
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, nil, notifications)

	runs := runctl.NewInMemoryRunManager()
	sandbox := sandboxClient
	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, frontendRegistry, sandbox)
	viewport := contracts.NewNoopViewportClient()
	server, err := New(Dependencies{
		Config:          cfg,
		Chats:           chats,
		Memory:          memories,
		Registry:        registry,
		Models:          modelRegistry,
		Runs:            runs,
		Agent:           agentEngine,
		Tools:           toolExecutor,
		Sandbox:         sandbox,
		MCP:             mcp,
		FrontendTools:   frontendRegistry,
		Viewport:        viewport,
		CatalogReloader: reloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	return testFixture{
		server:          server,
		cfg:             cfg,
		chats:           chats,
		memories:        memories,
		registry:        registry,
		runs:            runs,
		agent:           agentEngine,
		tools:           toolExecutor,
		sandbox:         sandbox,
		mcp:             mcp,
		viewport:        viewport,
		catalogReloader: reloader,
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

type loopbackServer struct {
	URL    string
	server *http.Server
	ln     net.Listener
}

func (s *loopbackServer) Close() {
	if s == nil {
		return
	}
	if s.server != nil {
		_ = s.server.Close()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func newLoopbackServer(t *testing.T, handler http.Handler) *loopbackServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback server: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	result := &loopbackServer{
		URL:    "http://" + listener.Addr().String(),
		server: server,
		ln:     listener,
	}
	t.Cleanup(result.Close)
	return result
}

func writeProviderSSE(t *testing.T, w http.ResponseWriter, frames ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("expected flusher")
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
			t.Fatalf("write sse frame: %v", err)
		}
		flusher.Flush()
	}
}

func providerToolCallFrame(t *testing.T, toolID string, toolName string, args map[string]any) string {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	frame, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{
						map[string]any{
							"index": 0,
							"id":    toolID,
							"type":  "function",
							"function": map[string]any{
								"name":      toolName,
								"arguments": string(argsJSON),
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal provider tool call frame: %v", err)
	}
	return string(frame)
}

func mustEncryptProviderAPIKeyForServerTest(t *testing.T, envPart string, plaintext string) string {
	t.Helper()

	const providerAPIKeyCodePart = "zenmind-provider"

	sum := sha256.Sum256([]byte(providerAPIKeyCodePart + ":" + envPart))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}

	nonce := []byte("0123456789ab")
	data := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(append([]byte{}, nonce...), data...)
	return "AES(" + base64.RawURLEncoding.EncodeToString(payload) + ")"
}

func assertPersistedEventTypes(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	seen := make(map[string]int)
	for _, event := range events {
		eventType := event.Type
		seen[eventType]++
	}
	for _, eventType := range want {
		if seen[eventType] == 0 {
			t.Fatalf("expected persisted event type %q, got %#v", eventType, events)
		}
	}
	for _, eventType := range disallowedPersistedEventTypes {
		if seen[eventType] > 0 {
			t.Fatalf("did not expect persisted event type %q, got %#v", eventType, events)
		}
	}
}

type scriptedRoundTripper struct {
	handler http.HandlerFunc
}

func (r scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.handler(rec, req)
	result := rec.Result()
	return &http.Response{
		StatusCode: result.StatusCode,
		Status:     result.Status,
		Header:     result.Header.Clone(),
		Body:       result.Body,
		Request:    req,
	}, nil
}

func newScriptedHTTPClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{Transport: scriptedRoundTripper{handler: handler}}
}

func writeTestJWTKeyPair(t *testing.T, dir string) (*rsa.PrivateKey, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	path := filepath.Join(dir, "test-public-key.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(path, block, 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	return privateKey, path
}

func mustSignRS256JWT(t *testing.T, privateKey *rsa.PrivateKey, payload map[string]any) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func decodeSSEMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	lines := strings.Split(body, "\n")
	messages := make([]map[string]any, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		var msg map[string]any
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			t.Fatalf("decode sse message %q: %v", payload, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func findToolResultPayload(t *testing.T, body string, toolID string) map[string]any {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if message["type"] == "tool.result" && message["toolId"] == toolID {
			return message
		}
	}
	t.Fatalf("expected tool.result for %s in body %s", toolID, body)
	return nil
}

func findToolMessageContent(t *testing.T, messages []map[string]any, toolName string) string {
	t.Helper()
	for _, message := range messages {
		if message["role"] != "tool" || message["name"] != toolName {
			continue
		}
		content, _ := message["content"].(string)
		if content != "" {
			return content
		}
	}
	t.Fatalf("expected tool message for %s in %#v", toolName, messages)
	return ""
}

func decodeSSEPayloadStrings(body string) []string {
	lines := strings.Split(body, "\n")
	payloads := make([]string, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
	}
	return payloads
}

func decodeSSELine(t *testing.T, line string) map[string]any {
	t.Helper()
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	var message map[string]any
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode sse line %q: %v", line, err)
	}
	return message
}

func normalizeProviderMessages(value any) []map[string]any {
	items, _ := value.([]any)
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		message, _ := item.(map[string]any)
		messages = append(messages, message)
	}
	return messages
}

func assertSSEMessagesHaveSeqAndTimestamp(t *testing.T, body string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got body %s", body)
	}
	prevSeq := 0.0
	for _, msg := range messages {
		seq, ok := msg["seq"].(float64)
		if !ok || seq <= prevSeq {
			t.Fatalf("expected ascending seq, got %#v", messages)
		}
		prevSeq = seq
		if _, ok := msg["type"].(string); !ok {
			t.Fatalf("expected type field, got %#v", msg)
		}
		if ts, ok := msg["timestamp"].(float64); !ok || ts <= 0 {
			t.Fatalf("expected positive timestamp, got %#v", msg)
		}
	}
}

func assertSSEEventOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) < len(want) {
		t.Fatalf("expected at least %d messages, got %#v", len(want), messages)
	}
	for idx, eventType := range want {
		if messages[idx]["type"] != eventType {
			t.Fatalf("event %d: expected %s, got %#v", idx, eventType, messages[idx])
		}
	}
}

func assertSSEPayloadOrder(t *testing.T, body string, eventType string, parts []string) {
	t.Helper()
	for _, payload := range decodeSSEPayloadStrings(body) {
		if !strings.Contains(payload, `"type":"`+eventType+`"`) {
			continue
		}
		assertOrderedSubstrings(t, payload, parts)
		return
	}
	t.Fatalf("expected sse event type %s in body %s", eventType, body)
}

func assertBodyContainsOrderedEvent(t *testing.T, body string, marker string, parts []string) {
	t.Helper()
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("expected marker %q in body %s", marker, body)
	}
	start := strings.LastIndex(body[:index], "{")
	end := strings.Index(body[index:], "}")
	if start < 0 || end < 0 {
		t.Fatalf("expected json object around marker %q in body %s", marker, body)
	}
	assertOrderedSubstrings(t, body[start:index+end+1], parts)
}

func assertOrderedSubstrings(t *testing.T, body string, parts []string) {
	t.Helper()
	prev := -1
	for _, part := range parts {
		idx := strings.Index(body, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, body)
		}
		if idx <= prev {
			t.Fatalf("expected ordered substrings %v in %s", parts, body)
		}
		prev = idx
	}
}

func assertUUIDLike(t *testing.T, value string) {
	t.Helper()
	parts := strings.Split(value, "-")
	if len(parts) != 5 {
		t.Fatalf("expected uuid-like value, got %q", value)
	}
	lengths := []int{8, 4, 4, 4, 12}
	for idx, part := range parts {
		if len(part) != lengths[idx] {
			t.Fatalf("expected uuid-like value, got %q", value)
		}
	}
}

func decodeEventTypesFromSSE(t *testing.T, body string) []string {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	types := make([]string, 0, len(messages))
	for _, message := range messages {
		eventType, _ := message["type"].(string)
		if eventType != "" {
			types = append(types, eventType)
		}
	}
	return types
}

func assertEventTypesInclude(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	assertStringSliceContains(t, got, want...)
}

func assertStringSliceContains(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, target := range want {
		found := false
		for _, item := range got {
			if item == target {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in %#v", target, got)
		}
	}
}

func assertStringSliceExcludes(t *testing.T, got []string, blocked ...string) {
	t.Helper()
	for _, target := range blocked {
		for _, item := range got {
			if item == target {
				t.Fatalf("did not expect %q in %#v", target, got)
			}
		}
	}
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
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"_ask_user_question_","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue with the request\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
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
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingIntervalMs = 30000
			if configure != nil {
				configure(cfg)
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-runner", "agent.yml")
			if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
				"key: mock-runner",
				"name: Mock Runner",
				"role: 测试代理",
				"description: test agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - _ask_user_question_",
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
	resp, err := http.Post(server.URL+"/api/query", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-runner","message":"please confirm first"}`))
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
