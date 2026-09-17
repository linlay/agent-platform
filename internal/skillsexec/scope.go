// Package skillsexec holds in-memory, run-owned execution grants for skill scripts.
package skillsexec

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"agent-platform/internal/pathutil"
	"agent-platform/internal/scriptstate"
)

// Root pairs a selected skill's host directory with its actual container mount.
type Root struct{ Host, Guest string }
type entry struct {
	source, canonical, scripts, root, hash string
	size                                   int64
}
type Scope struct {
	mu    sync.Mutex
	owner scriptstate.Owner
	roots []Root
	files map[string]entry
}

// New snapshots existing regular files under scripts. Unreadable or escaping
// entries receive no grant; they continue through the ordinary execution policy.
// No files are created and no proof is reconstructed from conversation history.
func New(owner scriptstate.Owner, roots []Root) *Scope {
	s := &Scope{owner: owner, files: map[string]entry{}}
	if owner.Agent == "" || owner.Run == "" {
		return s
	}
	seen := map[string]bool{}
	for _, r := range roots {
		root, err := strictPath(r.Host)
		if err != nil || seen[root] {
			continue
		}
		scripts, err := strictPath(filepath.Join(root, "scripts"))
		if err != nil || !within(scripts, root) || scripts == root {
			continue
		}
		info, err := os.Stat(scripts)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[root] = true
		s.roots = append(s.roots, Root{Host: root, Guest: r.Guest})
		_ = filepath.WalkDir(scripts, func(source string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			canonical, err := strictPath(source)
			if err != nil || !within(canonical, scripts) {
				return nil
			}
			hash, size, err := digest(canonical)
			if err != nil {
				return nil
			}
			s.files[canonical] = entry{source: source, canonical: canonical, scripts: scripts, root: root, hash: hash, size: size}
			return nil
		})
	}
	return s
}

// Roots returns a copy so callers cannot enlarge the frozen grant.
func (s *Scope) Roots() []Root {
	if s == nil {
		return nil
	}
	return append([]Root(nil), s.roots...)
}

// Matches permanently revokes an observed stale entry. Container callers must
// supply the digest read inside that container, never a host-only substitute.
func (s *Scope) Matches(owner scriptstate.Owner, target, guestHash string, container bool) bool {
	if s == nil || s.owner != owner {
		return false
	}
	canonical, err := strictPath(target)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A missing/repointed entry must not regain its old proof after restoration.
	lexical, _ := filepath.Abs(target)
	if candidate, pathErr := pathutil.Canonicalize(target); pathErr == nil && err != nil {
		lexical = candidate.Host
	}
	for key, existing := range s.files {
		if (lexical == existing.source || lexical == existing.canonical) && (err != nil || canonical != existing.canonical) {
			delete(s.files, key)
		}
	}
	if err != nil {
		return false
	}
	e, ok := s.files[canonical]
	if !ok {
		return false
	}
	root, rootErr := strictPath(e.root)
	scripts, scriptsErr := strictPath(filepath.Join(e.root, "scripts"))
	source, sourceErr := strictPath(e.source)
	hash, size, hashErr := digest(canonical)
	if rootErr != nil || root != e.root || scriptsErr != nil || scripts != e.scripts || sourceErr != nil || source != e.canonical || !within(canonical, scripts) || hashErr != nil || hash != e.hash || size != e.size || (container && (guestHash == "" || guestHash != e.hash)) {
		delete(s.files, canonical)
		return false
	}
	return true
}

// HostPath only maps selected skill mounts, with component-aware containment.
func (s *Scope) HostPath(guest string) (string, bool) {
	if s == nil || !path.IsAbs(guest) {
		return "", false
	}
	guest = path.Clean(guest)
	for _, r := range s.roots {
		if r.Guest == "" {
			continue
		}
		root := path.Clean(r.Guest)
		if guest == root {
			return r.Host, true
		}
		if strings.HasPrefix(guest, root+"/") {
			rel := strings.TrimPrefix(guest, root+"/")
			if strings.Contains(rel, "\\") || strings.Contains(rel, ":") {
				return "", false
			}
			return filepath.Join(r.Host, filepath.FromSlash(rel)), true
		}
	}
	return "", false
}

func strictPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fs.ErrInvalid
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func digest(p string) (string, int64, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fs.ErrInvalid
	}
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, info.Size()+1))
	if err != nil || n != info.Size() {
		return "", 0, fs.ErrInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
