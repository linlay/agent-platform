package conversationexport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
)

func TestBuildSnapshotAttachmentsUsesManifestAsOnlyAllowlist(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	root := t.TempDir()
	formal := filepath.Join(root, "artifacts", "run-1", "report.html")
	if err := os.MkdirAll(filepath.Dir(formal), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("<!doctype html><title>formal</title>")
	if err := os.WriteFile(formal, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.html"), []byte("root duplicate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "run-1", "unregistered.pdf"), []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tools", "hidden.html"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := []chat.ArtifactManifestItem{{ArtifactItemState: chat.ArtifactItemState{
		Type: "file", Name: "stale-name.html", URL: "artifacts/run-1/report.html", MimeType: "application/octet-stream", SizeBytes: 1, SHA256: "stale",
	}, RunID: "run-1"}}
	attachments, err := BuildSnapshotAttachments(chatID, items, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments=%#v", attachments)
	}
	digest := sha256.Sum256(contents)
	got := attachments[0]
	if got.Name != "report.html" || got.MIMEType != "text/html" || got.Size != int64(len(contents)) || got.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("attachment must describe the formal file: %#v", got)
	}
}

func TestBuildSnapshotAttachmentsLastManifestEntryWins(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	root := t.TempDir()
	file := filepath.Join(root, "artifacts", "run-1", "image.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("not-a-real-png-but-a-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := []chat.ArtifactManifestItem{
		{ArtifactItemState: chat.ArtifactItemState{Type: "legacy", URL: "artifacts/run-1/image.png"}, RunID: "wrong", PublishedAt: 1},
		{ArtifactItemState: chat.ArtifactItemState{Type: "file", URL: "artifacts/run-1/image.png"}, RunID: "run-1", PublishedAt: 2},
	}
	attachments, err := BuildSnapshotAttachments(chatID, items, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].SourceRef != "artifacts/run-1/image.png" {
		t.Fatalf("attachments=%#v", attachments)
	}
}

func TestBuildSnapshotAttachmentsRejectsOutsideFormalArtifactPath(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	for _, ref := range []string{"report.html", ".tools/artifacts.json", "artifacts/run-1/nested/report.html", "artifacts/run-1/../report.html"} {
		t.Run(strings.ReplaceAll(ref, "/", "_"), func(t *testing.T) {
			_, err := BuildSnapshotAttachments(chatID, []chat.ArtifactManifestItem{{ArtifactItemState: chat.ArtifactItemState{Type: "file", URL: ref}, RunID: "run-1"}}, t.TempDir())
			if err == nil {
				t.Fatalf("expected %q to be rejected", ref)
			}
		})
	}
}

func TestBuildSnapshotAttachmentsRejectsMissingManifestFile(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	_, err := BuildSnapshotAttachments(chatID, []chat.ArtifactManifestItem{{ArtifactItemState: chat.ArtifactItemState{
		Type: "file", URL: "artifacts/run-1/missing.pdf",
	}, RunID: "run-1"}}, t.TempDir())
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing artifact error=%v", err)
	}
}

func TestBuildSnapshotAttachmentsSupportsGeneralResourcesAndZeroBytes(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	root := t.TempDir()
	resources := []struct {
		name, mime string
		body       []byte
	}{
		{name: "page.html", mime: "text/html", body: []byte("<!doctype html><title>x</title>")},
		{name: "report.pdf", mime: "application/pdf", body: []byte("%PDF-1.7\n")},
		{name: "sheet.xlsx", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", body: []byte{'P', 'K', 3, 4, 0}},
		{name: "image.png", mime: "image/png", body: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
		{name: "empty.bin", mime: "text/plain", body: nil},
	}
	items := make([]chat.ArtifactManifestItem, 0, len(resources))
	for _, resource := range resources {
		file := filepath.Join(root, "artifacts", "run-1", resource.name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, resource.body, 0o600); err != nil {
			t.Fatal(err)
		}
		items = append(items, chat.ArtifactManifestItem{ArtifactItemState: chat.ArtifactItemState{
			Type: "file", URL: "artifacts/run-1/" + resource.name,
		}, RunID: "run-1"})
	}
	attachments, err := BuildSnapshotAttachments(chatID, items, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != len(resources) {
		t.Fatalf("attachments=%#v", attachments)
	}
	for index, resource := range resources {
		got := attachments[index]
		digest := sha256.Sum256(resource.body)
		if got.MIMEType != resource.mime || got.Size != int64(len(resource.body)) || got.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("attachment %s = %#v", resource.name, got)
		}
	}
	again, err := BuildSnapshotAttachments(chatID, items, root)
	if err != nil {
		t.Fatal(err)
	}
	seenIDs := map[string]bool{}
	for index := range attachments {
		if attachments[index].ID != again[index].ID || seenIDs[attachments[index].ID] {
			t.Fatalf("resource ids are not stable and distinct: first=%#v second=%#v", attachments, again)
		}
		seenIDs[attachments[index].ID] = true
	}
}

func TestBuildSnapshotAttachmentsRejectsSymlinkAndAggregateLimit(t *testing.T) {
	const chatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target.html")
		if err := os.WriteFile(target, []byte("<p>secret</p>"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "artifacts", "run-1", "report.html")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := BuildSnapshotAttachments(chatID, []chat.ArtifactManifestItem{{ArtifactItemState: chat.ArtifactItemState{
			Type: "file", URL: "artifacts/run-1/report.html",
		}, RunID: "run-1"}}, root)
		if err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("aggregate limit", func(t *testing.T) {
		root := t.TempDir()
		var items []chat.ArtifactManifestItem
		for _, name := range []string{"a.bin", "b.bin"} {
			file := filepath.Join(root, "artifacts", "run-1", name)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(file, MaxAttachmentBytes/2+1); err != nil {
				t.Fatal(err)
			}
			items = append(items, chat.ArtifactManifestItem{ArtifactItemState: chat.ArtifactItemState{Type: "file", URL: "artifacts/run-1/" + name}, RunID: "run-1"})
		}
		if _, err := BuildSnapshotAttachments(chatID, items, root); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestInspectArtifactRejectsFileReplacement(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "artifacts", "run-1", "report.html")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	filePath, expected, err := regularArtifactPath(root, "artifacts/run-1/report.html")
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement.html")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filePath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := inspectArtifact(filePath, expected, "report.html", MaxAttachmentBytes); err == nil ||
		!strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("replacement error=%v", err)
	}
}

func TestInspectArtifactRejectsMutationDuringRead(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "artifacts", "run-1", "report.bin")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("original")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	filePath, expected, err := regularArtifactPath(root, "artifacts/run-1/report.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = inspectArtifactWithReadHook(filePath, expected, "report.bin", MaxAttachmentBytes, func() error {
		return os.WriteFile(filePath, []byte("modified"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("mutation error=%v", err)
	}
}
