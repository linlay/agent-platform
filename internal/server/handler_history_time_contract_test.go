package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/ws"
)

func TestCompactAndBTWRejectInvalidHistoricalJSONLWith422(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	const chatID = "chat-maintenance-time-contract"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl"), []byte(`{"_type":"compact.checkpoint","chatId":"`+chatID+`","compactId":"bad","updatedAt":0}`+"\n"), 0o644); err != nil {
		t.Fatalf("write invalid history: %v", err)
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/compact", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"mock-agent"}`)),
		httptest.NewRequest(http.MethodPost, "/api/btw", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"inspect"}`)),
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, request)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
			t.Fatalf("expected 422 time_contract_violation for %s, status=%d body=%s", request.URL.Path, rec.Code, rec.Body.String())
		}
	}

	conn := dialTestWS(t, fixture.server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/compact",
		ID:      "compact-jsonl-time-contract",
		Payload: ws.MarshalPayload(map[string]any{"chatId": chatID, "agentKey": "mock-agent"}),
	}); err != nil {
		t.Fatalf("write WS compact: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read WS error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "compact-jsonl-time-contract" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("unexpected WS time violation: %#v", frame)
	}
}
