package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

func TestFileSnapshotExpiryBoundary(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name    string
		at      int64
		expired bool
	}{
		{"boundary", now - time.Hour.Milliseconds(), false},
		{"expired", now - time.Hour.Milliseconds() - 1, true},
		{"missing", 0, true}, {"future", now + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileSnapshotExpired(ReadFileSnapshot{ReadAtUnixMs: tc.at}, now, time.Hour); got != tc.expired {
				t.Fatalf("expired=%v", got)
			}
		})
	}
}

func TestFileMutationExpiryAndRealReread(t *testing.T) {
	for _, scope := range []string{"run", "chat"} {
		for _, operation := range []string{"file_edit", "file_write"} {
			t.Run(scope+"/"+operation, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "a.txt")
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
				executor := fileToolExecutor(root, false)
				store, err := chat.NewFileStoreAtStartup(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				executor.chats = store
				executor.cfg.FileTools.ReadBeforeWriteScope = scope
				ctx := fileToolExecutionContext(root)
				ctx.Session.ChatID = "chat-expiry"
				ctx.Session.RunID = "read"
				args := map[string]any{"file_path": "a.txt"}
				if _, err := executor.invokeRead(args, ctx); err != nil {
					t.Fatal(err)
				}
				canonical := filepath.Join(realPath(t, root), "a.txt")
				snap := ctx.ReadFileState[canonical]
				snap.ReadAtUnixMs = time.Now().Add(-61 * time.Minute).UnixMilli()
				ctx.ReadFileState[canonical] = snap
				if scope == "chat" {
					executor.recordChatFileVersion(ctx, canonical, snap, "read", false)
					ctx = fileToolExecutionContext(root)
					ctx.Session.ChatID = "chat-expiry"
					ctx.Session.RunID = "edit"
				}
				mutate := func() ToolExecutionResult {
					params := map[string]any{"file_path": "a.txt", "old_string": "old", "new_string": "new", "content": "new", "description": "update"}
					var result ToolExecutionResult
					var err error
					if operation == "file_edit" {
						result, err = executor.invokeEdit(context.Background(), params, ctx)
					} else {
						result, err = executor.invokeWrite(context.Background(), params, ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
					return result
				}
				result := mutate()
				if result.Structured["error"] != operation+"_cache_expired" || !strings.Contains(result.Structured["message"].(string), "文件缓存过期") {
					t.Fatalf("unexpected: %#v", result)
				}
				if result.Structured["maxAgeMs"] != time.Hour.Milliseconds() {
					t.Fatalf("metadata: %#v", result.Structured)
				}
				read, err := executor.invokeRead(args, ctx)
				if err != nil {
					t.Fatal(err)
				}
				if read.Structured["content"] != "old" {
					t.Fatalf("expected actual content: %#v", read.Structured)
				}
				at := ctx.ReadFileState[canonical].ReadAtUnixMs
				if _, rejected := executor.validateReadBeforeFileMutation(canonical, ctx, operation); rejected {
					t.Fatal("fresh snapshot rejected")
				}
				if ctx.ReadFileState[canonical].ReadAtUnixMs != at {
					t.Fatal("validation renewed observation")
				}
				result = mutate()
				if result.ExitCode != 0 || result.Error != "" {
					t.Fatalf("mutation: %#v", result)
				}
			})
		}
	}
}

func TestReadAndEditDetectSameStatChangedSHA(t *testing.T) {
	for _, reread := range []bool{false, true} {
		t.Run(map[bool]string{false: "edit", true: "read"}[reread], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "a.txt")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			executor := fileToolExecutor(root, false)
			ctx := fileToolExecutionContext(root)
			args := map[string]any{"file_path": "a.txt"}
			if _, err := executor.invokeRead(args, ctx); err != nil {
				t.Fatal(err)
			}
			canonical := filepath.Join(realPath(t, root), "a.txt")
			snap := ctx.ReadFileState[canonical]
			if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
				t.Fatal(err)
			}
			stamp := time.UnixMilli(snap.ModifiedUnixMs)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if reread {
				result, err := executor.invokeRead(args, ctx)
				if err != nil || result.Structured["content"] != "bad" {
					t.Fatalf("read: %#v %v", result, err)
				}
			} else {
				result, rejected := executor.validateReadBeforeFileMutation(canonical, ctx, "file_edit")
				if !rejected || result.Structured["error"] != "file_edit_modified_since_read" {
					t.Fatalf("edit: %#v", result)
				}
			}
		})
	}
}

func TestRestoredChatSnapshotExpiresWithoutRenewal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"
	first := fileToolExecutionContext(root)
	first.Session.ChatID = "chat-restored"
	args := map[string]any{"file_path": "a.txt"}
	if _, err := executor.invokeRead(args, first); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realPath(t, root), "a.txt")
	observed := first.ReadFileState[path].ReadAtUnixMs
	second := fileToolExecutionContext(root)
	second.Session.ChatID = first.Session.ChatID
	if result, rejected := executor.validateReadBeforeFileMutation(path, second, "file_edit"); rejected {
		t.Fatalf("restore: %#v", result)
	}
	result, err := executor.invokeRead(args, second)
	if err != nil || result.Structured["kind"] != "unchanged" {
		t.Fatalf("unchanged: %#v %v", result, err)
	}
	if second.ReadFileState[path].ReadAtUnixMs != observed {
		t.Fatal("observation renewed")
	}
	snap := second.ReadFileState[path]
	snap.ReadAtUnixMs = time.Now().Add(-61 * time.Minute).UnixMilli()
	second.ReadFileState[path] = snap
	if result, rejected := executor.validateReadBeforeFileMutation(path, second, "file_edit"); !rejected || result.Structured["error"] != "file_edit_cache_expired" {
		t.Fatalf("restored expiry: %#v", result)
	}
}
