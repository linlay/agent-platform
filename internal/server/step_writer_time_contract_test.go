package server

import (
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

func TestRunEventProcessorSurfacesInvalidPersistedQueryMessageTime(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	defer store.Close()

	const (
		chatID    = "chat-query-message-time"
		runID     = "run-query-message-time"
		timestamp = int64(1_700_000_000_001)
	)
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := chat.NewStepWriter(store, chatID, runID, "REACT")
	writer.SetPendingQueryMessages([]map[string]any{{
		"role":    "user",
		"content": "hello",
		"ts":      int64(0),
	}})
	processor := runEventProcessor{stepWriter: writer}
	_, _, err = processor.Consume(stream.StreamEvent{
		Seq:       1,
		Type:      "request.query",
		Timestamp: timestamp,
		Payload: map[string]any{
			"chatId":  chatID,
			"runId":   runID,
			"role":    "user",
			"message": "hello",
		},
	})
	if !timecontract.IsViolation(err) {
		t.Fatalf("processor error = %v, want time contract violation", err)
	}
	if !timecontract.IsViolation(writer.Err()) {
		t.Fatalf("writer error = %v, want time contract violation", writer.Err())
	}
}
