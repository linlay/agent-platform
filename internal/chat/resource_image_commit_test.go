package chat

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resourceImageTestPNG(marker byte) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, marker)
}

func seedResourceImageArtifact(t *testing.T, store *FileStore, chatID string) (string, string, string) {
	t.Helper()
	relativePath := "artifacts/run-1/source.png"
	targetPath := filepath.Join(store.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := resourceImageTestPNG(1)
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL, err := BuildChatScopeRef(relativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendArtifactManifest(chatID, "run-1", time.Now().UnixMilli(), []map[string]any{{
		"artifactId": "artifact-1",
		"type":       "file",
		"name":       "source.png",
		"mimeType":   "image/png",
		"sizeBytes":  len(data),
		"url":        resourceURL,
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	return relativePath, targetPath, fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli())
}

func TestCommitResourceImageOverwritesExpectedArtifactRevision(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-image", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, targetPath, revision := seedResourceImageArtifact(t, store, "chat-image")
	edited := resourceImageTestPNG(2)
	result, err := store.CommitResourceImage(ResourceImageCommitRequest{
		ChatID:           "chat-image",
		Profile:          "artifact",
		ResourceID:       "artifact-1",
		RelativePath:     relativePath,
		Mode:             "overwrite",
		ExpectedRevision: revision,
		MIMEType:         "image/png",
		Data:             edited,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactID != "artifact-1" || result.RelativePath != relativePath || result.Revision == "" {
		t.Fatalf("unexpected result %#v", result)
	}
	if got, err := os.ReadFile(targetPath); err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("edited file=%x err=%v", got, err)
	}
	detail, err := store.LoadChat("chat-image")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Artifact == nil || len(detail.Artifact.Items) != 1 || detail.Artifact.Items[0].SHA256 == "" {
		t.Fatalf("artifact manifest not updated: %#v", detail.Artifact)
	}
	_, err = store.CommitResourceImage(ResourceImageCommitRequest{
		ChatID: "chat-image", Profile: "artifact", ResourceID: "artifact-1",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		MIMEType: "image/png", Data: resourceImageTestPNG(3),
	})
	if !errors.Is(err, ErrResourceImageRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func TestCommitResourceImageCreatesArtifactFromArtifactOrReference(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-image-new", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, _, revision := seedResourceImageArtifact(t, store, "chat-image-new")
	result, err := store.CommitResourceImage(ResourceImageCommitRequest{
		ChatID: "chat-image-new", Profile: "artifact", ResourceID: "artifact-1",
		RelativePath: relativePath, Mode: "new-artifact", ExpectedRevision: revision,
		MIMEType: "image/png", Data: resourceImageTestPNG(4),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactID == "" || result.ArtifactID == "artifact-1" || result.ResourceID != result.ArtifactID || result.Revision == "" {
		t.Fatalf("unexpected new artifact %#v", result)
	}
	if filepath.ToSlash(result.RelativePath) == relativePath || filepath.Dir(result.RelativePath) == "." {
		t.Fatalf("unexpected new relative path %q", result.RelativePath)
	}
	if got, err := os.ReadFile(filepath.Join(store.ChatDir("chat-image-new"), filepath.FromSlash(result.RelativePath))); err != nil || !bytes.Equal(got, resourceImageTestPNG(4)) {
		t.Fatalf("new artifact=%x err=%v", got, err)
	}

	referencePath := "source.webp"
	referenceFile := filepath.Join(store.ChatDir("chat-image-new"), filepath.FromSlash(referencePath))
	if err := os.MkdirAll(filepath.Dir(referenceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	webp := append([]byte("RIFFxxxxWEBP"), 1)
	if err := os.WriteFile(referenceFile, webp, 0o644); err != nil {
		t.Fatal(err)
	}
	referenceResult, err := store.CommitResourceImage(ResourceImageCommitRequest{
		ChatID: "chat-image-new", Profile: "reference", ResourceID: "reference-1",
		RelativePath: referencePath, Mode: "new-artifact", MIMEType: "image/webp", Data: webp,
	})
	if err != nil || referenceResult.ArtifactID == "" {
		t.Fatalf("reference new artifact=%#v err=%v", referenceResult, err)
	}
	_, err = store.CommitResourceImage(ResourceImageCommitRequest{
		ChatID: "chat-image-new", Profile: "reference", ResourceID: "reference-1",
		RelativePath: referencePath, Mode: "overwrite", ExpectedRevision: "1:1",
		MIMEType: "image/webp", Data: webp,
	})
	if !errors.Is(err, ErrResourceImageOverwriteDenied) {
		t.Fatalf("expected reference overwrite denial, got %v", err)
	}
}

func TestCommitResourceImageRejectsIdentityPrefixSignatureAndEncodedTraversal(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-image-invalid", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, _, revision := seedResourceImageArtifact(t, store, "chat-image-invalid")
	cases := []ResourceImageCommitRequest{
		{ChatID: "chat-image-invalid", Profile: "artifact", ResourceID: "wrong", RelativePath: relativePath, Mode: "new-artifact", MIMEType: "image/png", Data: resourceImageTestPNG(1)},
		{ChatID: "chat-image-invalid", Profile: "artifact", ResourceID: "artifact-1", RelativePath: "references/source.png", Mode: "new-artifact", MIMEType: "image/png", Data: resourceImageTestPNG(1)},
		{ChatID: "chat-image-invalid", Profile: "artifact", ResourceID: "artifact-1", RelativePath: "artifacts/%2e%2e/source.png", Mode: "new-artifact", MIMEType: "image/png", Data: resourceImageTestPNG(1)},
		{ChatID: "chat-image-invalid", Profile: "artifact", ResourceID: "artifact-1", RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision, MIMEType: "image/png", Data: []byte("not-png")},
	}
	for index, request := range cases {
		if _, err := store.CommitResourceImage(request); err == nil {
			t.Fatalf("case %d unexpectedly succeeded", index)
		}
	}
}
