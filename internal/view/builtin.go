package view

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"agent-platform/internal/resources"
)

func BuiltinDocument(key string) (Document, error) {
	ref, err := ResolveBuiltin(key)
	if err != nil {
		return Document{}, err
	}
	if ref.Renderer != "html" {
		return Document{}, ErrNotFound
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "/\\") || key == "." || key == ".." {
		return Document{}, fmt.Errorf("invalid view key")
	}
	html, err := resources.ViewFS.ReadFile("views/" + key + ".html")
	if errors.Is(err, fs.ErrNotExist) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	page := string(html)
	// Shared assets are embedded inline: review pages remain offline and retain
	// the existing CSP. Only builtin, fixed markers are expanded.
	for _, asset := range []struct{ marker, path string }{
		{"/* REVIEW_STYLES */", "views/shared/review.css"},
		{"/* REVIEW_SCRIPT */", "views/shared/review.js"},
	} {
		if strings.Contains(page, asset.marker) {
			content, err := resources.ViewFS.ReadFile(asset.path)
			if err != nil {
				return Document{}, err
			}
			page = strings.ReplaceAll(page, asset.marker, string(content))
		}
	}
	// Every builtin template gets the same sizing bridge, including templates
	// that do not use the review renderer. Templates own their natural layout.
	resize, err := resources.ViewFS.ReadFile("views/shared/resize.js")
	if err != nil {
		return Document{}, err
	}
	bridge := "<script data-view-resize>" + string(resize) + "</script>"
	if end := strings.LastIndex(strings.ToLower(page), "</body>"); end >= 0 {
		page = page[:end] + bridge + page[end:]
	} else {
		page += bridge
	}
	return Document{View: *ref, HTML: page}, nil
}

// ResolveBuiltin derives rendering metadata from the reserved component keys or embedded HTML.
func ResolveBuiltin(key string) (*Reference, error) {
	if !idPattern.MatchString(key) {
		return nil, ErrInvalid
	}
	switch key {
	case "question", "approval", "planning", "confirm_dialog", "team-hitl":
		return &Reference{Source: "builtin", Key: key, Renderer: "native"}, nil
	}
	if _, err := resources.ViewFS.ReadFile("views/" + key + ".html"); err != nil {
		return nil, ErrNotFound
	}
	return &Reference{Source: "builtin", Key: key, Renderer: "html"}, nil
}

// Builtin is for statically known references; input boundaries must use ResolveBuiltin.
func Builtin(key string) *Reference {
	ref, err := ResolveBuiltin(key)
	if err != nil {
		panic(err)
	}
	return ref
}
func RejectLegacy(fields map[string]any) error {
	for _, key := range []string{"viewportType", "viewportKey"} {
		if _, ok := fields[key]; ok {
			return fmt.Errorf("%w: %s is no longer supported; use view", ErrInvalid, key)
		}
	}
	return nil
}
