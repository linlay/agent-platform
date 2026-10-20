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
	"agent-platform/internal/documentmeta"
	"agent-platform/internal/stream"
	"golang.org/x/net/html"
)

var ErrInvalidResource = errors.New("invalid export resource")
var ErrUnsupportedResource = errors.New("unsupported export resource")

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

// NormalizeSnapshotSourceRef accepts the two current-Chat spellings, never a
// transport URL, absolute path, another scope, or an internal Chat directory.
func NormalizeSnapshotSourceRef(raw string) (string, error) {
	raw = chat.BareChatScopeRef(raw)
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 || strings.ContainsAny(raw, `\?#:`) || strings.HasPrefix(raw, "@") {
		return "", ErrInvalidResource
	}
	_, relative, err := chat.ParseResourceKey("snapshot/" + raw)
	if err != nil || strings.HasPrefix(relative, "/") || strings.Contains(relative, ":") {
		return "", ErrInvalidResource
	}
	ref, err := chat.BuildChatScopeRef(relative)
	if err != nil || len(ref) > 2048 {
		return "", ErrInvalidResource
	}
	return ref, nil
}

func safeResourceName(ref string) string {
	name, err := url.PathUnescape(path.Base(ref))
	if err != nil || name == "" || name == "." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return "resource"
	}
	return name
}

func omittedResource(raw, reason string) OmittedResourceV1 {
	ref, err := NormalizeSnapshotSourceRef(raw)
	if err != nil {
		return OmittedResourceV1{Name: "resource", Reason: reason}
	}
	return OmittedResourceV1{ID: snapshotResourceID(ref), SourceRef: ref, Name: safeResourceName(ref), Reason: reason}
}

func SnapshotResourceReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrInvalidResource):
		return "invalid"
	case errors.Is(err, ErrUnsupportedResource):
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

func isSnapshotMedia(mime string) bool {
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/")
}

