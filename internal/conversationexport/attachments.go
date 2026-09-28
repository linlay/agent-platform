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
func BuildSnapshotAttachments(chatID string, items []chat.ArtifactManifestItem) ([]AttachmentV1, error) {
	if !chat.ValidChatID(chatID) {
		return nil, fmt.Errorf("invalid conversation artifact context")
	}
	latest := make(map[string]int, len(items))
	refs := make([]string, len(items))
	runIDs := make([]string, len(items))
	for index, item := range items {
		ref, runID, err := canonicalSnapshotArtifactRef(chatID, item.URL)
		if err != nil {
			return nil, err
		}
		refs[index], runIDs[index] = ref, runID
		latest[ref] = index
	}

	attachments := make([]AttachmentV1, 0, len(latest))
	ids := make(map[string]string, len(latest))
	for index, item := range items {
		ref := refs[index]
		if latest[ref] != index {
			continue
		}
		if item.Type != "file" || strings.TrimSpace(item.RunID) == "" || runIDs[index] != item.RunID {
			return nil, fmt.Errorf("published artifact manifest identity mismatch: %q", item.URL)
		}
		if err := validateSnapshotAttachmentName(item.Name); err != nil {
			return nil, fmt.Errorf("invalid published artifact name for %q: %w", ref, err)
		}
		mimeType, err := validateSnapshotAttachmentMIME(item.MimeType)
		if err != nil {
			return nil, fmt.Errorf("invalid published artifact MIME for %q: %w", ref, err)
		}
		if item.SizeBytes < 0 {
			return nil, fmt.Errorf("invalid published artifact size for %q", ref)
		}
		fileHash := strings.TrimSpace(item.SHA256)
		if item.SHA256 != fileHash || fileHash != strings.ToLower(fileHash) || len(fileHash) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid published artifact hash for %q", ref)
		}
		if _, err := hex.DecodeString(fileHash); err != nil {
			return nil, fmt.Errorf("invalid published artifact hash for %q", ref)
		}
		digest := sha256.Sum256([]byte(ref))
		id := hex.EncodeToString(digest[:12])
		if previous, exists := ids[id]; exists && previous != ref {
			return nil, fmt.Errorf("published artifact id collision")
		}
		ids[id] = ref
		attachments = append(attachments, AttachmentV1{
			ID: id, Name: item.Name, MIMEType: mimeType,
			Size: item.SizeBytes, SHA256: fileHash, SourceRef: ref,
		})
	}
	return attachments, nil
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

func validateSnapshotAttachmentName(name string) error {
	if name == "" || !utf8.ValidString(name) || len([]byte(name)) > 255 ||
		strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("name does not satisfy Snapshot V1")
	}
	return nil
}

func validateSnapshotAttachmentMIME(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if err != nil || !strings.Contains(mediaType, "/") {
		return "", fmt.Errorf("MIME type does not satisfy Snapshot V1")
	}
	return mediaType, nil
}
