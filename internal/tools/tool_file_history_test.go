package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func TestFileHistoryRecordsNewFileWithEmptyOriginal(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.RequireReadBeforeWrite = false
	executor.chats = store
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		ChatID:        "chat-history-new",
		RunID:         "run-history-new",
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path": "new.txt",
		"content":   "hello\n",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}

	filePath, _ := result.Structured["filePath"].(string)
	original, err := executor.ReadFileHistory("chat-history-new", "run-history-new", filePath, "original")
	if err != nil {
		t.Fatalf("read original history: %v", err)
	}
	if original != "" {
		t.Fatalf("expected empty original for new file, got %q", original)
	}
	current, err := executor.ReadFileHistory("chat-history-new", "run-history-new", filePath, "current")
	if err != nil {
		t.Fatalf("read current history: %v", err)
	}
	if current != "hello\n" {
		t.Fatalf("expected current content, got %q", current)
	}
}

func TestFileHistoryKeepsFirstOriginalAndLatestCurrent(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	filePath := filepath.Join(root, "app.txt")
	if err := os.WriteFile(filePath, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.RequireReadBeforeWrite = false
	executor.chats = store
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		ChatID:        "chat-history-edit",
		RunID:         "run-history-edit",
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":  "app.txt",
		"old_string": "one\n",
		"new_string": "two\n",
	}, execCtx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("first invokeEdit result=%#v err=%v", result, err)
	}
	result, err = executor.invokeEdit(context.Background(), map[string]any{
		"file_path":  "app.txt",
		"old_string": "two\n",
		"new_string": "three\n",
	}, execCtx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("second invokeEdit result=%#v err=%v", result, err)
	}

	filePath, _ = result.Structured["filePath"].(string)
	original, err := executor.ReadFileHistory("chat-history-edit", "run-history-edit", filePath, "original")
	if err != nil {
		t.Fatalf("read original history: %v", err)
	}
	if original != "one\n" {
		t.Fatalf("expected first original content, got %q", original)
	}
	current, err := executor.ReadFileHistory("chat-history-edit", "run-history-edit", filePath, "current")
	if err != nil {
		t.Fatalf("read current history: %v", err)
	}
	if current != "three\n" {
		t.Fatalf("expected latest current content, got %q", current)
	}
}

func TestFileHistoryMissingEntryReturnsNotExist(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	executor := fileToolExecutor(root, false)
	executor.chats = store
	_, err = executor.ReadFileHistory("chat-history-missing", "run-history-missing", filepath.Join(root, "missing.txt"), "original")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
