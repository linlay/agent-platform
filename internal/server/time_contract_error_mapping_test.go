package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/skills"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type timeContractRecentChatsStore struct {
	chat.Store
	err error
}

func (s timeContractRecentChatsStore) RecentChatsByAgent(string, int) ([]chat.Summary, error) {
	return nil, s.err
}

type timeContractListChatsStore struct {
	chat.Store
	err error
}

func (s timeContractListChatsStore) ListChats(string, string) ([]chat.Summary, error) {
	return nil, s.err
}

type timeContractFeedbackStore struct {
	chat.Store
	err error
}

func (s timeContractFeedbackStore) SetFeedback(string, string, string, string) (int64, error) {
	return 0, s.err
}

type timeContractEnsureChatStore struct {
	chat.Store
	err error
}

func (s timeContractEnsureChatStore) EnsureChat(string, string, string, string) (chat.Summary, bool, error) {
	return chat.Summary{}, false, s.err
}

func testTimeContractViolation(field string) error {
	return &timecontract.Violation{
		Field:    field,
		Location: "test.persisted",
		Reason:   "invalid historic value",
	}
}

func assertHTTPTimeContractViolation(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	details, _ := response.Data["error"].(map[string]any)
	if response.Code != http.StatusUnprocessableEntity || details["code"] != "time_contract_violation" || details["field"] != field || details["expected"] != timecontract.Expected {
		t.Fatalf("unexpected time contract response: %#v", response)
	}
}

func TestAdminAgentOrderInvalidPersistedTimeReturns422(t *testing.T) {
	fixture := newTestFixture(t)
	path := catalog.AgentOrderPath(fixture.cfg.Paths.AgentsDir)
	if err := os.WriteFile(path, []byte(`{"version":1,"order":[],"updatedAt":1700000000}`), 0o644); err != nil {
		t.Fatalf("write legacy agent order: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/order", nil))
	assertHTTPTimeContractViolation(t, rec, "updatedAt")
}

func TestHTTPTimeContractViolationsAreNotDowngradedTo500(t *testing.T) {
	fixture := newTestFixture(t)
	violation := testTimeContractViolation("updatedAt")

	fixture.server.deps.Chats = timeContractRecentChatsStore{Store: fixture.chats, err: violation}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?includeChats=1", nil))
	assertHTTPTimeContractViolation(t, rec, "updatedAt")

	fixture.server.deps.Chats = timeContractFeedbackStore{Store: fixture.chats, err: violation}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`{"chatId":"chat-1","runId":"run-1","type":"thumbs_down"}`)))
	assertHTTPTimeContractViolation(t, rec, "updatedAt")
}

func TestUploadTimeContractViolationReturns422(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.server.deps.Chats = timeContractEnsureChatStore{Store: fixture.chats, err: testTimeContractViolation("createdAt")}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chatId", "chat-upload"); err != nil {
		t.Fatalf("write multipart field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	assertHTTPTimeContractViolation(t, rec, "createdAt")
}

func TestWebSocketTimeContractViolationsAreNotDowngradedTo500(t *testing.T) {
	fixture := newTestFixture(t)
	hub := ws.NewHub()
	fixture.server.deps.Config.WebSocket.WriteQueueSize = 4
	fixture.server.deps.Config.WebSocket.PingInterval = 30000
	fixture.server.wsHandler = fixture.server.newWSHandler(hub)
	fixture.server.router.Handle("/ws", fixture.server.wsHandler)
	fixture.server.wsHandler.RegisterRoute("/api/test/time-agent", func(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
		fixture.server.sendAgentWSError(conn, req, testTimeContractViolation("updatedAt"))
	})

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	fixture.server.deps.Chats = timeContractRecentChatsStore{Store: fixture.chats, err: testTimeContractViolation("updatedAt")}
	writeTimeContractWSRequest(t, conn, "/api/agents", "agents_time", map[string]any{"includeChats": 1})
	assertWSTimeContractViolation(t, conn, "agents_time", "updatedAt")

	fixture.server.deps.Chats = timeContractListChatsStore{Store: fixture.chats, err: testTimeContractViolation("createdAt")}
	writeTimeContractWSRequest(t, conn, "/api/chats", "chats_time", map[string]any{})
	assertWSTimeContractViolation(t, conn, "chats_time", "createdAt")

	fixture.server.deps.Chats = timeContractFeedbackStore{Store: fixture.chats, err: testTimeContractViolation("updatedAt")}
	writeTimeContractWSRequest(t, conn, "/api/feedback", "feedback_time", map[string]any{"chatId": "chat-1", "runId": "run-1", "type": "thumbs_down"})
	assertWSTimeContractViolation(t, conn, "feedback_time", "updatedAt")

	writeTimeContractWSRequest(t, conn, "/api/test/time-agent", "agent_helper_time", map[string]any{})
	assertWSTimeContractViolation(t, conn, "agent_helper_time", "updatedAt")
}

func writeTimeContractWSRequest(t *testing.T, conn *gws.Conn, frameType, id string, payload map[string]any) {
	t.Helper()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    frameType,
		ID:      id,
		Payload: ws.MarshalPayload(payload),
	}); err != nil {
		t.Fatalf("write websocket %s request: %v", frameType, err)
	}
}

func assertWSTimeContractViolation(t *testing.T, conn *gws.Conn, id, field string) {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		return json.Unmarshal(data, &meta) == nil && meta.Frame == ws.FrameError && meta.ID == id
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket error: %v", err)
	}
	data, _ := frame.Data.(map[string]any)
	if frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" || data["code"] != "time_contract_violation" || data["field"] != field || data["expected"] != timecontract.Expected {
		t.Fatalf("unexpected websocket time contract error: %#v", frame)
	}
}

func TestSkillCandidatesInvalidPersistedTimeReturns422(t *testing.T) {
	storeRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(storeRoot, "legacy.json"), []byte(`{"id":"legacy","createdAt":"1700000000000","updatedAt":1700000000000}`), 0o644); err != nil {
		t.Fatalf("write legacy candidate: %v", err)
	}
	store, err := skills.NewFileCandidateStore(storeRoot)
	if err != nil {
		t.Fatalf("new candidate store: %v", err)
	}
	fixture := newTestFixture(t)
	fixture.server.deps.SkillCandidates = store
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/skill-candidates", nil))
	assertHTTPTimeContractViolation(t, rec, "createdAt")
}
