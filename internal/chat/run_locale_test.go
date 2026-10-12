package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestRunLocaleIsStoredOnQueryLineAndNotReplayed(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID, runID = "chat-locale", "run-locale"
	if _, _, err := store.EnsureChat(chatID, "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := store.OnRunStarted(RunStart{ChatID: chatID, RunID: runID, AgentKey: "agent", StartedAtMillis: testEpochMillis(1)}); err != nil {
		t.Fatal(err)
	}
	writer := NewStepWriter(store, chatID, runID, "GENERAL")
	writer.SetRunLocale("en")
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello", "ts": testEpochMillis(1)}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{"role": "user", "message": "hello", "runId": runID, "chatId": chatID}).Data())

	query, err := store.LoadRunQuery(chatID, runID)
	if err != nil || query == nil || query.Locale != "en" {
		t.Fatalf("stored locale = %+v, %v", query, err)
	}
	if _, leaked := query.Query["locale"]; leaked {
		t.Fatal("locale entered the public query object")
	}
	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, _ := json.Marshal(detail.Events)
	if strings.Contains(string(replayed), `"locale"`) {
		t.Fatalf("locale replayed to clients: %s", replayed)
	}
}
