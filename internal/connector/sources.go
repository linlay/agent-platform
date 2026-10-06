package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Sources resolves platform-owned packages and external installations through
// one namespace. BuiltinRoot is supplied by application assembly, never YAML.
type Sources struct {
	ExternalRoot string
	BuiltinRoot  string
	// Native directories are verified, leased packages from the running binary.
	NativeKanbanControlDir   string
	NativeTaskControlDir     string
	NativePlatformControlDir string
	NativeWebControlDir      string
	StateRoot                string
}

var ErrBuiltinReadOnly = errors.New("builtin connectors are platform-owned and cannot be modified or deleted")

func IsBuiltin(id string) bool { return strings.HasPrefix(strings.ToLower(id), "builtin.") }

func (s Sources) Root(id string) string {
	if IsBuiltin(id) {
		return s.BuiltinRoot
	}
	return s.ExternalRoot
}

func (s Sources) Load(id string) (Package, error) {
	if id == "builtin.desktop" || id == "builtin.desktop-web" {
		return Package{}, fmt.Errorf("retired connector %s; run the offline migration", id)
	}
	if !ValidID(id) {
		return Package{}, fmt.Errorf("invalid connector id %q", id)
	}
	if dir := s.embeddedNativeDir(id); dir != "" {
		pkg, err := LoadDirectory(dir, id)
		pkg.Builtin, pkg.StateRoot = true, s.PersistentRoot()
		return pkg, err
	}
	root := s.Root(id)
	if strings.TrimSpace(root) == "" {
		return Package{}, fmt.Errorf("connector %s: %w", id, os.ErrNotExist)
	}
	pkg, err := Load(root, id)
	pkg.Builtin = IsBuiltin(id)
	pkg.StateRoot = s.PersistentRoot()
	return pkg, err
}

func (s Sources) LoadAll() ([]Package, error) {
	return s.loadAllExcept("")
}

func (s Sources) loadAllExcept(exclude string) ([]Package, error) {
	packages := []Package{}
	for _, id := range NativeConnectorIDs() {
		if s.embeddedNativeDir(id) == "" || id == exclude {
			continue
		}
		pkg, err := s.Load(id)
		if err != nil {
			return nil, err
		}
		packages = append(packages, pkg)
	}
	for _, source := range []struct {
		root    string
		builtin bool
	}{{s.BuiltinRoot, true}, {s.ExternalRoot, false}} {
		if strings.TrimSpace(source.root) == "" {
			continue
		}
		entries, err := os.ReadDir(source.root)
		if os.IsNotExist(err) && !source.builtin {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			id := entry.Name()
			if id == "builtin.desktop" || id == "builtin.desktop-web" || id == exclude || s.embeddedNativeDir(id) != "" {
				continue
			}
			if strings.HasPrefix(id, ".") {
				continue
			}
			// External packages are directories; root-level regular files
			// (README, user pin preferences, etc.) are not packages. Directories and symlinks
			// still go through normal package validation.
			if !source.builtin && entry.Type().IsRegular() {
				continue
			}
			// Legacy runtime copies never override or become fallback builtins.
			// Ignore them without reading their contents or changing user data.
			if !source.builtin && IsBuiltin(id) {
				continue
			}
			if source.builtin && !IsBuiltin(id) {
				return nil, fmt.Errorf("platform connector %q must use the builtin namespace", id)
			}
			pkg, err := s.Load(id)
			if err != nil {
				return nil, err
			}
			packages = append(packages, pkg)
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].ID < packages[j].ID })
	return packages, nil
}

func (s Sources) ReadFile(id, file string) (File, error) {
	if !ValidID(id) || !definitionFile(file) {
		return File{}, fmt.Errorf("invalid connector definition target")
	}
	pkg, err := s.Load(id)
	if err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(filepath.Join(pkg.Dir, file))
	if err != nil {
		return File{}, err
	}
	return File{ID: id, File: file, Content: string(data), SHA256: digest(data)}, nil
}

func (s Sources) embeddedNativeDir(id string) string {
	switch id {
	case KanbanControlConnectorID:
		return s.NativeKanbanControlDir
	case TaskControlConnectorID:
		return s.NativeTaskControlDir
	case PlatformControlConnectorID:
		return s.NativePlatformControlDir
	case WebControlConnectorID:
		return s.NativeWebControlDir
	default:
		return ""
	}
}
