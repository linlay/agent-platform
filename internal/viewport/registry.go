package viewport

import (
	"agent-platform/internal/resources"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	// Builtins cannot be shadowed by a runtime file or remote viewport server.
	viewportKey = strings.TrimSpace(viewportKey)
	if viewportKey == "" || strings.ContainsAny(viewportKey, "/\\") || viewportKey == "." || viewportKey == ".." {
		return nil, false, fmt.Errorf("invalid viewport key")
	}
	html, err := resources.ViewportFS.ReadFile("viewports/" + viewportKey + ".html")
	if err == nil {
		return map[string]any{"viewportKey": viewportKey, "html": string(html)}, true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}

	// Try QLC (JSON schema) files first
	qlcCandidates := []string{
		filepath.Join(r.root, viewportKey+".qlc"),
		filepath.Join(r.root, viewportKey, "index.qlc"),
	}
	for _, path := range qlcCandidates {
		data, err := os.ReadFile(path)
		if err == nil {
			var payload map[string]any
			if jsonErr := json.Unmarshal(data, &payload); jsonErr == nil {
				return payload, true, nil
			}
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
	}

	// Try HTML files
	htmlCandidates := []string{
		filepath.Join(r.root, viewportKey+".html"),
		filepath.Join(r.root, viewportKey, "index.html"),
	}
	for _, path := range htmlCandidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return map[string]any{
				"viewportKey": viewportKey,
				"html":        string(data),
			}, true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
	}
	return nil, false, nil
}

func DefaultRoot(registriesDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(registriesDir)), "viewports")
}

func DefaultServersRoot(registriesDir string) string {
	return filepath.Join(registriesDir, "viewport-servers")
}
