package chat

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDesktopOwnerMigrationPreservesDataAndOtherOwners(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := NewArchiveStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	for id, source := range map[string]string{"old-active": "query:app", "old-archived": "query:app", "other-user": "query:bob", "automation": "automation:daily"} {
		if _, _, err := store.EnsureChatWithSource(id, "agent", "", id, source); err != nil {
			t.Fatal(err)
		}
	}
	attachment := filepath.Join(store.ChatDir("old-active"), "proof.txt")
	os.MkdirAll(filepath.Dir(attachment), 0700)
	os.WriteFile(attachment, []byte("preserved attachment"), 0600)
	if _, err := store.db.Exec("UPDATE CHATS SET LAST_RUN_AT_ = CREATED_AT_ WHERE CHAT_ID_ = ?", "old-archived"); err != nil {
		t.Fatal(err)
	}
	if err := NewArchiver(store, archive).ArchiveChat("old-archived"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	archive.db.Close()
	backup := t.TempDir()
	subject := "desktop-user:" + strings.Repeat("a", 64)
	result, err := MigrateLegacyDesktopSource(t.Context(), root, backup, subject)
	if err != nil || result["active"] != 1 || result["archived"] != 1 {
		t.Fatal(result, err)
	}
	for _, item := range []struct{ file, table, id, want string }{
		{filepath.Join(root, "chats.db"), "CHATS", "old-active", "query:" + subject},
		{filepath.Join(root, "chats.db"), "CHATS", "other-user", "query:bob"},
		{filepath.Join(root, "chats.db"), "CHATS", "automation", "automation:daily"},
		{filepath.Join(root, "archive", "archive.db"), "ARCHIVED_CHATS", "old-archived", "query:" + subject},
		{filepath.Join(backup, "active.db"), "CHATS", "old-active", "query:app"},
		{filepath.Join(backup, "archived.db"), "ARCHIVED_CHATS", "old-archived", "query:app"},
	} {
		db, err := sql.Open("sqlite", item.file)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		err = db.QueryRow("SELECT SOURCE_ FROM "+item.table+" WHERE CHAT_ID_=?", item.id).Scan(&got)
		db.Close()
		if err != nil || got != item.want {
			t.Fatalf("%+v got %q %v", item, got, err)
		}
	}
	bytes, err := os.ReadFile(attachment)
	if err != nil || string(bytes) != "preserved attachment" {
		t.Fatal("attachment changed", err)
	}
	result, err = MigrateLegacyDesktopSource(t.Context(), root, backup, subject)
	if err != nil || result["active"] != 0 || result["archived"] != 0 {
		t.Fatal("repeat was not idempotent", result, err)
	}
}
