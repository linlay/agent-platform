package chat

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveStoreMigratesAgentModeColumnsWithoutBackfill(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(root, "archive")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		t.Fatalf("mkdir archive root: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(archiveRoot, "archive.db"))
	if err != nil {
		t.Fatalf("open legacy archive db: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE ARCHIVED_CHATS (
			CHAT_ID_ TEXT PRIMARY KEY,
			CHAT_NAME_ TEXT NOT NULL,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			ARCHIVED_AT_ INTEGER NOT NULL DEFAULT 0,
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO ARCHIVED_CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, ARCHIVED_AT_, LAST_RUN_CONTENT_)
		VALUES ('chat-archive-old', 'legacy', 'agent-old', 1700000003000, 'answer');
		CREATE TABLE ARCHIVED_RUNS (
			RUN_ID_ TEXT PRIMARY KEY,
			CHAT_ID_ TEXT NOT NULL
		);
		INSERT INTO ARCHIVED_RUNS (RUN_ID_, CHAT_ID_) VALUES ('run-archive-old', 'chat-archive-old');
	`); err != nil {
		t.Fatalf("seed legacy archive schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy archive db: %v", err)
	}

	store, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("migrate archive store: %v", err)
	}
	for _, table := range []string{"ARCHIVED_CHATS", "ARCHIVED_RUNS"} {
		if !sqliteColumnNames(t, store.db, table)["AGENT_MODE_"] {
			t.Fatalf("expected AGENT_MODE_ in %s", table)
		}
	}
	var chatMode, runMode string
	if err := store.db.QueryRow(`SELECT AGENT_MODE_ FROM ARCHIVED_CHATS WHERE CHAT_ID_='chat-archive-old'`).Scan(&chatMode); err != nil {
		t.Fatalf("read migrated archived chat mode: %v", err)
	}
	if err := store.db.QueryRow(`SELECT AGENT_MODE_ FROM ARCHIVED_RUNS WHERE RUN_ID_='run-archive-old'`).Scan(&runMode); err != nil {
		t.Fatalf("read migrated archived run mode: %v", err)
	}
	if chatMode != "" || runMode != "" {
		t.Fatalf("historical archive data must remain unknown, chat=%q run=%q", chatMode, runMode)
	}
}

func TestOwnerTypeColumnsArePhysicallyRemovedFromActiveAndArchiveStores(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	if _, _, err := active.EnsureChat("chat-owner-migration", "agent-a", "", "question"); err != nil {
		t.Fatalf("ensure active chat: %v", err)
	}
	if err := active.OnRunStarted(RunStart{
		ChatID: "chat-owner-migration", RunID: "run-owner-migration", AgentKey: "agent-a", InitialMessage: "question", StartedAtMillis: testEpochMillis(1000),
	}); err != nil {
		t.Fatalf("start active run: %v", err)
	}
	if err := active.OnRunCompleted(RunCompletion{
		ChatID: "chat-owner-migration", RunID: "run-owner-migration", AgentKey: "agent-a", InitialMessage: "question", AssistantText: "active answer", FinishReason: "complete", StartedAtMillis: testEpochMillis(1000), UpdatedAtMillis: testEpochMillis(2000),
	}); err != nil {
		t.Fatalf("complete active run: %v", err)
	}
	for _, table := range []string{"CHATS", "RUNS"} {
		if _, err := active.db.Exec("ALTER TABLE " + table + " ADD COLUMN OWNER_TYPE_ TEXT NOT NULL DEFAULT 'agent'"); err != nil {
			t.Fatalf("add legacy %s owner type: %v", table, err)
		}
	}
	if err := active.Close(); err != nil {
		t.Fatalf("close active store: %v", err)
	}

	active, err = NewFileStore(root)
	if err != nil {
		t.Fatalf("migrate active store: %v", err)
	}
	defer active.Close()
	for _, table := range []string{"CHATS", "RUNS"} {
		if sqliteColumnNames(t, active.db, table)["OWNER_TYPE_"] {
			t.Fatalf("%s retained obsolete OWNER_TYPE_ column", table)
		}
	}
	if summary, err := active.Summary("chat-owner-migration"); err != nil || summary == nil || summary.AgentKey != "agent-a" {
		t.Fatalf("active summary after migration = %#v, %v", summary, err)
	}
	if runs, err := active.ListRuns("chat-owner-migration"); err != nil || len(runs) != 1 || runs[0].AgentKey != "agent-a" {
		t.Fatalf("active runs after migration = %#v, %v", runs, err)
	}

	archives, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archived := testArchivedChat("chat-owner-archive-migration", "agent-a", "Owner migration", "archive migration answer")
	if err := archives.ArchiveChat(archived); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	for _, table := range []string{"ARCHIVED_CHATS", "ARCHIVED_RUNS"} {
		if _, err := archives.db.Exec("ALTER TABLE " + table + " ADD COLUMN OWNER_TYPE_ TEXT NOT NULL DEFAULT 'agent'"); err != nil {
			t.Fatalf("add legacy %s owner type: %v", table, err)
		}
	}
	if err := archives.db.Close(); err != nil {
		t.Fatalf("close archive store: %v", err)
	}

	archives, err = NewArchiveStore(root)
	if err != nil {
		t.Fatalf("migrate archive store: %v", err)
	}
	defer archives.db.Close()
	for _, table := range []string{"ARCHIVED_CHATS", "ARCHIVED_RUNS"} {
		if sqliteColumnNames(t, archives.db, table)["OWNER_TYPE_"] {
			t.Fatalf("%s retained obsolete OWNER_TYPE_ column", table)
		}
	}
	if loaded, err := archives.LoadArchived("chat-owner-archive-migration"); err != nil || loaded.Summary.AgentKey != "agent-a" || len(loaded.Runs) != 1 {
		t.Fatalf("archive load after migration = %#v, %v", loaded, err)
	}
	if hits, err := archives.SearchArchived("migration answer", "agent-a", 10); err != nil || len(hits) != 1 || hits[0].ChatID != "chat-owner-archive-migration" {
		t.Fatalf("archive FTS after migration = %#v, %v", hits, err)
	}
}

func TestArchiveJSONLRejectsUnsupportedSystemSchema(t *testing.T) {
	_, err := readJSONLinesContent(`{"_type":"query","chatId":"chat-archive-invalid","runId":"run-1","updatedAt":1700000001000,"systems":[]}` + "\n")
	if err == nil || !strings.Contains(err.Error(), "unsupported system schema field=systems") || !strings.Contains(err.Error(), "chatId=chat-archive-invalid") || !strings.Contains(err.Error(), "runId=run-1") {
		t.Fatalf("expected contextual archive schema rejection, got %v", err)
	}
}

func TestArchiveStoreArchiveListLoadSearchAndDelete(t *testing.T) {
	root := t.TempDir()
	store, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archived := testArchivedChat("chat-archive-store", "agent-a", "Archive topic", "final archive answer")
	archived.Summary.UpdatedAt = testEpochMillis(2500)
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
	if !items[0].HasAttachments || items[0].Usage == nil || items[0].Usage.TotalTokens != 3 || items[0].LastRunAt != testEpochMillis(2000) {
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
	if loaded.Detail.ChatID != "chat-archive-store" || len(loaded.Detail.Events) == 0 || len(loaded.Runs) != 1 || loaded.Summary.LastRunAt != testEpochMillis(2000) {
		t.Fatalf("unexpected loaded archive: %#v", loaded)
	}
	if len(loaded.Detail.RawMessages) == 0 {
		t.Fatalf("expected raw messages derived from archived JSONL")
	}

	hits, err := store.SearchArchived("archive answer", "agent-a", 10)
	if err != nil {
		t.Fatalf("search archived: %v", err)
	}
	if len(hits) != 1 || hits[0].ChatID != "chat-archive-store" || hits[0].CreatedAt != testEpochMillis(1000) || hits[0].LastRunAt != testEpochMillis(2000) {
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
			CreatedAt:      testEpochMillis(1000),
			UpdatedAt:      testEpochMillis(2000),
			LastRunAt:      testEpochMillis(2000),
			ArchivedAt:     testEpochMillis(3000),
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
			StartedAt:      testEpochMillis(1000),
			CompletedAt:    testEpochMillis(2000),
			Usage:          UsageData{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		}},
		JSONLContent: `{"chatId":"` + chatID + `","runId":"run-archive","updatedAt":1700000001000,"query":{"role":"user","message":"hello archive"},"_type":"query"}` + "\n" +
			`{"chatId":"` + chatID + `","runId":"run-archive","updatedAt":1700000002000,"messages":[{"role":"assistant","ts":1700000002000,"content":[{"type":"text","text":"` + lastRunContent + `"}]}],"_type":"react"}` + "\n",
	}
}
