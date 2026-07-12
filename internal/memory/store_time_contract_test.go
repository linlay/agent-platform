package memory

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestFileStoreRejectsInvalidPersistedTimeAsContractViolation(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "legacy.stored.json"), []byte(`{
  "id":"legacy",
  "chatId":"chat-1",
  "summary":"legacy",
  "sourceType":"learn",
  "category":"general",
  "createdAt":"1700000000000",
  "updatedAt":1700000000000
}`), 0o644); err != nil {
		t.Fatalf("write legacy memory: %v", err)
	}
	if _, err := store.ListAll(""); !timecontract.IsViolation(err) {
		t.Fatalf("expected time contract violation, got %v", err)
	}
}
