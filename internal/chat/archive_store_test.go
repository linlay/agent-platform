package chat

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/sqlitecontract"
)

func TestArchiveStoreRejectsLegacySchemaWithoutChangingIt(t *testing.T) {
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

	dbPath := filepath.Join(archiveRoot, "archive.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read legacy archive db: %v", err)
	}
	if _, err := NewArchiveStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("NewArchiveStore error = %v, want unsupported storage schema", err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read archive db after rejection: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("legacy archive db was modified while being rejected")
	}
}

func TestArchiveStoreRejectsRemovedOwnerTypeColumn(t *testing.T) {
	root := t.TempDir()
	archives, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	if _, err := archives.db.Exec("ALTER TABLE ARCHIVED_CHATS ADD COLUMN OWNER_TYPE_ TEXT NOT NULL DEFAULT 'agent'"); err != nil {
		t.Fatalf("add obsolete owner type: %v", err)
	}
	if err := archives.db.Close(); err != nil {
		t.Fatalf("close archive store: %v", err)
	}
	if _, err := NewArchiveStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("NewArchiveStore error = %v, want unsupported storage schema", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "archive", "archive.db"))
	if err != nil {
		t.Fatalf("reopen archive db: %v", err)
	}
	defer db.Close()
	if !sqliteColumnNames(t, db, "ARCHIVED_CHATS")["OWNER_TYPE_"] {
		t.Fatal("rejected archive db lost its obsolete column")
	}
}

func TestArchiveStoreRejectsResidualRuntimeData(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(root, "archive")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		t.Fatalf("mkdir archive root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, "legacy.jsonl"), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy archive data: %v", err)
	}
	if _, err := NewArchiveStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("NewArchiveStore error = %v, want unsupported storage schema", err)
	}
}

func TestArchiveStoreWritesCurrentSchemaMarker(t *testing.T) {
	store, err := NewArchiveStore(t.TempDir())
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	defer store.db.Close()
	assertSQLiteSchemaMarker(t, store.db, archiveSchemaSpec)
	if err := sqlitecontract.Verify(store.db, filepath.Join(store.root, "archive.db"), filepath.Dir(store.root), archiveSchemaSpec); err != nil {
		t.Fatalf("verify current archive schema: %v", err)
	}
}

func TestArchiveStoreAtStartupClaimsExactUnmarkedDatabase(t *testing.T) {
	root := t.TempDir()
	store, err := NewArchiveStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA application_id = 0; PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewArchiveStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("runtime open error = %v, want unsupported storage schema", err)
	}
	claimed, err := NewArchiveStoreAtStartup(root)
	if err != nil {
		t.Fatalf("startup claim: %v", err)
	}
	defer claimed.db.Close()
	assertSQLiteSchemaMarker(t, claimed.db, archiveSchemaSpec)
}

func TestArchiveJSONLRejectsUnsupportedSystemSchema(t *testing.T) {
	_, err := readJSONLinesContent(`{"_type":"query","chatId":"chat-archive-invalid","runId":"run-1","updatedAt":1700000001000,"systems":[]}` + "\n")
	if !IsJSONLSchemaViolation(err) || !strings.Contains(err.Error(), "unsupported system schema field=systems") || !strings.Contains(err.Error(), "chatId=chat-archive-invalid") || !strings.Contains(err.Error(), "runId=run-1") {
		t.Fatalf("expected contextual archive schema rejection, got %v", err)
	}
}

func TestArchiveReadersRejectInvalidJSONLWithoutPartialResults(t *testing.T) {
	store, err := NewArchiveStore(t.TempDir())
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	const chatID = "chat-archive-schema"
	archived := testArchivedChat(chatID, "agent-a", "Archive schema topic", "archive answer")
	if err := store.ArchiveChat(archived); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	invalid := `{"type":"query","chatId":"` + chatID + `","runId":"run-archive","updatedAt":1700000001000}` + "\n"
	if _, err := store.db.Exec("UPDATE ARCHIVED_CHATS SET JSONL_CONTENT_=? WHERE CHAT_ID_=?", invalid, chatID); err != nil {
		t.Fatalf("seed invalid archived JSONL: %v", err)
	}

	checks := map[string]func() error{
		"load":  func() error { _, err := store.LoadArchived(chatID); return err },
		"jsonl": func() error { _, err := store.LoadJSONLContent(chatID); return err },
		"search": func() error {
			_, err := store.SearchArchived("Archive schema topic", "", 10)
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !IsJSONLSchemaViolation(err) {
				t.Fatalf("expected archive schema violation, got %v", err)
			}
		})
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
