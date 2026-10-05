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
	return map[string]any{"viewportKey": key, "html": string(html)}, nil
}
