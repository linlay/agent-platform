package conversationexport

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
)

// BuildSnapshotAttachments projects manifest metadata without reading file bytes.
func BuildSnapshotAttachments(chatID string, items []chat.ArtifactManifestItem) ([]AttachmentV1, []OmittedResourceV1) {
	if !chat.ValidChatID(chatID) {
		return nil, nil
	}
	latest := map[string]int{}
	key := func(raw string) string {
		if ref, err := chatresource.NormalizeSourceRef(raw); err == nil {
			return ref
		}
		return raw
	}
	for i, item := range items {
		latest[key(item.URL)] = i
	}
	attachments := make([]AttachmentV1, 0, len(latest))
	var omitted []OmittedResourceV1
	ids := map[string]string{}
	for i, item := range items {
		if latest[key(item.URL)] != i {
			continue
		}
		ref, runID, refErr := chatresource.CanonicalArtifactRef(chatID, item.URL)
		mimeType, _, mimeErr := mime.ParseMediaType(item.MimeType)
		mimeType = strings.ToLower(strings.TrimSpace(mimeType))
		hash, hashErr := hex.DecodeString(item.SHA256)
		id := snapshotResourceID(ref)
		if refErr != nil || item.Type != "file" || runID != item.RunID ||
			item.Name == "" || !utf8.ValidString(item.Name) || len(item.Name) > 255 ||
			strings.ContainsAny(item.Name, `/\`) || strings.ContainsFunc(item.Name, unicode.IsControl) ||
			mimeErr != nil || !strings.Contains(mimeType, "/") || item.SizeBytes < 0 ||
			hashErr != nil || len(hash) != sha256.Size || item.SHA256 != strings.ToLower(item.SHA256) ||
			(ids[id] != "" && ids[id] != ref) {
			omitted = append(omitted, omittedResource(item.URL, "invalid"))
			continue
		}
		ids[id] = ref
		attachments = append(attachments, AttachmentV1{ID: id, Name: item.Name, MIMEType: mimeType, Size: item.SizeBytes, SHA256: item.SHA256, SourceRef: ref})
	}
	return attachments, omitted
}
