package chatresource

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/multimodal"
	"agent-platform/internal/rootpaths"
)

// PrepareSteerImages resolves only uploaded/current-chat resources. The returned
// blocks own the image bytes so later file mutations cannot change queued input.
func PrepareSteerImages(chatID, chatDir string, container bool, references []api.Reference) ([]api.Reference, []map[string]any, error) {
	roots, err := rootpaths.New("", filepath.Dir(chatDir), chatDir)
	if err != nil {
		return nil, nil, err
	}
	prepared := make([]api.Reference, 0, len(references))
	blocks := make([]map[string]any, 0, len(references))
	for _, ref := range references {
		if ref.Type != "" && ref.Type != "file" {
			return nil, nil, fmt.Errorf("steer references must be uploaded images")
		}
		u, err := url.Parse(strings.TrimSpace(ref.URL))
		if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
			return nil, nil, fmt.Errorf("steer image requires a current-chat resource URL")
		}
		_, rel, err := chat.ParseResourceKey(chatID + "/" + u.EscapedPath())
		if err != nil {
			return nil, nil, fmt.Errorf("invalid steer image resource: %w", err)
		}
		zone, canonical, err := roots.Classify(filepath.Join(chatDir, filepath.FromSlash(rel)))
		if err != nil || zone != rootpaths.ZoneCurrentChat {
			return nil, nil, fmt.Errorf("steer image must belong to the active chat")
		}
		// Detect from bytes rather than trusting the client-supplied MIME hint.
		image, err := multimodal.LoadImageFile(canonical.Host, "", multimodal.DefaultImageLoadOptions())
		if err != nil {
			return nil, nil, fmt.Errorf("cannot load steer image %q: %w", rel, err)
		}
		resourceURL, err := chat.BuildChatScopeRef(rel)
		if err != nil {
			return nil, nil, err
		}
		path := canonical.Host
		if container {
			path = "/chat/" + filepath.ToSlash(rel)
		}
		size := image.SizeBytes
		prepared = append(prepared, api.Reference{ID: ref.ID, Type: "file", Name: filepath.Base(rel), Path: path, URL: resourceURL, MimeType: image.MimeType, SizeBytes: &size})
		blocks = append(blocks, multimodal.OpenAIImageBlock(image))
	}
	return prepared, blocks, nil
}
