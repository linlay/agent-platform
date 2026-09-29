package conversationexport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
)

// BuildSnapshotAttachments maps the published manifest into the public
// Snapshot V1 contract. Artifact bytes remain owned by chatresource and are
// deliberately not opened while exporting the timeline.
func BuildSnapshotAttachments(chatID string, items []chat.ArtifactManifestItem) ([]AttachmentV1, int) {
	if !chat.ValidChatID(chatID) {
		return nil, 0
	}
	latest := make(map[string]int, len(items))
	for index, item := range items {
		latest[item.URL] = index
	}

	attachments := make([]AttachmentV1, 0, len(latest))
	ids := make(map[string]string, len(latest))
	skipped := 0
	for index, item := range items {
		if latest[item.URL] != index {
			continue
		}
		ref, runID, err := canonicalSnapshotArtifactRef(chatID, item.URL)
		if err != nil || item.Type != "file" || runID != item.RunID {
			skipped++
			continue
		}
		if item.Name == "" || !utf8.ValidString(item.Name) || len(item.Name) > 255 ||
			strings.ContainsAny(item.Name, `/\`) || strings.ContainsFunc(item.Name, unicode.IsControl) {
			skipped++
			continue
		}
		mimeType, _, err := mime.ParseMediaType(item.MimeType)
		mimeType = strings.ToLower(strings.TrimSpace(mimeType))
		if err != nil || !strings.Contains(mimeType, "/") {
			skipped++
			continue
		}
		if item.SizeBytes < 0 {
			skipped++
			continue
		}
		fileHash := strings.TrimSpace(item.SHA256)
		if item.SHA256 != fileHash || fileHash != strings.ToLower(fileHash) || len(fileHash) != sha256.Size*2 {
			skipped++
			continue
		}
		if _, err := hex.DecodeString(fileHash); err != nil {
			skipped++
			continue
		}
		digest := sha256.Sum256([]byte(ref))
		id := hex.EncodeToString(digest[:12])
		if previous, exists := ids[id]; exists && previous != ref {
			skipped++
			continue
		}
		ids[id] = ref
		attachments = append(attachments, AttachmentV1{
			ID: id, Name: item.Name, MIMEType: mimeType,
			Size: item.SizeBytes, SHA256: fileHash, SourceRef: ref,
		})
	}
	return attachments, skipped
}

func canonicalSnapshotArtifactRef(chatID, raw string) (string, string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, `\?#`) {
		return "", "", fmt.Errorf("invalid published artifact path %q", raw)
	}
	owner, relativePath, err := chat.ParseResourceKey(chatID + "/" + raw)
	if err != nil || owner != chatID {
		return "", "", fmt.Errorf("invalid published artifact path %q", raw)
	}
	segments := strings.Split(relativePath, "/")
	if len(segments) != 3 || segments[0] != "artifacts" || segments[1] == "" || segments[2] == "" {
		return "", "", fmt.Errorf("published artifact must use artifacts/<runId>/<file>: %q", raw)
	}
	canonicalRef, err := chat.BuildChatScopeRef(relativePath)
	if err != nil || canonicalRef != raw {
		return "", "", fmt.Errorf("published artifact path is not canonical: %q", raw)
	}
	return canonicalRef, segments[1], nil
}
