package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestHandleChatJSONLReturnsActiveRawContent(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-jsonl-active"
	seedSearchableChat(t, fixture.chats, chatID)
	want, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw jsonl: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="chat-jsonl-active.jsonl"` {
		t.Fatalf("content-disposition=%q", got)
	}
	if rec.Body.String() != want {
		t.Fatalf("raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLFallsBackToArchiveRawContent(t *testing.T) {
	server, active, _ := newArchiveHandlerTestServer(t, nil)
	chatID := "chat-jsonl-archive"
	seedArchiveHandlerChat(t, active, chatID)
	want, err := active.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load active raw jsonl: %v", err)
	}

	archiveRec := httptest.NewRecorder()
	server.ServeHTTP(archiveRec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", strings.NewReader(`{"chatIds":["`+chatID+`"]}`)))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != want {
		t.Fatalf("archived raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLValidationAndNotFound(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: "/api/chat/jsonl", code: http.StatusBadRequest},
		{name: "invalid", path: "/api/chat/jsonl?chatId=../chat", code: http.StatusBadRequest},
		{name: "not found", path: "/api/chat/jsonl?chatId=missing-chat", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestHandleChatLLMTraceReturnsRawContent(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "llm/run_trace_001.json"
	want := `{"runId":"run_trace","status":"ok"}` + "\n"
	seedLLMTraceFile(t, fixture, fileParam, want)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/llm-trace?file=llm%2Frun_trace_001.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="run_trace_001.json"` {
		t.Fatalf("content-disposition=%q", got)
	}
	if rec.Body.String() != want {
		t.Fatalf("raw llm trace mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatLLMTraceValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: "/api/chat/llm-trace", code: http.StatusBadRequest},
		{name: "path traversal", path: "/api/chat/llm-trace?file=llm%2F..%2Frun_001.json", code: http.StatusBadRequest},
		{name: "wrong directory", path: "/api/chat/llm-trace?file=chat_1%2Frun_001.json", code: http.StatusBadRequest},
		{name: "wrong extension", path: "/api/chat/llm-trace?file=llm%2Frun_001.txt", code: http.StatusBadRequest},
		{name: "missing trace", path: "/api/chat/llm-trace?file=llm%2Fmissing_001.json", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestWSChatJSONLReturnsRawContent(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	chatID := "chat-jsonl-ws"
	seedSearchableChat(t, fixture.chats, chatID)
	want, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw jsonl: %v", err)
	}
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/jsonl",
		ID:      "req_raw_jsonl",
		Payload: ws.MarshalPayload(map[string]any{"chatId": chatID}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.ID != "req_raw_jsonl" || frame.Code != 0 {
		t.Fatalf("unexpected response frame: %#v", frame)
	}
	data, ok := frame.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %#v", frame.Data)
	}
	if data != want {
		t.Fatalf("raw jsonl mismatch\nwant: %q\ngot:  %q", want, data)
	}
}

func TestWSChatJSONLValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	for _, tc := range []struct {
		name    string
		id      string
		payload map[string]any
		code    int
		typeKey string
	}{
		{name: "missing", id: "req_missing", payload: map[string]any{}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "invalid", id: "req_invalid", payload: map[string]any{"chatId": "../chat"}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "not found", id: "req_not_found", payload: map[string]any{"chatId": "missing-chat"}, code: http.StatusNotFound, typeKey: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/chat/jsonl",
				ID:      tc.id,
				Payload: ws.MarshalPayload(tc.payload),
			}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != tc.id || frame.Type != tc.typeKey || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %#v", frame)
			}
		})
	}
}

func TestWSChatLLMTraceReturnsRawContent(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "llm/run_trace_ws_001.json"
	want := `{"runId":"run_trace_ws","status":"ok"}` + "\n"
	seedLLMTraceFile(t, fixture, fileParam, want)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/llm-trace",
		ID:      "req_raw_llm_trace",
		Payload: ws.MarshalPayload(map[string]any{"file": fileParam}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.ID != "req_raw_llm_trace" || frame.Code != 0 {
		t.Fatalf("unexpected response frame: %#v", frame)
	}
	data, ok := frame.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %#v", frame.Data)
	}
	if data != want {
		t.Fatalf("raw llm trace mismatch\nwant: %q\ngot:  %q", want, data)
	}
}

func TestWSChatLLMTraceValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	for _, tc := range []struct {
		name    string
		id      string
		payload map[string]any
		code    int
		typeKey string
	}{
		{name: "missing", id: "req_missing_llm_trace", payload: map[string]any{}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "invalid", id: "req_invalid_llm_trace", payload: map[string]any{"file": "../trace.json"}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "not found", id: "req_not_found_llm_trace", payload: map[string]any{"file": "llm/missing_001.json"}, code: http.StatusNotFound, typeKey: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/chat/llm-trace",
				ID:      tc.id,
				Payload: ws.MarshalPayload(tc.payload),
			}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != tc.id || frame.Type != tc.typeKey || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %#v", frame)
			}
		})
	}
}

func TestLoadJSONLContentRejectsInvalidChatID(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.LoadJSONLContent("../chat"); err == nil {
		t.Fatalf("expected invalid chatId error")
	}
}

func dialTestWS(t *testing.T, server *Server) *gws.Conn {
	t.Helper()
	testServer := httptest.NewServer(server)
	t.Cleanup(testServer.Close)
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	readConnectedPush(t, conn)
	return conn
}

func newChatExportWSTestFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.Logging.LLMInteraction.RecordDir = cfg.Paths.ChatsDir
		},
	})
}

func seedLLMTraceFile(t *testing.T, fixture testFixture, fileParam string, content string) {
	t.Helper()
	relativeFile, _, err := validateLLMTraceFileParam(fileParam)
	if err != nil {
		t.Fatalf("validate llm trace file: %v", err)
	}
	target := filepath.Join(fixture.cfg.Logging.LLMInteraction.RecordDir, filepath.FromSlash(relativeFile))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir llm trace dir: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write llm trace file: %v", err)
	}
}

func TestRenderChatMarkdownSkipsAutomationQuery(t *testing.T) {
	markdown := renderChatMarkdown("Automation", "agent-a", []stream.EventData{
		{
			Type:      "request.query",
			Timestamp: 100,
			Payload: map[string]any{
				"message": "Secret automation prompt",
				"role":    "automation",
			},
		},
		{
			Type:      "content.snapshot",
			Timestamp: 200,
			Payload: map[string]any{
				"text": "Automation result",
			},
		},
	})

	if strings.Contains(markdown, "Secret automation prompt") || strings.Contains(markdown, "## User") {
		t.Fatalf("expected automation query to be omitted, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "Automation result") {
		t.Fatalf("expected assistant content to remain, got:\n%s", markdown)
	}
}
