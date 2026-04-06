package viewport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Registry struct {
	root string
}

func NewRegistry(root string) *Registry {
	return &Registry{root: root}
}

func (r *Registry) Get(viewportKey string) (map[string]any, bool, error) {
	if strings.TrimSpace(viewportKey) == "confirm_dialog" {
		return map[string]any{
			"viewportKey": "confirm_dialog",
			"html":        `<div data-viewport="confirm_dialog"><p>confirm dialog viewport placeholder</p></div>`,
		}, true, nil
	}

	candidates := []string{
		filepath.Join(r.root, viewportKey+".html"),
		filepath.Join(r.root, viewportKey, "index.html"),
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return map[string]any{
				"viewportKey": viewportKey,
				"html":        string(data),
				"path":        path,
			}, true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
	}
	return nil, false, nil
}

func DefaultRoot(registriesDir string) string {
	return filepath.Join(registriesDir, "viewports")
}

func MissingViewportError(viewportKey string) error {
	return fmt.Errorf("viewport %s not found", viewportKey)
}
