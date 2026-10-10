package tools

import (
	"agent-platform/internal/contracts"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKBaseCollectionWritesRequireReadButNoExtraWriteApproval(t *testing.T) {
	workspace, source := t.TempDir(), t.TempDir()
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(source, "existing.md")
	if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := fileToolExecutor(workspace, true)
	executor.cfg.FileTools.RequireReadBeforeWrite = false
	execCtx := kbaseEditingExecutionContext(workspace)
	execCtx.Session.AccessLevel = contracts.AccessLevelDefault
	execCtx.Session.ScopedFilePolicy.EditableCollectionRoots = []string{canonical}
	unread, err := executor.invokeWrite(context.Background(), map[string]any{"filePath": target, "content": "after"}, execCtx)
	if err != nil || unread.Structured["error"] != "file_write_not_read" {
		t.Fatalf("mandatory collection read: %#v %v", unread, err)
	}
	read, err := executor.invokeRead(map[string]any{"filePath": target}, execCtx)
	if err != nil || read.Error != "" {
		t.Fatalf("read collection: %#v %v", read, err)
	}
	written, err := executor.invokeWrite(context.Background(), map[string]any{"filePath": target, "content": "after"}, execCtx)
	if err != nil || written.Error != "" {
		t.Fatalf("write collection: %#v %v", written, err)
	}
	execCtx.Session.ScopedFilePolicy.WorkspaceMutationEnabled = false
	blocked, err := executor.invokeWrite(context.Background(), map[string]any{"filePath": filepath.Join(source, "new.md"), "content": "denied"}, execCtx)
	if err != nil || blocked.Structured["error"] != "kbase_editing_mode_required" {
		t.Fatalf("collection editing gate: %#v %v", blocked, err)
	}
}
