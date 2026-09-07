package connector

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Sources resolves platform-owned packages and external installations through
// one namespace. BuiltinRoot is supplied by application assembly, never YAML.
type Sources struct {
	ExternalRoot string
	BuiltinRoot  string
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
	if !ValidID(id) {
		return Package{}, fmt.Errorf("invalid connector id %q", id)
	}
	root := s.Root(id)
	if strings.TrimSpace(root) == "" {
		return Package{}, fmt.Errorf("connector %s: %w", id, os.ErrNotExist)
	}
	pkg, err := Load(root, id)
	pkg.Builtin = IsBuiltin(id)
	return pkg, err
}

func (s Sources) LoadAll() ([]Package, error) {
	packages := []Package{}
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
			if strings.HasPrefix(id, ".") {
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
	if _, err := s.Load(id); err != nil {
		return File{}, err
	}
	return ReadFile(s.Root(id), id, file)
}
