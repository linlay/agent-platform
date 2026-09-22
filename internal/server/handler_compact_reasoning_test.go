package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleL1ReasoningOnlyWithoutModel(t *testing.T) {
	modelCalls := 0
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) { modelCalls++; writeProviderSSE(t, w, `[DONE]`) })
	id := "chat-l1-reasoning"
	if _, _, err := fixture.chats.EnsureChat(id, "mock-agent", "", "reasoning"); err != nil {
		t.Fatal(err)
	}
	appendServerCompactRun(t, fixture.chats, id, "r", "question", "answer")
	if err := fixture.chats.(*chat.FileStore).AppendRunCompactCheckpoint(id, chat.RunCompactCheckpointLine{Type: chat.RunCompactCheckpointLineType, ChatID: id, RunID: "r", CompactID: "cp", UpdatedAt: 1700000000000, Messages: []map[string]any{{"role": "assistant", "content": "answer", "reasoning_content": strings.Repeat("reason ", 6000)}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		appendServerCompactRun(t, fixture.chats, id, fmt.Sprint("recent-", i), "recent question", "recent answer")
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", bytes.NewBufferString(`{"chatId":"`+id+`","agentKey":"mock-agent","level":"l1_tools","trigger":"manual"}`)))
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !response.Data.Accepted || response.Data.Status != "completed" || response.Data.ReasoningCleared != 1 || response.Data.ToolsCleared != 0 || modelCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, modelCalls, rec.Body.String())
	}
}

// Large compaction fixtures must not accidentally exercise the repetition guard.
func compactTestDistinctText(prefix string, count int) string {
	var out strings.Builder
	out.WriteString(prefix + "\n")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&out, "%x\n", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", prefix, i))))
	}
	return out.String()
}
