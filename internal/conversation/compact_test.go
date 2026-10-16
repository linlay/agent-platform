package conversation

import (
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/modelclient"
)

type recordingCompactStore struct {
	snapshot    chat.CompactSnapshot
	snapshotErr error
	records     []chat.CompactRecordLine
}

func (s *recordingCompactStore) BuildCompactSnapshot(string, int) (chat.CompactSnapshot, error) {
	return s.snapshot, s.snapshotErr
}

func (s *recordingCompactStore) CommitCompactCheckpoint(string, chat.CompactSnapshot, chat.CompactCheckpointLine) error {
	return nil
}

func (s *recordingCompactStore) BuildL1CompactSnapshot(string, chat.L1Options) (chat.ToolCompactSnapshot, error) {
	return chat.ToolCompactSnapshot{}, chat.ErrNoCompactableHistory
}

func (s *recordingCompactStore) CommitToolCompact(string, chat.ToolCompactSnapshot, chat.ToolCompactLine) error {
	return nil
}

func (s *recordingCompactStore) AppendCompactRecord(chatID string, line chat.CompactRecordLine) error {
	line.ChatID = chatID
	s.records = append(s.records, line)
	return nil
}

func TestCompactHistoryRecordsSummaryFailureCause(t *testing.T) {
	store := &recordingCompactStore{snapshot: chat.CompactSnapshot{
		PreCompactEstimatedTokens: 5000,
		CoveredMessages:           []map[string]any{{"role": "user", "content": "old question"}, {"role": "assistant", "content": "old answer"}},
	}}
	base := api.CompactResponse{RequestID: "req-1", ChatID: "chat-1", Trigger: "manual", Level: "summary", Scope: "history"}
	resp, err := CompactHistory(store, base, 128000, 1000, "compact-1", func(string, int) (string, map[string]any, error) {
		return "", nil, modelclient.StreamTimeoutError(60 * time.Second)
	})
	if err != nil || resp.Detail != "summary_model_failed" {
		t.Fatalf("CompactHistory = %#v, %v", resp, err)
	}
	if len(store.records) != 1 {
		t.Fatalf("records = %#v", store.records)
	}
	record := store.records[0]
	if record.Status != "failed" || record.Scope != "history" || record.CompactID != "compact-1" || record.ChatID != "chat-1" ||
		record.StartedAt <= 0 || record.UpdatedAt < record.StartedAt || record.ContextWindow != 128000 ||
		record.Error["code"] != "provider_timeout" || record.Error["message"] != "model stream idle timeout after 60 seconds" {
		t.Fatalf("record = %#v", record)
	}
}

func TestCompactHistoryRecordsSkippedAttempt(t *testing.T) {
	store := &recordingCompactStore{snapshotErr: chat.ErrNoCompactableHistory}
	base := api.CompactResponse{RequestID: "req-2", ChatID: "chat-1", Trigger: "manual", Level: "summary", Scope: "history"}
	if _, err := CompactHistory(store, base, 128000, 0, "compact-2", nil); err != nil {
		t.Fatalf("CompactHistory: %v", err)
	}
	if len(store.records) != 1 || store.records[0].Status != "skipped" || store.records[0].Detail != "no_compactable_history" || store.records[0].Error != nil {
		t.Fatalf("records = %#v", store.records)
	}
}