// OpenSnapshotResource is shared by export metadata collection and the thin
// Desktop read route. It does not rediscover references or broaden /api/resource.
func OpenSnapshotResource(ctx context.Context, chats chat.Store, chatID, sourceRef string) (*os.File, AttachmentV1, error) {
	if err := ctx.Err(); err != nil {
		return nil, AttachmentV1{}, err
	}
	ref, err := NormalizeSnapshotSourceRef(sourceRef)
	if !chat.ValidChatID(chatID) || err != nil || ref != sourceRef {
		return nil, AttachmentV1{}, ErrInvalidResource
	}
	resources := chatresource.NewService(chats)
	if strings.HasPrefix(ref, "artifacts/") {
		file, artifact, err := resources.OpenArtifactByRef(ctx, chatID, ref)
		if err != nil {
			return nil, AttachmentV1{}, err
		}
		return file, AttachmentV1{ID: snapshotResourceID(ref), SourceRef: ref, Name: artifact.Name, MIMEType: documentmeta.NormalizeMIME(artifact.MIMEType), Size: artifact.SizeBytes, SHA256: artifact.SHA256}, nil
	}
	_, relative, err := chat.ParseResourceKey(chatID + "/" + ref)
	if err != nil {
		return nil, AttachmentV1{}, ErrInvalidResource
	}
	key, err := chat.BuildResourceKey(chatID, relative)
	if err != nil {
		return nil, AttachmentV1{}, ErrInvalidResource
	}
	filename, err := resources.ResolveResource(key)
	if err != nil {
		return nil, AttachmentV1{}, err
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, AttachmentV1{}, err
	}
	valid := false
	defer func() {
		if !valid {
			file.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { file.Close() })
	defer stop()
	info, err := file.Stat()
	if err := ctx.Err(); err != nil {
		return nil, AttachmentV1{}, err
	}
	if err != nil {
		return nil, AttachmentV1{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, AttachmentV1{}, ErrUnsupportedResource
	}
	// Detect only a small prefix; the Desktop freezes and hashes the actual bytes.
	sample := make([]byte, 512+utf8.UTFMax-1)
	n, err := io.ReadFull(file, sample)
	if err := ctx.Err(); err != nil {
		return nil, AttachmentV1{}, err
	}
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, AttachmentV1{}, err
	}
	end := min(n, 512)
	metadata := documentmeta.Resolve(info.Name(), documentmeta.DetectMIME(info.Name(), sample[:end], sample[end:n], n <= 512), sample[:end], sample[end:n], n <= 512)
	mimeType := documentmeta.NormalizeMIME(metadata.MIMEType)
	if !isSnapshotMedia(mimeType) {
		return nil, AttachmentV1{}, ErrUnsupportedResource
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, AttachmentV1{}, err
	}
	valid = true
	return file, AttachmentV1{ID: snapshotResourceID(ref), SourceRef: ref, Name: safeResourceName(ref), MIMEType: mimeType, Size: info.Size()}, nil
}

// BuildSnapshotExportDocument is the only file-aware snapshot entry. Projection
// remains pure, resources are discovered once, and the body is encoded once.
func BuildSnapshotExportDocument(ctx context.Context, summary *chat.Summary, events []stream.EventData, chats chat.Store, capturedAt int64, locale string, resolveAssistant ResolveAssistant) (SnapshotDocument, error) {
	snapshot, err := projectSnapshot(summary, events, nil, capturedAt, locale, resolveAssistant, true)
	if err != nil {
		return SnapshotDocument{}, err
	}
	if err := completeSnapshotResources(ctx, &snapshot, chats, summary.ChatID); err != nil {
		return SnapshotDocument{}, err
	}
	return encodeSnapshot(snapshot)
}

func completeSnapshotResources(ctx context.Context, snapshot *SnapshotV1, chats chat.Store, chatID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var published []AttachmentV1
	if reader, ok := chats.(interface {
		PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
	}); ok {
		items, err := reader.PublishedArtifacts(chatID)
		if err != nil {
			snapshot.OmittedResources = append(snapshot.OmittedResources, OmittedResourceV1{Name: "published attachments", Reason: "read_failed"})
		} else {
			published, snapshot.OmittedResources = BuildSnapshotAttachments(chatID, items)
		}
	}
	seen := map[string]bool{}
	publishedByRef := make(map[string]AttachmentV1, len(published))
	for _, item := range published {
		publishedByRef[item.SourceRef] = item
	}
	omitted := map[string]bool{}
	for _, item := range snapshot.OmittedResources {
		if item.ID != "" {
			omitted[item.ID] = true
			seen[item.SourceRef] = true
		}
	}
	addOmission := func(raw, reason string) string {
		item := omittedResource(raw, reason)
		key := item.ID
		if key == "" {
			key = raw
		}
		if !omitted[key] {
			omitted[key] = true
			snapshot.OmittedResources = append(snapshot.OmittedResources, item)
		}
		return item.ID
	}
	// Published HTML dependencies are appended directly after their parent. The
	// closure only opens direct media; it never recurses through HTML/CSS/JS.
	addMedia := func(raw string, mediaOnly bool) string {
		if externalMediaRef(raw) {
			return ""
		}
		ref, err := NormalizeSnapshotSourceRef(raw)
		if err != nil {
			return addOmission(raw, "invalid")
		}
		id := snapshotResourceID(ref)
		if item, exists := publishedByRef[ref]; exists {
			if mediaOnly && !isSnapshotMedia(item.MIMEType) {
				// Keep a published download intact even when HTML misuses it as
				// media. Such a mapping cannot point to a successful non-media file.
				key := "unsupported-media:" + ref
				if !omitted[key] {
					omitted[key] = true
					snapshot.OmittedResources = append(snapshot.OmittedResources, OmittedResourceV1{Name: item.Name, Reason: "unsupported"})
				}
				return ""
			}
			return id // Its metadata is handled exactly once in manifest order.
		}
		if seen[ref] {
			return id
		}
		seen[ref] = true
		f, item, err := OpenSnapshotResource(ctx, chats, chatID, ref)
		if err != nil {
			addOmission(ref, SnapshotResourceReason(err))
			return id
		}
		f.Close()
		if !isSnapshotMedia(item.MIMEType) {
			addOmission(ref, "unsupported")
			return id
		}
		snapshot.Attachments = append(snapshot.Attachments, item)
		return id
	}
	for _, item := range published {
		if seen[item.SourceRef] {
			continue
		}
		seen[item.SourceRef] = true
		if err := ctx.Err(); err != nil {
			return err
		}
		// Non-HTML published files already carry manifest metadata; avoid reading
		// them twice. Desktop still verifies size/hash when freezing.
		if item.MIMEType != "text/html" {
			filename, err := chats.ResolveResource(chatID + "/" + item.SourceRef)
			if err != nil {
				addOmission(item.SourceRef, SnapshotResourceReason(err))
				continue
			}
			info, err := os.Stat(filename)
			if err != nil {
				addOmission(item.SourceRef, SnapshotResourceReason(err))
				continue
			}
			if info.Size() != item.Size {
				addOmission(item.SourceRef, "changed")
				continue
			}
			snapshot.Attachments = append(snapshot.Attachments, item)
			continue
		}
		f, _, err := OpenSnapshotResource(ctx, chats, chatID, item.SourceRef)
		if err != nil {
			addOmission(item.SourceRef, SnapshotResourceReason(err))
			continue
		}
		// Integrity was checked against this manifest size. Bound dependency
		// parsing to those bytes as well; snapshot JSON limits do not limit files.
		stop := context.AfterFunc(ctx, func() { f.Close() })
		refs, err := htmlMediaRefs(io.NewSectionReader(f, 0, item.Size))
		stop()
		f.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			addOmission(item.SourceRef, "read_failed")
			continue
		}
		position := len(snapshot.Attachments)
		snapshot.Attachments = append(snapshot.Attachments, item)
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
				// URL-relative paths may use ../ while remaining inside this Chat.
				target = path.Join(path.Dir(item.SourceRef), raw)
			}
			if id := addMedia(target, true); id != "" {
				mappings[raw] = id
			}
		}
		if len(mappings) > 0 {
			snapshot.Attachments[position].MediaRefs = mappings
		}
	}
	for ti := range snapshot.Turns {
		for ni := range snapshot.Turns[ti].Nodes {
			node := &snapshot.Turns[ti].Nodes[ni]
			refs := append([]string{}, node.resourceRefs...)
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

func htmlMediaRefs(reader io.Reader) ([]string, error) {
	tokenizer := html.NewTokenizer(reader)
	var refs []string
	seen := map[string]bool{}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				return refs, nil
			}
			return nil, tokenizer.Err()
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data != "img" && token.Data != "video" && token.Data != "audio" && token.Data != "source" {
			continue
		}
		for _, attr := range token.Attr {
			if attr.Key != "src" && !(attr.Key == "poster" && token.Data == "video") && !(attr.Key == "srcset" && (token.Data == "img" || token.Data == "source")) {
				continue
			}
			values := []string{attr.Val}
			if attr.Key == "srcset" {
				values = srcsetURLs(attr.Val)
			}
			for _, value := range values {
				if value != "" && !seen[value] {
					seen[value] = true
					refs = append(refs, value)
				}
			}
		}
	}
}

func srcsetURLs(value string) []string {
	var urls []string
	for value != "" {
		value = strings.TrimLeft(value, " \t\r\n\f,")
		if value == "" {
			break
		}
		end := strings.IndexAny(value, " \t\r\n\f")
		if end < 0 {
			end = len(value)
		}
		target := value[:end]
		value = value[end:]
		urls = append(urls, strings.TrimRight(target, ","))
		if strings.HasSuffix(target, ",") {
			continue
		}
		if end = strings.Index(value, ","); end >= 0 {
			value = value[end+1:]
		} else {
			value = ""
		}
	}
	return urls
}
