package chatresource

import (
	"agent-platform/internal/chat"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactsRequireChatResolveAmbiguityAndVerifyContent(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"chat-a", "chat-b"} {
		if _, _, err = store.EnsureChat(id, "agent", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("published")
	hash := sha256.Sum256(data)
	relative := "artifacts/run-a/result.txt"
	path := filepath.Join(store.ChatDir("chat-a"), filepath.FromSlash(relative))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, data, 0600)
	item := map[string]any{"artifactId": "same", "url": relative, "name": "result.txt", "sizeBytes": len(data), "sha256": hex.EncodeToString(hash[:]), "mimeType": "text/plain"}
	if err = store.AppendArtifactManifest("chat-a", "run-a", 1, []map[string]any{item}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if _, err = service.GetArtifact("chat-b", "same", ""); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatal("cross-chat resolution", err)
	}
	f, meta, err := service.OpenArtifact("chat-a", "same", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if meta.RunID != "run-a" || meta.ChatID != "chat-a" {
		t.Fatal(meta)
	}
	os.WriteFile(path, []byte("modified!"), 0600)
	if _, _, err = service.OpenArtifact("chat-a", "same", ""); !errors.Is(err, ErrArtifactChanged) {
		t.Fatal("changed content accepted", err)
	}
	if err = store.AppendArtifactManifest("chat-a", "run-b", 2, []map[string]any{item}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetArtifact("chat-a", "same", ""); !errors.Is(err, ErrArtifactAmbiguous) {
		t.Fatal("ambiguous ID picked first", err)
	}
	if _, err = service.GetArtifact("chat-a", "same", "run-a"); err != nil {
		t.Fatal(err)
	}
	items, more, err := service.ListArtifacts("chat-a", "", 0, 1)
	if err != nil || !more || len(items) != 1 {
		t.Fatal(items, more, err)
	}
	items, more, err = service.ListArtifacts("chat-a", "run-b", 0, 10)
	if err != nil || more || len(items) != 1 || items[0].RunID != "run-b" {
		t.Fatal(items, more, err)
	}
}

func TestOpenArtifactByRefUsesPublishedManifestAndLatestEntry(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, chatID := range []string{"chat-ref-a", "chat-ref-b"} {
		if _, _, err = store.EnsureChat(chatID, "agent", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	const sourceRef = "artifacts/run-1/report.pdf"
	path := filepath.Join(store.ChatDir("chat-ref-a"), filepath.FromSlash(sourceRef))
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	publish := func(artifactID string, publishedAt int64, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if err := store.AppendArtifactManifest("chat-ref-a", "run-1", publishedAt, []map[string]any{{
			"artifactId": artifactID, "type": "file", "url": sourceRef, "name": "report.pdf",
			"mimeType": "application/pdf", "sizeBytes": len(data), "sha256": hex.EncodeToString(digest[:]),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	publish("old", 1, []byte("old"))
	publish("latest", 2, []byte("latest"))

	service := NewService(store)
	f, artifact, err := service.OpenArtifactByRef("chat-ref-a", sourceRef)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if artifact.ArtifactID != "latest" || artifact.RunID != "run-1" {
		t.Fatalf("artifact=%#v", artifact)
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "latest" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, _, err = service.OpenArtifactByRef("chat-ref-a", "artifacts/run-1/missing.pdf"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("unpublished sourceRef error=%v", err)
	}
	if _, _, err = service.OpenArtifactByRef("chat-ref-b", sourceRef); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("cross-chat sourceRef error=%v", err)
	}
	if err = os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.OpenArtifactByRef("chat-ref-a", sourceRef); !errors.Is(err, ErrArtifactChanged) {
		t.Fatalf("changed sourceRef error=%v", err)
	}
}

// An @chat/ reference is a literal path, so "?" and "#" in a file name are
// part of the name rather than URL syntax.
func TestOpenArtifactByRefAcceptsLiteralChatAlias(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err = store.EnsureChat("chat-alias", "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	const sourceRef = "@chat/artifacts/run-1/夏日 #1?.txt"
	path := filepath.Join(store.ChatDir("chat-alias"), "artifacts", "run-1", "夏日 #1?.txt")
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("alias")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if err = store.AppendArtifactManifest("chat-alias", "run-1", 1, []map[string]any{{
		"artifactId": "alias", "type": "file", "url": sourceRef, "name": "夏日 #1?.txt",
		"mimeType": "text/plain", "sizeBytes": len(data), "sha256": hex.EncodeToString(digest[:]),
	}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	for _, ref := range []string{sourceRef, "artifacts/run-1/%E5%A4%8F%E6%97%A5%20%231%3F.txt"} {
		f, artifact, err := service.OpenArtifactByRef("chat-alias", ref)
		if err != nil || artifact.ArtifactID != "alias" {
			t.Fatalf("%s: artifact=%#v err=%v", ref, artifact, err)
		}
		f.Close()
	}
	// The bare form is a URL reference and still reserves these characters.
	if _, _, err = service.OpenArtifactByRef("chat-alias", "artifacts/run-1/夏日 #1?.txt"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("bare literal error=%v", err)
	}
}

func TestOpenArtifactByRefRejectsNonCanonicalReferences(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err = store.EnsureChat("chat-ref-invalid", "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	for _, sourceRef := range []string{
		"/artifacts/run-1/report.pdf",
		`artifacts\run-1\report.pdf`,
		"artifacts/run-1/report.pdf?download=1",
		"artifacts/run-1/report.pdf#page=1",
		"artifacts//report.pdf",
		"artifacts/run-1/nested/report.pdf",
		"artifacts/run-1/../report.pdf",
		"artifacts/run-1/%2e%2e",
		"artifacts/run-1/%252e%252e",
		" artifacts/run-1/report.pdf",
	} {
		t.Run(sourceRef, func(t *testing.T) {
			if _, _, err := service.OpenArtifactByRef("chat-ref-invalid", sourceRef); !errors.Is(err, ErrArtifactNotFound) {
				t.Fatalf("sourceRef=%q error=%v", sourceRef, err)
			}
		})
	}
}
