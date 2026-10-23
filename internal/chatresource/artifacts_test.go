package chatresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/chat"
)

func TestArtifactsRequireChatResolveAmbiguityAndVerifyContent(t *testing.T) {
	store := artifactStore(t, "chat-a", "chat-b")
	var err error
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
	f, meta, err := service.OpenArtifact(context.Background(), "chat-a", "same", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if meta.RunID != "run-a" || meta.ChatID != "chat-a" {
		t.Fatal(meta)
	}
	os.WriteFile(path, []byte("modified!"), 0600)
	if _, _, err = service.OpenArtifact(context.Background(), "chat-a", "same", ""); !errors.Is(err, ErrArtifactChanged) {
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
	store := artifactStore(t, "chat-ref-a", "chat-ref-b")
	var err error
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
	f, artifact, err := service.OpenArtifactByRef(context.Background(), "chat-ref-a", sourceRef)
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
	for _, duringResolve := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var reader chat.Store = store
		if duringResolve {
			reader = &cancelArtifactResolveStore{FileStore: store, cancel: cancel}
		} else {
			cancel()
		}
		opened, _, err := NewService(reader).OpenArtifactByRef(ctx, "chat-ref-a", sourceRef)
		cancel()
		if opened != nil {
			opened.Close()
			t.Fatal("cancelled read returned an open file")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("duringResolve=%v: err=%v", duringResolve, err)
		}
	}
	if _, _, err = service.OpenArtifactByRef(context.Background(), "chat-ref-a", "artifacts/run-1/missing.pdf"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("unpublished sourceRef error=%v", err)
	}
	if _, _, err = service.OpenArtifactByRef(context.Background(), "chat-ref-b", sourceRef); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("cross-chat sourceRef error=%v", err)
	}
	if err = os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.OpenArtifactByRef(context.Background(), "chat-ref-a", sourceRef); !errors.Is(err, ErrArtifactChanged) {
		t.Fatalf("changed sourceRef error=%v", err)
	}
}

type cancelArtifactResolveStore struct {
	*chat.FileStore
	cancel context.CancelFunc
}

func (s *cancelArtifactResolveStore) ResolveResource(key string) (string, error) {
	path, err := s.FileStore.ResolveResource(key)
	s.cancel()
	return path, err
}

// An @chat/ reference is a literal path, so "?" and "#" in a file name are
// part of the name rather than URL syntax.
func TestOpenArtifactByRefAcceptsLiteralChatAlias(t *testing.T) {
	store := artifactStore(t, "chat-alias")
	var err error
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
		f, artifact, err := service.OpenArtifactByRef(context.Background(), "chat-alias", ref)
		if err != nil || artifact.ArtifactID != "alias" {
			t.Fatalf("%s: artifact=%#v err=%v", ref, artifact, err)
		}
		f.Close()
	}
	// The bare form is a URL reference and still reserves these characters.
	if _, _, err = service.OpenArtifactByRef(context.Background(), "chat-alias", "artifacts/run-1/夏日 #1?.txt"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("bare literal error=%v", err)
	}
}

func TestOpenArtifactByRefRejectsNonCanonicalReferences(t *testing.T) {
	store := artifactStore(t, "chat-ref-invalid")
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
			if _, _, err := service.OpenArtifactByRef(context.Background(), "chat-ref-invalid", sourceRef); !errors.Is(err, ErrArtifactNotFound) {
				t.Fatalf("sourceRef=%q error=%v", sourceRef, err)
			}
		})
	}
}

func TestExportResourceReadsRejectUnsafeAndUnsupportedFiles(t *testing.T) {
	store := artifactStore(t, "media")
	root := store.ChatDir("media")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("ordinary text"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.svg")
	if err := os.WriteFile(outside, []byte("<svg></svg>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.svg")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, reason string }{
		{"fake.png", "unsupported"}, {"escape.svg", "forbidden"}, {"missing.png", "not_found"}, {".tools/secret.png", "invalid"}, {".btw/secret.png", "invalid"},
		{"../other/a.png", "invalid"}, {"%252e%252e/a.png", "invalid"}, {"a%2Fb.png", "invalid"}, {"C:/Windows/a.png", "invalid"}, {`C:\Windows\a.png`, "invalid"}, {"https://example.test/a.png", "invalid"}, {"/tmp/a.png", "invalid"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			f, _, err := NewService(store).Export("media").Open(context.Background(), tc.ref)
			if f != nil {
				f.Close()
			}
			if err == nil || FailureReason(err) != tc.reason {
				t.Fatalf("reason=%s err=%v", FailureReason(err), err)
			}
		})
	}
}

func artifactStore(t *testing.T, ids ...string) *chat.FileStore {
	t.Helper()
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range ids {
		if _, _, err := store.EnsureChat(id, "agent", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	return store
}
