package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBTWBranchRejectsUnsupportedParentSystemSchema(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	const chatID = "chat-btw-invalid-system"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "question"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	legacyJSONL := `{"_type":"query","chatId":"` + chatID + `","runId":"run-parent","updatedAt":1700000001000,"systems":[]}` + "\n"
	if err := os.WriteFile(store.chatJSONLPath(chatID), []byte(legacyJSONL), 0o644); err != nil {
		t.Fatalf("write legacy parent JSONL: %v", err)
	}
	if _, err := store.CreateBTWBranch(chatID, "btw_invalid"); err == nil || !strings.Contains(err.Error(), "unsupported system schema field=systems") || !strings.Contains(err.Error(), "chatId="+chatID) {
		t.Fatalf("expected parent schema rejection before BTW copy, got %v", err)
	}
}

func TestBTWBranchCopiesParentAndAppendsIndependently(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const chatID = "chat-btw-copy"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "parent question"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-parent",
		UpdatedAt: testEpochMillis(100),
		Messages: []map[string]any{{
			"role": "user", "content": "parent question", "ts": testEpochMillis(100),
		}},
	}); err != nil {
		t.Fatalf("append parent query: %v", err)
	}
	parentBefore, err := os.ReadFile(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}

	branch, err := store.CreateBTWBranch(chatID, "btw_copy")
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	branchBefore, err := os.ReadFile(branch.Path())
	if err != nil {
		t.Fatalf("read branch: %v", err)
	}
	if string(branchBefore) != string(parentBefore) {
		t.Fatalf("branch snapshot differs from parent\nparent=%s\nbranch=%s", parentBefore, branchBefore)
	}
	if _, err := store.ResolveResource(filepath.Join(chatID, BTWRootDirName, "btw_copy.jsonl")); !os.IsPermission(err) {
		t.Fatalf("expected BTW JSONL to be hidden from resource API, got %v", err)
	}
	if err := branch.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-btw",
		UpdatedAt: testEpochMillis(101),
		Messages: []map[string]any{{
			"role": "user", "content": "side question", "ts": testEpochMillis(101),
		}},
	}); err != nil {
		t.Fatalf("append branch query: %v", err)
	}
	parentAfter, err := os.ReadFile(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read parent after: %v", err)
	}
	if string(parentAfter) != string(parentBefore) {
		t.Fatalf("branch append changed parent JSONL")
	}
	messages, err := branch.LoadRawMessages(10)
	if err != nil {
		t.Fatalf("load branch messages: %v", err)
	}
	if len(messages) != 2 || messages[1]["content"] != "side question" {
		t.Fatalf("unexpected branch messages %#v", messages)
	}
}

func TestBTWBranchAppendDoesNotRecreateDeletedParentDirectory(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const chatID = "chat-btw-deleted"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "question"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	branch, err := store.CreateBTWBranch(chatID, "btw_deleted")
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if err := os.RemoveAll(store.ChatDir(chatID)); err != nil {
		t.Fatalf("remove chat dir: %v", err)
	}
	if err := branch.AppendStepLine(chatID, StepLine{ChatID: chatID, RunID: "run-btw"}); err != ErrBTWNotFound {
		t.Fatalf("expected ErrBTWNotFound, got %v", err)
	}
	if _, err := os.Stat(filepath.Dir(branch.Path())); !os.IsNotExist(err) {
		t.Fatalf("branch append recreated deleted directory: %v", err)
	}
}

func TestCopyDerivedChatDirSkipsBTWBranches(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(source, BTWRootDirName), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "attachment.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, BTWRootDirName, "btw_1.jsonl"), []byte("hidden"), 0o600); err != nil {
		t.Fatalf("write btw: %v", err)
	}
	if err := copyDerivedChatDir(source, target); err != nil {
		t.Fatalf("copy derived dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "attachment.txt")); err != nil {
		t.Fatalf("expected attachment to copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, BTWRootDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected BTW directory to be skipped, got %v", err)
	}
}
