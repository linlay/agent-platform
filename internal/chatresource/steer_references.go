package chatresource

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/multimodal"
	"agent-platform/internal/rootpaths"
)

// PrepareSteerReferences resolves current-chat files. Image blocks own their bytes;
// ordinary files are validated pointers for subsequent tool reads.
func PrepareSteerReferences(chatID, chatDir string, container bool, references []api.Reference) ([]api.Reference, []map[string]any, error) {
	roots, err := rootpaths.New("", filepath.Dir(chatDir), chatDir)
	if err != nil {
		return nil, nil, err
	}
	prepared := make([]api.Reference, 0, len(references))
	blocks := make([]map[string]any, 0, len(references))
	for _, ref := range references {
		if ref.Type != "" && ref.Type != "file" {
			return nil, nil, fmt.Errorf("steer references must be uploaded files")
		}
		u, err := url.Parse(strings.TrimSpace(ref.URL))
		if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
			return nil, nil, fmt.Errorf("steer attachment requires a current-chat resource URL")
		}
		_, rel, err := chat.ParseResourceKey(chatID + "/" + u.EscapedPath())
		if err != nil {
			return nil, nil, fmt.Errorf("invalid steer attachment resource: %w", err)
		}
		zone, canonical, err := roots.Classify(filepath.Join(chatDir, filepath.FromSlash(rel)))
		if err != nil || zone != rootpaths.ZoneCurrentChat {
			return nil, nil, fmt.Errorf("steer attachment must belong to the active chat")
		}
		// Derive metadata from the actual file, never the client's path/MIME.
		info, err := os.Stat(canonical.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot stat steer attachment %q: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("steer attachment must be a regular file")
		}
		file, err := os.Open(canonical.Host)
		if err != nil {
			return nil, nil, err
		}
		header := make([]byte, 512)
		n, readErr := file.Read(header)
		_ = file.Close()
		if readErr != nil && readErr != io.EOF {
			return nil, nil, readErr
		}
		detected := http.DetectContentType(header[:n])
		extensionMIME := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel)))
		mimeType, size := detected, info.Size()
		if strings.HasPrefix(detected, "image/") || strings.HasPrefix(extensionMIME, "image/") {
			image, err := multimodal.LoadImageFile(canonical.Host, "", multimodal.DefaultImageLoadOptions())
			if err != nil {
				return nil, nil, fmt.Errorf("cannot load steer image %q: %w", rel, err)
			}
			mimeType, size = image.MimeType, image.SizeBytes
			blocks = append(blocks, multimodal.OpenAIImageBlock(image))
		}
		resourceURL, err := chat.BuildChatScopeRef(rel)
		if err != nil {
			return nil, nil, err
		}
		path := canonical.Host
		if container {
			path = "/chat/" + filepath.ToSlash(rel)
		}
		prepared = append(prepared, api.Reference{ID: ref.ID, Type: "file", Name: filepath.Base(rel), Path: path, URL: resourceURL, MimeType: mimeType, SizeBytes: &size})
	}
	return prepared, blocks, nil
}
