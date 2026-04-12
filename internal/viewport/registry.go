package viewport

import (
	"encoding/json"
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
			"html":        `<div data-viewport="confirm_dialog"><p>builtin ask-user viewport placeholder</p></div>`,
		}, true, nil
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
	return filepath.Join(registriesDir, "viewports")
}

func DefaultServersRoot(registriesDir string) string {
	return filepath.Join(registriesDir, "viewport-servers")
}

func MissingViewportError(viewportKey string) error {
	return fmt.Errorf("viewport %s not found", viewportKey)
}
