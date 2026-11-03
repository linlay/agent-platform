package conversationexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

type OmittedResourceV1 struct {
	ID        string `json:"id,omitempty"`
	SourceRef string `json:"sourceRef,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

func snapshotResourceID(ref string) string {
	digest := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(digest[:12])
}

func safeResourceName(ref string) string {
	name, err := url.PathUnescape(path.Base(ref))
	if err != nil || name == "" || name == "." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return "resource"
	}
	return name
}

func omittedResource(raw, reason string) OmittedResourceV1 {
	ref, err := chatresource.NormalizeSourceRef(raw)
	if err != nil {
		return OmittedResourceV1{Name: "resource", Reason: reason}
	}
	return OmittedResourceV1{ID: snapshotResourceID(ref), SourceRef: ref, Name: safeResourceName(ref), Reason: reason}
}

// BuildDocument owns the complete export. Its projection and reference index are
// local, so serialization never mutates a caller's snapshot or retains hidden state.
func BuildDocument(ctx context.Context, summary *chat.Summary, events []stream.EventData, capturedAt int64, locale string, resolve ResolveAssistant, files *chatresource.Export) (SnapshotDocument, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotDocument{}, err
	}
	var refs map[string][]string
	if files != nil {
		refs = map[string][]string{}
	}
	snapshot, err := projectSnapshot(summary, events, capturedAt, locale, resolve, refs)
	if err != nil {
		return SnapshotDocument{}, err
	}
	if files != nil {
		snapshot.Attachments, snapshot.OmittedResources = BuildSnapshotAttachments(summary.ChatID, files.Items)
		if files.Err != nil && !errors.Is(files.Err, chatresource.ErrNotConfigured) {
			snapshot.OmittedResources = append(snapshot.OmittedResources, OmittedResourceV1{Name: "published attachments", Reason: "read_failed"})
		}
		if err := completeSnapshotResources(ctx, &snapshot, files, refs); err != nil {
			return SnapshotDocument{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return SnapshotDocument{}, err
	}
	encoded, err := json.Marshal(snapshot)
	return SnapshotDocument{Snapshot: snapshot, JSON: encoded}, err
}

func completeSnapshotResources(ctx context.Context, snapshot *SnapshotV1, files *chatresource.Export, refs map[string][]string) error {
	known := map[string]string{}
	nonMedia := map[string]bool{}
	for _, item := range snapshot.OmittedResources {
		if item.SourceRef != "" {
			known[item.SourceRef] = item.ID
		}
	}
	for _, item := range snapshot.Attachments {
		known[item.SourceRef] = item.ID
		nonMedia[item.SourceRef] = !chatresource.IsMedia(item.MIMEType)
	}
	omit := func(raw, reason string) string {
		item := omittedResource(raw, reason)
		key := item.SourceRef
		if key == "" {
			key = raw
		}
		if _, exists := known[key]; !exists {
			known[key] = item.ID
			snapshot.OmittedResources = append(snapshot.OmittedResources, item)
		}
		return item.ID
	}
	add := func(raw string, mediaOnly bool) string {
		if externalMediaRef(raw) {
			return ""
		}
		ref, err := chatresource.NormalizeSourceRef(raw)
		if err != nil {
			return omit(raw, "invalid")
		}
		if mediaOnly && nonMedia[ref] {
			omit("unsupported-media:"+ref, "unsupported")
			return ""
		}
		if id, exists := known[ref]; exists {
			return id
		}
		f, item, err := files.Open(ctx, ref)
		if err != nil {
			return omit(ref, chatresource.FailureReason(err))
		}
		f.Close()
		id := snapshotResourceID(ref)
		known[ref] = id
		snapshot.Attachments = append(snapshot.Attachments, AttachmentV1{ID: id, SourceRef: ref, Name: safeResourceName(ref), MIMEType: item.MIMEType, Size: item.SizeBytes, SHA256: item.SHA256})
		return id
	}
	// Published declarations remain download candidates even when optional HTML
	// discovery fails. Opening through this export reuses its manifest index.
	for i, item := range snapshot.Attachments {
		if item.MIMEType != "text/html" {
			continue
		}
		f, _, err := files.Open(ctx, item.SourceRef)
		if err != nil {
			continue
		}
		stop := context.AfterFunc(ctx, func() { f.Close() })
		media, err := htmlMediaRefs(io.NewSectionReader(f, 0, item.Size))
		stop()
		f.Close()
		if err != nil {
			continue
		}
		for _, raw := range media {
			if err := ctx.Err(); err != nil {
				return err
			}
			target := raw
			if !externalMediaRef(raw) && !chat.HasChatScopeAlias(raw) && !strings.HasPrefix(raw, "/") && !strings.ContainsAny(raw, `\:`) {
				target = path.Join(path.Dir(item.SourceRef), raw)
			}
			if id := add(target, true); id != "" {
				if snapshot.Attachments[i].MediaRefs == nil {
					snapshot.Attachments[i].MediaRefs = map[string]string{}
				}
				snapshot.Attachments[i].MediaRefs[raw] = id
			}
		}
	}
	for ti := range snapshot.Turns {
		for ni := range snapshot.Turns[ti].Nodes {
			node := &snapshot.Turns[ti].Nodes[ni]
			media := refs[node.ID]
			switch {
			case node.Kind == "content" || node.Kind == "planning":
				media = append(media, markdownMediaRefs(node.Text)...)
			case node.Kind == "tool" && node.Status == "success" && !node.ResultIsCode:
				media = append(media, markdownMediaRefs(node.ResultText)...)
			}
			for _, raw := range media {
				if err := ctx.Err(); err != nil {
					return err
				}
				if id := add(raw, false); id != "" && !slices.Contains(node.AttachmentIDs, id) {
					node.AttachmentIDs = append(node.AttachmentIDs, id)
				}
			}
		}
	}
	return ctx.Err()
}

func externalMediaRef(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") || strings.HasPrefix(raw, "//")
}

func queryResourceRefs(value any) []string {
	refs, ok := value.([]queryinput.Reference)
	if !ok {
		data, err := json.Marshal(value)
		if err != nil || json.Unmarshal(data, &refs) != nil {
			return nil
		}
	}
	var urls []string
	for _, ref := range refs {
		if (ref.Type == "file" || ref.Type == "image" || ref.Type == "audio" || ref.Type == "video") && ref.URL != "" {
			urls = append(urls, ref.URL)
		}
	}
	return urls
}

func generatedImageRefs(value any) []string {
	if text, ok := value.(string); ok && json.Unmarshal([]byte(text), &value) != nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok || record["ok"] == false {
		return nil
	}
	var urls []string
	for _, raw := range objectSlice(record["images"]) {
		if image, ok := raw.(map[string]any); ok {
			if url, ok := image["url"].(string); ok && url != "" {
				urls = append(urls, url)
			}
		}
	}
	return urls
}
