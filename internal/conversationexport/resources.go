package conversationexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/contracts/queryinput"
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

func SnapshotResourceReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, chatresource.ErrInvalidResource):
		return "invalid"
	case errors.Is(err, chatresource.ErrUnsupportedResource):
		return "unsupported"
	case errors.Is(err, os.ErrPermission):
		return "forbidden"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, chat.ErrChatNotFound), errors.Is(err, chatresource.ErrArtifactNotFound):
		return "not_found"
	case errors.Is(err, chatresource.ErrArtifactChanged):
		return "changed"
	default:
		return "read_failed"
	}
}

// BuildExportDocument enriches only resource exports; ordinary reads use the
// pure BuildSnapshot result. Both export formats encode their quota bytes once.
func BuildExportDocument(ctx context.Context, snapshot SnapshotV1, resources *chatresource.Service, items []chat.ArtifactManifestItem) (SnapshotDocument, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotDocument{}, err
	}
	if resources != nil {
		published, omitted := BuildSnapshotAttachments(snapshot.chatID, items)
		snapshot.Attachments = published
		snapshot.OmittedResources = append(snapshot.OmittedResources, omitted...)
		if err := completeSnapshotResources(ctx, &snapshot, resources); err != nil {
			return SnapshotDocument{}, err
		}
	}
	encoded, err := json.Marshal(snapshot)
	return SnapshotDocument{Snapshot: snapshot, JSON: encoded}, err
}

func completeSnapshotResources(ctx context.Context, snapshot *SnapshotV1, resources *chatresource.Service) error {
	// A single registry covers published candidates, discovered media and omissions.
	// Empty MIME denotes an omission; all stable identities use canonical refs.
	known := map[string]string{}
	for _, item := range snapshot.OmittedResources {
		if item.SourceRef != "" {
			known[item.SourceRef] = ""
		}
	}
	for _, item := range snapshot.Attachments {
		known[item.SourceRef] = item.MIMEType
	}
	omit := func(raw, reason string) string {
		item := omittedResource(raw, reason)
		key := item.SourceRef
		if key == "" {
			key = raw
		}
		if _, exists := known[key]; !exists {
			known[key] = ""
			snapshot.OmittedResources = append(snapshot.OmittedResources, item)
		}
		return item.ID
	}
	addMedia := func(raw string, mediaOnly bool) string {
		if externalMediaRef(raw) {
			return ""
		}
		ref, err := chatresource.NormalizeSourceRef(raw)
		if err != nil {
			return omit(raw, "invalid")
		}
		id := snapshotResourceID(ref)
		if mime, exists := known[ref]; exists {
			if mediaOnly && mime != "" && !chatresource.IsMedia(mime) {
				omit("unsupported-media:"+ref, "unsupported")
				return ""
			}
			return id
		}
		f, item, err := resources.OpenExportResource(ctx, snapshot.chatID, ref)
		if err != nil {
			return omit(ref, SnapshotResourceReason(err))
		}
		f.Close()
		known[ref] = item.MIMEType
		snapshot.Attachments = append(snapshot.Attachments, AttachmentV1{ID: id, SourceRef: ref, Name: safeResourceName(ref), MIMEType: item.MIMEType, Size: item.SizeBytes, SHA256: item.SHA256})
		return id
	}
	// Manifest declarations survive missing/changed files, as on main. Optional
	// HTML discovery must not remove a download candidate or inspect changed HTML.
	published := len(snapshot.Attachments)
	for i := 0; i < published; i++ {
		item := snapshot.Attachments[i]
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.MIMEType != "text/html" {
			continue
		}
		f, _, err := resources.OpenExportResource(ctx, snapshot.chatID, item.SourceRef)
		if err != nil {
			continue
		}
		stop := context.AfterFunc(ctx, func() { f.Close() })
		refs, err := htmlMediaRefs(io.NewSectionReader(f, 0, item.Size))
		stop()
		f.Close()
		if err != nil {
			continue
		}
		mappings := map[string]string{}
		for _, raw := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if externalMediaRef(raw) {
				continue
			}
			target := raw
			if !chat.HasChatScopeAlias(raw) && !strings.HasPrefix(raw, "/") && !strings.ContainsAny(raw, `\:`) {
				target = path.Join(path.Dir(item.SourceRef), raw)
			}
			if id := addMedia(target, true); id != "" {
				mappings[raw] = id
			}
		}
		if len(mappings) > 0 {
			snapshot.Attachments[i].MediaRefs = mappings
		}
	}
	for ti := range snapshot.Turns {
		for ni := range snapshot.Turns[ti].Nodes {
			node := &snapshot.Turns[ti].Nodes[ni]
			refs := node.resourceRefs
			if node.Kind == "content" || node.Kind == "planning" {
				refs = append(refs, markdownMediaRefs(node.Text)...)
			}
			if node.Kind == "tool" && node.Status == "success" && !node.ResultIsCode {
				refs = append(refs, markdownMediaRefs(node.ResultText)...)
			}
			ids := map[string]bool{}
			for _, raw := range refs {
				if err := ctx.Err(); err != nil {
					return err
				}
				if id := addMedia(raw, false); id != "" && !ids[id] {
					ids[id] = true
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
	var refs []queryinput.Reference
	switch items := value.(type) {
	case []queryinput.Reference:
		refs = items
	default:
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
	if text, ok := value.(string); ok {
		if json.Unmarshal([]byte(text), &value) != nil {
			return nil
		}
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
