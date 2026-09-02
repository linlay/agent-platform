package chat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedMarkdownArtifact(t *testing.T, store *FileStore, chatID string) (string, string, string) {
	t.Helper()
	relativePath := "artifacts/run-doc/notes.md"
	targetPath := filepath.Join(store.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("# original\n")
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := BuildChatScopeRef(relativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendArtifactManifest(chatID, "run-doc", time.Now().UnixMilli(), []map[string]any{{
		"artifactId": "artifact-doc", "type": "file", "name": "notes.md",
		"mimeType": "text/markdown", "sizeBytes": len(data), "url": resourceURL,
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	return relativePath, targetPath, resourceImageRevision(info)
}

func TestCommitResourceDocumentOverwriteAndRevisionConflict(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-document", "agent-1", "", "document"); err != nil {
		t.Fatal(err)
	}
	relativePath, targetPath, revision := seedMarkdownArtifact(t, store, "chat-document")
	edited := []byte("# edited\n")
	result, err := store.CommitResourceDocument(ResourceDocumentCommitRequest{
		ChatID: "chat-document", Profile: "artifact", ResourceID: "artifact-doc",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		DocumentKind: "document-markdown", MIMEType: "text/markdown", Data: edited,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourceID != "artifact-doc" || result.RelativePath != relativePath || result.Revision == "" {
		t.Fatalf("unexpected overwrite result %#v", result)
	}
	if got, err := os.ReadFile(targetPath); err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("edited document=%q err=%v", got, err)
	}
	_, err = store.CommitResourceDocument(ResourceDocumentCommitRequest{
		ChatID: "chat-document", Profile: "artifact", ResourceID: "artifact-doc",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		DocumentKind: "document-markdown", MIMEType: "text/markdown", Data: []byte("stale"),
	})
	if !errors.Is(err, ErrResourceDocumentRevisionConflict) {
		t.Fatalf("stale overwrite error = %v, want revision conflict", err)
	}
}

func TestCommitResourceDocumentReferenceOnlyCreatesArtifact(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-reference-document", "agent-1", "", "document"); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(store.ChatDir("chat-reference-document"), "source.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	revision := resourceImageRevision(info)
	request := ResourceDocumentCommitRequest{
		ChatID: "chat-reference-document", Profile: "reference", ResourceID: "reference-1",
		RelativePath: "source.txt", Mode: "overwrite", ExpectedRevision: revision,
		DocumentKind: "document-text", MIMEType: "text/plain", Data: []byte("edited"),
	}
	if _, err := store.CommitResourceDocument(request); !errors.Is(err, ErrResourceDocumentOverwriteDenied) {
		t.Fatalf("reference overwrite error = %v, want overwrite denied", err)
	}
	request.Mode = "new-artifact"
	result, err := store.CommitResourceDocument(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactID == "" || result.ResourceID != result.ArtifactID || result.RelativePath == "source.txt" {
		t.Fatalf("unexpected new Artifact result %#v", result)
	}
	if original, err := os.ReadFile(targetPath); err != nil || string(original) != "original" {
		t.Fatalf("Reference source was modified: %q err=%v", original, err)
	}
}

func TestCommitResourceDocumentRejectsKindThatConflictsWithPath(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-document-kind", "agent-1", "", "document"); err != nil {
		t.Fatal(err)
	}
	relativePath, _, revision := seedMarkdownArtifact(t, store, "chat-document-kind")
	_, err = store.CommitResourceDocument(ResourceDocumentCommitRequest{
		ChatID: "chat-document-kind", Profile: "artifact", ResourceID: "artifact-doc",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		DocumentKind: "document-code", MIMEType: "text/plain", Data: []byte("not markdown by claim"),
	})
	if !errors.Is(err, ErrResourceDocumentInvalid) {
		t.Fatalf("kind-confused overwrite error = %v, want invalid", err)
	}
}
