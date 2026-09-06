package chat

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	return relativePath, targetPath, resourceImageRevision(info)
}

func TestCommitResourceDocumentImageOverwritesExpectedArtifactRevision(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-image", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, targetPath, revision := seedResourceImageArtifact(t, store, "chat-image")
	edited := append(resourceImageTestPNG(2), 3) // Different size makes the stale revision deterministic.
	result, err := store.CommitResourceDocument(ResourceDocumentCommitRequest{
		DocumentKind:     "document-image",
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
	if result.ArtifactID != "artifact-1" || result.ResourceID != "artifact-1" || result.RelativePath != relativePath || result.Revision == "" {
		t.Fatalf("unexpected result %#v", result)
	}
	if got, err := os.ReadFile(targetPath); err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("edited file=%x err=%v", got, err)
	}
	detail, err := store.LoadChat("chat-image")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Artifact == nil || len(detail.Artifact.Items) != 1 || detail.Artifact.Items[0].SHA256 != resourceImageSHA256(edited) || detail.Artifact.Items[0].SizeBytes != int64(len(edited)) {
		t.Fatalf("artifact manifest not updated: %#v", detail.Artifact)
	}
	_, err = store.CommitResourceDocument(ResourceDocumentCommitRequest{
		DocumentKind: "document-image",
		ChatID:       "chat-image", Profile: "artifact", ResourceID: "artifact-1",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		MIMEType: "image/png", Data: resourceImageTestPNG(3),
	})
	if !errors.Is(err, ErrResourceDocumentRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func TestCommitResourceDocumentImageCreatesArtifactFromArtifactOrReference(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-image-new", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, sourcePath, revision := seedResourceImageArtifact(t, store, "chat-image-new")
	result, err := store.CommitResourceDocument(ResourceDocumentCommitRequest{
		DocumentKind: "document-image",
		ChatID:       "chat-image-new", Profile: "artifact", ResourceID: "artifact-1",
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

	if got, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(got, resourceImageTestPNG(1)) {
		t.Fatalf("source artifact changed: %x, %v", got, err)
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
	referenceInfo, err := os.Stat(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	referenceResult, err := store.CommitResourceDocument(ResourceDocumentCommitRequest{
		DocumentKind: "document-image",
		ChatID:       "chat-image-new", Profile: "reference", ResourceID: "reference-1",
		RelativePath: referencePath, Mode: "new-artifact", ExpectedRevision: resourceImageRevision(referenceInfo), MIMEType: "image/webp", Data: webp,
	})
	if err != nil || referenceResult.ArtifactID == "" {
		t.Fatalf("reference new artifact=%#v err=%v", referenceResult, err)
	}
	_, err = store.CommitResourceDocument(ResourceDocumentCommitRequest{
		DocumentKind: "document-image",
		ChatID:       "chat-image-new", Profile: "reference", ResourceID: "reference-1",
		RelativePath: referencePath, Mode: "overwrite", ExpectedRevision: "1:1",
		MIMEType: "image/webp", Data: webp,
	})
	if !errors.Is(err, ErrResourceDocumentOverwriteDenied) {
		t.Fatalf("expected reference overwrite denial, got %v", err)
	}
}

func newImageDocumentCommitTest(t *testing.T) (*FileStore, ResourceDocumentCommitRequest) {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChat("chat-image", "agent-1", "", "image"); err != nil {
		t.Fatal(err)
	}
	relativePath, _, revision := seedResourceImageArtifact(t, store, "chat-image")
	return store, ResourceDocumentCommitRequest{
		ChatID: "chat-image", Profile: "artifact", ResourceID: "artifact-1",
		RelativePath: relativePath, Mode: "overwrite", ExpectedRevision: revision,
		DocumentKind: "document-image", MIMEType: "image/png", Data: resourceImageTestPNG(2),
	}
}

// Include resource files and directories so rejected commits cannot leave new
// artifacts or staging files behind. Timestamps are excluded for rollback.
func resourceDocumentFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[path+string(os.PathSeparator)] = ""
			return nil
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommitResourceDocumentImageRejectsInvalidRequestsWithoutMutation(t *testing.T) {
	store, base := newImageDocumentCommitTest(t)
	before := resourceDocumentFiles(t, store.ChatDir(base.ChatID))
	cases := []struct {
		name   string
		change func(*ResourceDocumentCommitRequest)
		want   error
	}{
		{"identity", func(r *ResourceDocumentCommitRequest) { r.ResourceID = "wrong" }, ErrResourceDocumentIdentityMismatch},
		{"domain", func(r *ResourceDocumentCommitRequest) { r.RelativePath = "references/source.png" }, ErrResourceDocumentInvalid},
		{"encoded traversal", func(r *ResourceDocumentCommitRequest) { r.RelativePath = "artifacts/%2e%2e/source.png" }, ErrResourceDocumentInvalid},
		{"cross chat", func(r *ResourceDocumentCommitRequest) { r.RelativePath = "../chat-other/source.png" }, ErrResourceDocumentInvalid},
		{"signature", func(r *ResourceDocumentCommitRequest) { r.Data = []byte("not-png") }, ErrResourceDocumentInvalid},
		{"empty image", func(r *ResourceDocumentCommitRequest) { r.Data = nil }, ErrResourceDocumentInvalid},
		{"mime", func(r *ResourceDocumentCommitRequest) { r.MIMEType = "image/jpeg" }, ErrResourceDocumentInvalid},
		{"extension", func(r *ResourceDocumentCommitRequest) { r.RelativePath = "artifacts/run-1/source.jpg" }, ErrResourceDocumentInvalid},
		{"empty revision", func(r *ResourceDocumentCommitRequest) { r.ExpectedRevision = "" }, ErrResourceDocumentRevisionConflict},
		{"stale revision", func(r *ResourceDocumentCommitRequest) { r.ExpectedRevision = "1:1" }, ErrResourceDocumentRevisionConflict},
	}
	for _, mode := range []string{"overwrite", "new-artifact"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				request := base
				request.Mode = mode
				tc.change(&request)
				if result, err := store.CommitResourceDocument(request); !errors.Is(err, tc.want) || result != (ResourceDocumentCommitResult{}) {
					t.Fatalf("result=%#v err=%v, want %v", result, err, tc.want)
				}
				if after := resourceDocumentFiles(t, store.ChatDir(base.ChatID)); !reflect.DeepEqual(after, before) {
					t.Fatal("rejected commit changed resource files")
				}
			})
		}
	}
}

func TestCommitResourceDocumentImageRejectsSymlinkEscape(t *testing.T) {
	store, request := newImageDocumentCommitTest(t)
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, resourceImageTestPNG(1), 0o644); err != nil {
		t.Fatal(err)
	}
	request.Profile, request.ResourceID, request.RelativePath = "reference", "reference-1", "source.png"
	if err := os.Symlink(outside, filepath.Join(store.ChatDir(request.ChatID), request.RelativePath)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	request.Mode, request.ExpectedRevision = "new-artifact", resourceImageRevision(info)
	before := resourceDocumentFiles(t, store.ChatDir(request.ChatID))
	if _, err := store.CommitResourceDocument(request); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("escape error=%v, want permission denied", err)
	}
	if after := resourceDocumentFiles(t, store.ChatDir(request.ChatID)); !reflect.DeepEqual(after, before) {
		t.Fatal("escape changed resource or external file")
	}
}

func TestCommitResourceDocumentImageReferenceFormats(t *testing.T) {
	for _, format := range []struct {
		ext, mime string
		data      []byte
	}{
		{"png", "image/png", resourceImageTestPNG(1)},
		{"jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 1}},
		{"jpeg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 1}},
		{"webp", "image/webp", []byte("RIFFxxxxWEBP1")},
	} {
		for _, prefix := range []string{"", "references/"} {
			t.Run(prefix+format.ext, func(t *testing.T) {
				store, request := newImageDocumentCommitTest(t)
				request.Profile, request.ResourceID = "reference", "reference-1"
				request.RelativePath = prefix + "source." + format.ext
				request.MIMEType, request.Data = format.mime, append(append([]byte(nil), format.data...), 2)
				source := filepath.Join(store.ChatDir(request.ChatID), filepath.FromSlash(request.RelativePath))
				if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, format.data, 0o644); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				request.ExpectedRevision = resourceImageRevision(info)
				if _, err := store.CommitResourceDocument(request); !errors.Is(err, ErrResourceDocumentOverwriteDenied) {
					t.Fatalf("reference overwrite error=%v", err)
				}
				request.Mode = "new-artifact"
				result, err := store.CommitResourceDocument(request)
				if err != nil {
					t.Fatal(err)
				}
				if result.ArtifactID == "" || result.ResourceID != result.ArtifactID || !strings.HasPrefix(result.RelativePath, "artifacts/document-edit-") {
					t.Fatalf("unexpected artifact: %#v", result)
				}
				if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, format.data) {
					t.Fatalf("source changed: %x, %v", data, err)
				}
				target := filepath.Join(store.ChatDir(request.ChatID), filepath.FromSlash(result.RelativePath))
				if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, request.Data) {
					t.Fatalf("artifact content=%x, %v", data, err)
				}
				info, err = os.Stat(target)
				if err != nil || result.Revision != resourceImageRevision(info) {
					t.Fatalf("artifact revision=%q, stat error=%v", result.Revision, err)
				}
			})
		}
	}
}

func TestCommitResourceDocumentImageManifestFailureRestoresResources(t *testing.T) {
	for _, mode := range []string{"overwrite", "new-artifact"} {
		t.Run(mode, func(t *testing.T) {
			store, request := newImageDocumentCommitTest(t)
			request.Mode = mode
			root := store.ChatDir(request.ChatID)
			before := resourceDocumentFiles(t, root)
			// Artifact directories remain writable: only manifest staging fails.
			manifestDir := filepath.Dir(artifactManifestPath(root))
			if err := os.Chmod(manifestDir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(manifestDir, 0o755) })
			if probe, err := os.CreateTemp(manifestDir, ".permission-probe-*"); err == nil {
				_ = probe.Close()
				_ = os.Remove(probe.Name())
				t.Skip("filesystem or current user does not enforce directory write permissions")
			} else if !errors.Is(err, os.ErrPermission) {
				t.Fatal(err)
			}
			result, err := store.CommitResourceDocument(request)
			var pathErr *os.PathError
			if !errors.Is(err, os.ErrPermission) || !errors.As(err, &pathErr) || !strings.HasPrefix(filepath.Base(pathErr.Path), ".artifacts-") {
				t.Fatalf("expected manifest staging failure, got %v", err)
			}
			if result != (ResourceDocumentCommitResult{}) {
				t.Fatalf("failed commit returned %#v", result)
			}
			if after := resourceDocumentFiles(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("manifest failure changed resources or left staging files")
			}
		})
	}
}
