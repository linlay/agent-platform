package chatresource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/documentmeta"
)

var ErrInvalidResource = errors.New("invalid export resource")
var ErrUnsupportedResource = errors.New("unsupported export resource")

type Resource struct {
	Name, MIMEType, SHA256 string
	SizeBytes              int64
}

// Export binds one manifest read to one Chat. It is never shared across requests.
type Export struct {
	service   *Service
	chatID    string
	Items     []chat.ArtifactManifestItem
	Err       error
	published map[string]chat.ArtifactManifestItem
}

func (s *Service) Export(chatID string) *Export {
	items, err := s.PublishedArtifacts(chatID)
	export := &Export{service: s, chatID: chatID, Items: items, Err: err, published: map[string]chat.ArtifactManifestItem{}}
	for _, item := range items {
		if ref, err := NormalizeSourceRef(item.URL); err == nil {
			export.published[ref] = item
		}
	}
	return export
}

func IsMedia(mime string) bool {
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/")
}

// NormalizeSourceRef accepts the two current-Chat spellings, never a
// transport URL, absolute path, another scope, or an internal Chat directory.
func NormalizeSourceRef(raw string) (string, error) {
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

func (e *Export) Open(ctx context.Context, sourceRef string) (*os.File, Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, Resource{}, err
	}
	ref, err := NormalizeSourceRef(sourceRef)
	if !chat.ValidChatID(e.chatID) || err != nil || ref != sourceRef {
		return nil, Resource{}, ErrInvalidResource
	}
	if strings.HasPrefix(ref, "artifacts/") && e.Err != nil {
		return nil, Resource{}, e.Err
	}
	var published *chat.ArtifactManifestItem
	if item, ok := e.published[ref]; ok {
		if _, _, err := CanonicalArtifactRef(e.chatID, item.URL); err != nil {
			return nil, Resource{}, ErrInvalidResource
		}
		published = &item
	}
	f, item, err := e.service.openResource(ctx, e.chatID+"/"+ref, published)
	item.MIMEType = documentmeta.NormalizeMIME(item.MIMEType)
	return f, item, err
}

func FailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrInvalidResource):
		return "invalid"
	case errors.Is(err, ErrUnsupportedResource):
		return "unsupported"
	case errors.Is(err, os.ErrPermission):
		return "forbidden"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, chat.ErrChatNotFound), errors.Is(err, ErrArtifactNotFound):
		return "not_found"
	case errors.Is(err, ErrArtifactChanged):
		return "changed"
	default:
		return "read_failed"
	}
}

// openResource owns the file lifetime until validation succeeds. Published
// files are verified before any bytes are exposed; other files must be media.
func (s *Service) openResource(ctx context.Context, key string, published *chat.ArtifactManifestItem) (file *os.File, meta Resource, err error) {
	if err = ctx.Err(); err != nil {
		return nil, meta, err
	}
	filename, err := s.ResolveResource(key)
	if err != nil {
		return nil, meta, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, meta, err
	}
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer func() {
		stop()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			f.Close()
			file = nil
		}
	}()
	info, err := f.Stat()
	if err != nil {
		if published != nil {
			return nil, meta, ErrArtifactChanged
		}
		return nil, meta, err
	}
	if published != nil {
		if !info.Mode().IsRegular() || info.Size() != published.SizeBytes || published.SHA256 == "" {
			return nil, meta, ErrArtifactChanged
		}
		hash := sha256.New()
		written, err := io.Copy(hash, io.LimitReader(f, info.Size()+1))
		if err != nil {
			return nil, meta, err
		}
		if written != published.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != published.SHA256 {
			return nil, meta, ErrArtifactChanged
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return nil, meta, err
		}
		return f, Resource{published.Name, published.MimeType, published.SHA256, published.SizeBytes}, nil
	}
	if !info.Mode().IsRegular() {
		return nil, meta, ErrUnsupportedResource
	}
	metadata, err := documentmeta.ResolveReader(info.Name(), f)
	if err != nil {
		return nil, meta, err
	}
	mime := documentmeta.NormalizeMIME(metadata.MIMEType)
	if !IsMedia(mime) {
		return nil, meta, ErrUnsupportedResource
	}
	return f, Resource{info.Name(), mime, "", info.Size()}, nil
}
