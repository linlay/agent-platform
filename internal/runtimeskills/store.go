// Package runtimeskills stores process-lifetime immutable ordinary Skill trees.
package runtimeskills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Reference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func Root(agentRoot string) string { return filepath.Join(filepath.Dir(agentRoot), "ru-skills") }
func Path(root, digest string) (string, error) {
	b, err := hex.DecodeString(digest)
	if err != nil || len(b) != sha256.Size || digest != strings.ToLower(digest) {
		return "", fmt.Errorf("invalid skill digest")
	}
	return filepath.Join(root, digest), nil
}

// Digest ignores writable bits: installation removes them. Executability is
// normalized to a boolean so the same tree hashes identically before sealing.
func Digest(root string) (string, error) { return DigestExcept(root) }
func DigestExcept(root string, excluded ...string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported runtime file: %s", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, skip := range excluded {
			if rel == skip {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() {
			fmt.Fprintf(h, "d:%q\n", filepath.ToSlash(rel))
			return nil
		}
		fmt.Fprintf(h, "f:%q:%t:%d\n", filepath.ToSlash(rel), info.Mode()&0111 != 0, info.Size())
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Seal(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0400)
		if d.IsDir() || info.Mode()&0111 != 0 {
			mode = 0500
		}
		return os.Chmod(p, mode)
	})
}

// Remove only receives generated paths owned by the runtime. Never follow links.
func Remove(root string) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return os.Chmod(p, 0700)
		}
		return os.Chmod(p, 0600)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func WriteReferences(agentDir string, refs []Reference) error {
	// Pre-create empty mountpoints before mounting the parent /skills read-only.
	// The bodies remain exclusively in ru-skills; hidden metadata cannot collide
	// with ordinary Skill IDs.
	for _, ref := range refs {
		if !ValidID(ref.ID) || strings.HasPrefix(ref.ID, ".") {
			return fmt.Errorf("invalid skill reference %q", ref.ID)
		}
		if err := os.MkdirAll(filepath.Join(agentDir, "skills", filepath.FromSlash(ref.ID)), 0700); err != nil {
			return err
		}
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentDir, "skills", ".refs.json"), data, 0600)
}
func References(agentDir string) ([]Reference, error) {
	data, err := os.ReadFile(filepath.Join(agentDir, "skills", ".refs.json"))
	if err != nil {
		return nil, err
	}
	var refs []Reference
	if err = json.Unmarshal(data, &refs); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !ValidID(ref.ID) || seen[strings.ToLower(ref.ID)] {
			return nil, fmt.Errorf("invalid skill reference %q", ref.ID)
		}
		if _, err := Path("", ref.Digest); err != nil {
			return nil, err
		}
		seen[strings.ToLower(ref.ID)] = true
	}
	return refs, nil
}
func ValidID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\\:\x00") {
			return false
		}
	}
	return true
}
func Dirs(agentDir string) (map[string]string, error) {
	refs, err := References(agentDir)
	if err != nil {
		return nil, err
	}
	root := Root(filepath.Dir(filepath.Dir(agentDir)))
	result := map[string]string{}
	for _, ref := range refs {
		p, err := Path(root, ref.Digest)
		if err != nil {
			return nil, err
		}
		result[ref.ID] = p
	}
	return result, nil
}
func Resolve(agentDir, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid skill id")
	}
	dirs, err := Dirs(agentDir)
	if err != nil {
		return "", err
	}
	for key, p := range dirs {
		if strings.EqualFold(key, id) {
			return p, nil
		}
	}
	return "", fmt.Errorf("skill %q is not mounted", id)
}
func Verify(agentDir string) error {
	refs, err := References(agentDir)
	if err != nil {
		return err
	}
	dirs, err := Dirs(agentDir)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		got, err := Digest(dirs[ref.ID])
		if err != nil {
			return err
		}
		if got != ref.Digest {
			return fmt.Errorf("skill integrity failure: %s", ref.ID)
		}
	}
	return nil
}
