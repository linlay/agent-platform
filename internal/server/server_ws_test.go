package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"io"
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
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

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
