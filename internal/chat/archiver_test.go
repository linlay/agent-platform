package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiverMovesChatToArchiveAndPreservesAttachments(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archive)

	if _, _, err := active.EnsureChat("chat-archiver", "agent-a", "", "hello archive"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := active.AppendQueryLine("chat-archiver", QueryLine{
		ChatID:    "chat-archiver",
		RunID:     "run-archiver",
		UpdatedAt: 1000,
		Query:     map[string]any{"role": "user", "message": "hello archive"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := active.AppendStepLine("chat-archiver", StepLine{
		ChatID:    "chat-archiver",
		RunID:     "run-archiver",
		UpdatedAt: 2000,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "archived response"}},
		}},
		Type: "react",
		Usage: map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 2,
			"total_tokens":      3,
		},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := active.OnRunCompleted(RunCompletion{
		ChatID:          "chat-archiver",
		RunID:           "run-archiver",
		AgentKey:        "agent-a",
		AssistantText:   "archived response",
		InitialMessage:  "hello archive",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
		Usage:           UsageData{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-archiver"), "artifact.txt"), []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-archiver"), "events.jsonl"), []byte(`{"type":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-archiver"), "raw_messages.jsonl"), []byte(`{"role":"user","content":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write raw messages: %v", err)
	}

	if err := archiver.ArchiveChat("chat-archiver"); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	if sum, err := active.Summary("chat-archiver"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum != nil {
		t.Fatalf("expected active summary removed, got %#v", sum)
	}
	if _, err := os.Stat(filepath.Join(root, "chat-archiver.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected active jsonl removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.ChatDir("chat-archiver"), "artifact.txt")); err != nil {
		t.Fatalf("expected artifact moved to archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.ChatDir("chat-archiver"), "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected events sidecar removed from archive attachments, got %v", err)
	}

	loaded, err := archive.LoadArchived("chat-archiver")
	if err != nil {
		t.Fatalf("load archived: %v", err)
	}
	if !loaded.Summary.HasAttachments || loaded.Summary.AgentKey != "agent-a" || len(loaded.Detail.Events) == 0 {
		t.Fatalf("unexpected archived chat: %#v", loaded)
	}
	if loaded.EventsContent == "" || loaded.RawMessagesContent == "" {
		t.Fatalf("expected sidecar contents stored in archive DB")
	}
}

func TestArchiverLeavesActiveChatWhenArchiveAlreadyExists(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	if _, _, err := active.EnsureChat("chat-duplicate-archive", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure active chat: %v", err)
	}
	if err := archive.ArchiveChat(testArchivedChat("chat-duplicate-archive", "agent-a", "hello", "done")); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	err = NewArchiver(active, archive).ArchiveChat("chat-duplicate-archive")
	if !errors.Is(err, ErrChatAlreadyArchived) {
		t.Fatalf("expected ErrChatAlreadyArchived, got %v", err)
	}
	if sum, err := active.Summary("chat-duplicate-archive"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum == nil {
		t.Fatalf("expected active chat to remain")
	}
}
