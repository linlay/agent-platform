package temppaths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/pathutil"
)

type Classification string

const (
	Outside Classification = "outside"
	Inside  Classification = "inside"
	Escape  Classification = "escape"
)

// Resolver freezes the process temporary roots and their canonical forms.
// lexicalRoots intentionally keeps both presentation paths (for example
// /tmp) and canonical paths (for example /private/tmp) so a symlink escape
// cannot shed its temporary-root provenance during canonicalization.
type Resolver struct {
	primary      pathutil.Canonical
	roots        []pathutil.Canonical
	lexicalRoots []lexicalRoot
}

type lexicalRoot struct {
	path      string
	canonical pathutil.Canonical
}

var systemResolver = newSystemResolver()

func newSystemResolver() Resolver {
	primary := os.TempDir()
	extra := []string(nil)
	if runtime.GOOS != "windows" {
		extra = append(extra, "/tmp")
	}
	return New(primary, extra...)
}

// New constructs an immutable resolver. The first candidate is the primary
// root used by @temp paths; the remaining candidates are equivalent allowed
// temporary roots.
func New(primary string, extra ...string) Resolver {
	candidates := append([]string{primary}, extra...)
	resolver := Resolver{}
	rootKeys := map[string]struct{}{}
	lexicalKeys := map[string]struct{}{}
	for index, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		clean := filepath.Clean(pathutil.ExpandHome(raw))
		if !filepath.IsAbs(clean) {
			continue
		}
		canonical, err := pathutil.Canonicalize(clean)
		if err != nil {
			continue
		}
		if index == 0 {
			resolver.primary = canonical
		}
		if _, exists := rootKeys[canonical.Key]; !exists {
			rootKeys[canonical.Key] = struct{}{}
			resolver.roots = append(resolver.roots, canonical)
		}
		for _, presentation := range []string{clean, canonical.Host} {
			key := lexicalKey(presentation)
			if key == "" {
				continue
			}
			if _, exists := lexicalKeys[key]; exists {
				continue
			}
			lexicalKeys[key] = struct{}{}
			resolver.lexicalRoots = append(resolver.lexicalRoots, lexicalRoot{path: presentation, canonical: canonical})
		}
	}
	if resolver.primary.Key == "" && len(resolver.roots) > 0 {
		resolver.primary = resolver.roots[0]
	}
	return resolver
}

func System() Resolver {
	return systemResolver
}

func (r Resolver) Primary() (pathutil.Canonical, bool) {
	return r.primary, r.primary.Key != ""
}

func (r Resolver) Roots() []pathutil.Canonical {
	return append([]pathutil.Canonical(nil), r.roots...)
}

// Paths returns the frozen lexical and canonical root spellings. Session
// snapshots retain these spellings so aliases such as macOS /tmp can still be
// recognized as the declared root when a descendant symlink escapes it.
func (r Resolver) Paths() []string {
	out := make([]string, 0, len(r.lexicalRoots))
	for _, root := range r.lexicalRoots {
		out = append(out, root.path)
	}
	return out
}

func (r Resolver) MatchCanonical(candidate pathutil.Canonical) (pathutil.Canonical, bool) {
	for _, root := range r.roots {
		if pathutil.WithinRoot(candidate, root) {
			return root, true
		}
	}
	return pathutil.Canonical{}, false
}

// Classify resolves the final target and distinguishes a normal outside path
// from a path that lexically starts in a temporary root but escapes through a
// symlink or junction.
func (r Resolver) Classify(raw string) (Classification, pathutil.Canonical, pathutil.Canonical, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Outside, pathutil.Canonical{}, pathutil.Canonical{}, fmt.Errorf("temporary path is required")
	}
	clean := filepath.Clean(pathutil.ExpandHome(raw))
	if !filepath.IsAbs(clean) {
		return Outside, pathutil.Canonical{}, pathutil.Canonical{}, nil
	}
	claimedRoot, claimed := r.lexicallyClaimedRoot(clean)
	candidate, err := pathutil.Canonicalize(clean)
	if err != nil {
		return Outside, pathutil.Canonical{}, pathutil.Canonical{}, err
	}
	if root, ok := r.MatchCanonical(candidate); ok {
		return Inside, candidate, root, nil
	}
	if claimed {
		return Escape, candidate, claimedRoot, nil
	}
	return Outside, candidate, pathutil.Canonical{}, nil
}

func (r Resolver) ResolveAtPrimary(suffix string) (Classification, pathutil.Canonical, pathutil.Canonical, error) {
	primary, ok := r.Primary()
	if !ok {
		return Outside, pathutil.Canonical{}, pathutil.Canonical{}, fmt.Errorf("temporary root is unavailable")
	}
	suffix = strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(suffix)), "/")
	joined := filepath.Clean(filepath.Join(primary.Host, filepath.FromSlash(suffix)))
	rel, err := filepath.Rel(primary.Host, joined)
	if err != nil {
		return Outside, pathutil.Canonical{}, pathutil.Canonical{}, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		candidate, canonicalErr := pathutil.Canonicalize(joined)
		return Escape, candidate, primary, canonicalErr
	}
	return r.Classify(joined)
}

func (r Resolver) lexicallyClaimedRoot(target string) (pathutil.Canonical, bool) {
	for _, root := range r.lexicalRoots {
		if lexicallyWithin(target, root.path) {
			return root.canonical, true
		}
	}
	return pathutil.Canonical{}, false
}

func lexicallyWithin(target string, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func lexicalKey(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == "" {
		return ""
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		path = strings.ToLower(path)
	}
	return path
}
