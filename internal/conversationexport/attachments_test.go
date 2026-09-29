package conversationexport

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"agent-platform/internal/chat"
)

const attachmentTestChatID = "36af17d7-c7df-443b-85a1-7ad4c3b78c31"

func manifestAttachment(runID, name, mimeType, sourceRef string, size int64, hash string) chat.ArtifactManifestItem {
	return chat.ArtifactManifestItem{
		ArtifactItemState: chat.ArtifactItemState{
			Type: "file", Name: name, MimeType: mimeType, URL: sourceRef, SizeBytes: size, SHA256: hash,
		},
		RunID: runID,
	}
}

func TestBuildSnapshotAttachmentsMapsManifestResources(t *testing.T) {
	resources := []chat.ArtifactManifestItem{
		manifestAttachment("run-1", "page.html", "Text/HTML; Charset=UTF-8", "artifacts/run-1/page.html", 12, strings.Repeat("a", 64)),
		manifestAttachment("run-1", "image.png", "image/png", "artifacts/run-1/image.png", 24, strings.Repeat("b", 64)),
		manifestAttachment("run-1", "report.pdf", "application/pdf", "artifacts/run-1/report.pdf", 48, strings.Repeat("c", 64)),
	}
	wantMIMEs := []string{"text/html", "image/png", "application/pdf"}
	attachments, skipped := BuildSnapshotAttachments(attachmentTestChatID, resources)
	if skipped != 0 {
		t.Fatalf("skipped=%d", skipped)
	}
	if len(attachments) != len(resources) {
		t.Fatalf("attachments=%#v", attachments)
	}
	seen := map[string]bool{}
	for index, attachment := range attachments {
		resource := resources[index]
		digest := sha256.Sum256([]byte(resource.URL))
		wantID := hex.EncodeToString(digest[:12])
		if attachment.ID != wantID || attachment.Name != resource.Name || attachment.MIMEType != wantMIMEs[index] ||
			attachment.Size != resource.SizeBytes || attachment.SHA256 != resource.SHA256 || attachment.SourceRef != resource.URL || seen[attachment.ID] {
			t.Fatalf("attachment[%d]=%#v", index, attachment)
		}
		seen[attachment.ID] = true
	}
	again, _ := BuildSnapshotAttachments(attachmentTestChatID, resources)
	for index := range attachments {
		if attachments[index].ID != again[index].ID {
			t.Fatalf("unstable IDs: first=%#v second=%#v", attachments, again)
		}
	}
}

func TestBuildSnapshotAttachmentsLastSourceRefWins(t *testing.T) {
	sourceRef := "artifacts/run-1/report.pdf"
	items := []chat.ArtifactManifestItem{
		manifestAttachment("wrong-run", "stale.pdf", "Application/PDF", sourceRef, -1, "stale"),
		manifestAttachment("run-1", "report.pdf", "application/pdf", sourceRef, 10, strings.Repeat("d", 64)),
	}
	attachments, skipped := BuildSnapshotAttachments(attachmentTestChatID, items)
	if skipped != 0 {
		t.Fatalf("historical record counted as skipped: %d", skipped)
	}
	if len(attachments) != 1 || attachments[0].Name != "report.pdf" || attachments[0].Size != 10 {
		t.Fatalf("attachments=%#v", attachments)
	}
	items[1].SHA256 = "invalid"
	if attachments, skipped := BuildSnapshotAttachments(attachmentTestChatID, items); len(attachments) != 0 || skipped != 1 {
		t.Fatalf("fell back to stale artifact: %#v", attachments)
	}
}

func TestBuildSnapshotAttachmentsSkipsInvalidManifestFields(t *testing.T) {
	valid := manifestAttachment("run-1", "report.pdf", "application/pdf", "artifacts/run-1/report.pdf", 1, strings.Repeat("e", 64))
	tests := map[string]func(*chat.ArtifactManifestItem){
		"type":       func(item *chat.ArtifactManifestItem) { item.Type = "url" },
		"run id":     func(item *chat.ArtifactManifestItem) { item.RunID = "run-2" },
		"name empty": func(item *chat.ArtifactManifestItem) { item.Name = "" },
		"name path":  func(item *chat.ArtifactManifestItem) { item.Name = "nested/report.pdf" },
		"name long":  func(item *chat.ArtifactManifestItem) { item.Name = strings.Repeat("中", 86) },
		"MIME empty": func(item *chat.ArtifactManifestItem) { item.MimeType = "" },
		"size":       func(item *chat.ArtifactManifestItem) { item.SizeBytes = -1 },
		"hash":       func(item *chat.ArtifactManifestItem) { item.SHA256 = strings.Repeat("G", 64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			item := valid
			mutate(&item)
			other := manifestAttachment("run-1", "good.pdf", "application/pdf", "artifacts/run-1/good.pdf", 1, strings.Repeat("a", 64))
			if attachments, skipped := BuildSnapshotAttachments(attachmentTestChatID, []chat.ArtifactManifestItem{other, item}); len(attachments) != 1 || attachments[0].Name != other.Name || skipped != 1 {
				t.Fatalf("unexpected attachments %#v", attachments)
			}
		})
	}
}

func TestBuildSnapshotAttachmentsSkipsNonCanonicalPaths(t *testing.T) {
	for _, sourceRef := range []string{
		"/artifacts/run-1/report.pdf",
		`artifacts\run-1\report.pdf`,
		"artifacts/run-1/report.pdf?download=1",
		"artifacts/run-1/report.pdf#page=1",
		"artifacts/run-1/nested/report.pdf",
		"artifacts/run-1/../report.pdf",
		"artifacts/run-1/%2e%2e",
		"artifacts/run-1/%252e%252e",
		"/api/resource?file=chat/artifacts/run-1/report.pdf",
	} {
		t.Run(sourceRef, func(t *testing.T) {
			item := manifestAttachment("run-1", "report.pdf", "application/pdf", sourceRef, 1, strings.Repeat("f", 64))
			if attachments, skipped := BuildSnapshotAttachments(attachmentTestChatID, []chat.ArtifactManifestItem{item}); len(attachments) != 0 || skipped != 1 {
				t.Fatalf("accepted sourceRef %q", sourceRef)
			}
		})
	}
}
