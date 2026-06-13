package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func TestHandleCompactWritesCheckpointAndReloadsRawMessages(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"compact model "}}]}`,
			`{"choices":[{"delta":{"content":"summary"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	chatID := "chat-api-compact"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	appendServerCompactRun(t, fixture.chats, chatID, "r1", "user r1", "assistant r1")
	appendServerCompactRun(t, fixture.chats, chatID, "r2", "user r2", "assistant r2")
	appendServerCompactRun(t, fixture.chats, chatID, "r3", "user r3", "assistant r3")
	appendServerCompactRun(t, fixture.chats, chatID, "r4", "user r4", "assistant r4")

	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","agentKey":"mock-agent","requestId":"req-compact","trigger":"manual"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || !response.Data.Accepted || response.Data.Status != "completed" {
		t.Fatalf("unexpected compact response: %#v", response)
	}
	if response.Data.SummarySource != "model" {
		t.Fatalf("summarySource = %q, want model", response.Data.SummarySource)
	}

	raw, err := fixture.chats.LoadRawMessages(chatID, 1)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if len(raw) != 5 {
		t.Fatalf("raw len = %d, want summary + two tail runs", len(raw))
	}
	firstContent, _ := raw[0]["content"].(string)
	if !strings.Contains(firstContent, "compact model summary") {
		t.Fatalf("first raw content = %q", firstContent)
	}
	for _, msg := range raw {
		content, _ := msg["content"].(string)
		if strings.Contains(content, "r1") || strings.Contains(content, "r2") {
			t.Fatalf("compacted content leaked into raw messages: %#v", msg)
		}
	}
}

func appendServerCompactRun(t *testing.T, store chat.Store, chatID string, runID string, userText string, assistantText string) {
	t.Helper()
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 100,
		Query:     map[string]any{"role": "user", "message": userText},
	}); err != nil {
		t.Fatalf("AppendQueryLine(%s): %v", runID, err)
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 101,
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: assistantText}},
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
}
