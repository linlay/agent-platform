package viewport

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"agent-platform/internal/resources"
)

type Service struct{}

func NewService() *Service { return &Service{} }

func (s *Service) Get(_ context.Context, key string) (map[string]any, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "/\\") || key == "." || key == ".." {
		return nil, fmt.Errorf("invalid viewport key")
	}
	html, err := resources.ViewportFS.ReadFile("viewports/" + key + ".html")
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{"viewportKey": key, "status": "not_implemented"}, nil
	}
	if err != nil {
		return nil, err
	}
	page := string(html)
	// Shared assets are embedded inline: review pages remain offline and retain
	// the existing CSP. Only builtin, fixed markers are expanded.
	for _, asset := range []struct{ marker, path string }{
		{"/* REVIEW_STYLES */", "viewports/shared/review.css"},
		{"/* REVIEW_SCRIPT */", "viewports/shared/review.js"},
	} {
		if strings.Contains(page, asset.marker) {
			content, err := resources.ViewportFS.ReadFile(asset.path)
			if err != nil {
				return nil, err
			}
			page = strings.ReplaceAll(page, asset.marker, string(content))
		}
	}
	return map[string]any{"viewportKey": key, "html": page}, nil
}
