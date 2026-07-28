package filetools

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/contracts"
)

func TestScopedFilePolicyEnforcesKBaseMarkdownBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	valid := filepath.Join(root, "policy.md")
	upper := filepath.Join(root, "POLICY.MD")
	markdown := filepath.Join(root, "policy.markdown")
	if err := os.WriteFile(valid, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upper, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markdown, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := scopedTestSession(root, true)

	for _, path := range []string{valid, upper} {
		if err := ValidateScopedRead(session, path, false); err != nil {
			t.Fatalf("expected %s to be allowed: %v", path, err)
		}
	}
	if err := ValidateScopedRead(session, markdown, false); ScopedPolicyErrorCode(err) != "kbase_editing_extension_unsupported" {
		t.Fatalf("expected .markdown rejection, got %v", err)
	}
	if err := ValidateScopedWrite(session, filepath.Join(outside, "escape.md")); ScopedPolicyErrorCode(err) != "kbase_editing_path_outside_source" {
		t.Fatalf("expected outside source rejection, got %v", err)
	}
	if err := ValidateScopedWrite(session, filepath.Join(root, "missing", "new.md")); ScopedPolicyErrorCode(err) != "kbase_editing_parent_missing" {
		t.Fatalf("expected missing parent rejection, got %v", err)
	}
	if err := ValidateScopedWrite(session, filepath.Join(root, "new.md")); err != nil {
		t.Fatalf("expected new Markdown file in existing directory to be allowed: %v", err)
	}
	if err := ValidateScopedWrite(session, filepath.Join(root, "docs", "..", "new.md")); err != nil {
		t.Fatalf("expected canonical in-source parent traversal to be allowed: %v", err)
	}
}

func TestScopedFilePolicyRejectsSymlinkEscapeAndReadOnlyRun(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	session := scopedTestSession(root, true)
	if err := ValidateScopedRead(session, filepath.Join(link, "secret.md"), false); err != nil {
		t.Fatalf("expected source symlink escape to defer to AccessPolicy, got %v", err)
	}

	readOnly := scopedTestSession(root, false)
	if err := ValidateScopedRead(readOnly, filepath.Join(root, "notes.md"), false); ScopedPolicyErrorCode(err) != "kbase_editing_mode_required" {
		t.Fatalf("expected editing mode requirement for read, got %v", err)
	}
	if err := ValidateScopedWrite(readOnly, filepath.Join(root, "notes.md")); ScopedPolicyErrorCode(err) != "kbase_editing_mode_required" {
		t.Fatalf("expected editing mode requirement for write, got %v", err)
	}
}

func TestScopedFilePolicyUsesCanonicalSourceAndCurrentChatRoots(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	chatDir := filepath.Join(base, "chats", "chat-1")
	otherChatDir := filepath.Join(base, "chats", "chat-2")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{source, chatDir, otherChatDir, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	session := scopedTestSession(source, true)
	session.RuntimeContext.LocalPaths.ChatAttachmentsDir = chatDir

	sourcePath := filepath.Join(source, "policy.md")
	chatPath := filepath.Join(chatDir, "report.txt")
	if !ScopedPathInSource(session, sourcePath) || ScopedPathInSource(session, chatPath) {
		t.Fatal("source membership must be derived from the current canonical path")
	}
	if err := ValidateScopedWrite(session, chatPath); err != nil {
		t.Fatalf("current chatspace write must be allowed: %v", err)
	}
	if err := ValidateScopedRead(session, filepath.Join(outside, "secret.txt"), false); err != nil {
		t.Fatalf("external read must defer to AccessPolicy: %v", err)
	}
	for _, path := range []string{
		filepath.Join(otherChatDir, "report.txt"),
		filepath.Join(outside, "report.txt"),
	} {
		if err := ValidateScopedWrite(session, path); ScopedPolicyErrorCode(err) != "kbase_editing_path_outside_source" {
			t.Fatalf("write outside source/current chatspace must be rejected for %s: %v", path, err)
		}
	}

	link := filepath.Join(chatDir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScopedWrite(session, filepath.Join(link, "report.txt")); ScopedPolicyErrorCode(err) != "kbase_editing_path_outside_source" {
		t.Fatalf("chatspace symlink escape must be rejected: %v", err)
	}
}

func scopedTestSession(root string, editing bool) contracts.QuerySession {
	return contracts.QuerySession{
		WorkspaceRoot: root,
		ScopedFilePolicy: &contracts.ScopedFilePolicy{
			Root:                  root,
			AllowedExtensions:     []string{".md"},
			AllowRead:             editing,
			AllowWrite:            editing,
			AllowCreate:           editing,
			RequireExistingParent: true,
			RequireUTF8:           true,
		},
	}
}
