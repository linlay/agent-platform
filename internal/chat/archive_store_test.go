package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveStoreArchiveListLoadSearchAndDelete(t *testing.T) {
	root := t.TempDir()
	store, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archived := testArchivedChat("chat-archive-store", "agent-a", "Archive topic", "final archive answer")
	archived.Summary.UpdatedAt = 2500
	if err := store.ArchiveChat(archived); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	if err := store.ArchiveChat(archived); !errors.Is(err, ErrChatAlreadyArchived) {
		t.Fatalf("expected ErrChatAlreadyArchived, got %v", err)
	}

	items, total, err := store.ListArchived("agent-a", 50, 0)
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one archived item, got total=%d len=%d", total, len(items))
	}
	if !items[0].HasAttachments || items[0].Usage == nil || items[0].Usage.TotalTokens != 3 || items[0].LastRunAt != 2000 {
		t.Fatalf("unexpected summary: %#v", items[0])
	}
	filtered, total, err := store.ListArchived("agent-b", 50, 0)
	if err != nil {
		t.Fatalf("list filtered archived: %v", err)
	}
	if total != 0 || len(filtered) != 0 {
		t.Fatalf("expected empty filtered list, got total=%d len=%d", total, len(filtered))
	}

	loaded, err := store.LoadArchived("chat-archive-store")
	if err != nil {
		t.Fatalf("load archived: %v", err)
	}
	if loaded.Detail.ChatID != "chat-archive-store" || len(loaded.Detail.Events) == 0 || len(loaded.Runs) != 1 || loaded.Summary.LastRunAt != 2000 {
		t.Fatalf("unexpected loaded archive: %#v", loaded)
	}
	if len(loaded.Detail.RawMessages) == 0 {
		t.Fatalf("expected raw messages derived from archived JSONL")
	}

	hits, err := store.SearchArchived("archive answer", "agent-a", 10)
	if err != nil {
		t.Fatalf("search archived: %v", err)
	}
	if len(hits) != 1 || hits[0].ChatID != "chat-archive-store" || hits[0].CreatedAt != 1000 || hits[0].LastRunAt != 2000 {
		t.Fatalf("unexpected search hits: %#v", hits)
	}

	if err := os.MkdirAll(store.ChatDir("chat-archive-store"), 0o755); err != nil {
		t.Fatalf("mkdir archive chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-archive-store"), "artifact.txt"), []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	path, err := store.ResolveResource("chat-archive-store", "artifact.txt")
	if err != nil {
		t.Fatalf("resolve resource: %v", err)
	}
	if filepath.Base(path) != "artifact.txt" {
		t.Fatalf("unexpected resource path: %s", path)
	}
	if _, err := store.ResolveResource("chat-archive-store", "../artifact.txt"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected path traversal denial, got %v", err)
	}

	if err := store.DeleteArchived("chat-archive-store"); err != nil {
		t.Fatalf("delete archived: %v", err)
	}
	if _, err := store.LoadArchived("chat-archive-store"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("expected archive not found after delete, got %v", err)
	}
	if _, err := os.Stat(store.ChatDir("chat-archive-store")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected archive attachment dir removed, got %v", err)
	}
}

func testArchivedChat(chatID, agentKey, chatName, lastRunContent string) ArchivedChat {
	return ArchivedChat{
		Summary: ArchivedSummary{
			ChatID:         chatID,
			ChatName:       chatName,
			AgentKey:       agentKey,
			CreatedAt:      1000,
			UpdatedAt:      2000,
			ArchivedAt:     3000,
			LastRunID:      "run-archive",
			LastRunContent: lastRunContent,
			Usage:          &UsageData{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
			HasAttachments: true,
		},
		Runs: []RunSummary{{
			RunID:          "run-archive",
			ChatID:         chatID,
			AgentKey:       agentKey,
			InitialMessage: "hello",
			AssistantText:  lastRunContent,
			FinishReason:   "complete",
			StartedAt:      1000,
			CompletedAt:    2000,
			Usage:          UsageData{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		}},
		JSONLContent: `{"chatId":"` + chatID + `","runId":"run-archive","updatedAt":1000,"query":{"role":"user","message":"hello archive"},"_type":"query"}` + "\n" +
			`{"chatId":"` + chatID + `","runId":"run-archive","updatedAt":2000,"messages":[{"role":"assistant","content":[{"type":"text","text":"` + lastRunContent + `"}]}],"_type":"react"}` + "\n",
	}
}
