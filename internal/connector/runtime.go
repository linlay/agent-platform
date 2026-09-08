package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agent-platform/internal/pathutil"
)

func (s Sources) PersistentRoot() string {
	if s.StateRoot != "" {
		return s.StateRoot
	}
	if s.ExternalRoot == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.ExternalRoot), ".state", "connectors")
}

func (p Package) PersistentRoot() string {
	if p.StateRoot != "" {
		return p.StateRoot
	}
	return (Sources{ExternalRoot: filepath.Dir(p.Dir)}).PersistentRoot()
}

func RootsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, ea := pathutil.Canonicalize(a)
	cb, eb := pathutil.Canonicalize(b)
	return ea != nil || eb != nil || pathutil.WithinRoot(ca, cb) || pathutil.WithinRoot(cb, ca)
}

func (s Sources) ValidateRoots() error {
	roots := []string{s.ExternalRoot, s.BuiltinRoot, s.StateRoot}
	for i, root := range roots {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if filepath.Dir(abs) == abs {
			return fmt.Errorf("connector directory cannot be a filesystem root")
		}
		if info, err := os.Lstat(root); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("connector root must be a real directory: %s", root)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, other := range roots[:i] {
			if RootsOverlap(root, other) {
				return fmt.Errorf("connector directories must not overlap: %s and %s", root, other)
			}
		}
	}
	return nil
}

// Materialize copies only the selected packages into a fresh Agent candidate.
// The caller publishes the whole Agent after its skills and configuration pass
// validation. Credentials are never loaded or copied by this operation.
func (s Sources) Materialize(target string, ids []string) ([]Package, error) {
	if target == "" {
		return nil, fmt.Errorf("agent connector target is required")
	}
	if err := s.ValidateRoots(); err != nil {
		return nil, err
	}
	for _, root := range []string{s.ExternalRoot, s.BuiltinRoot, s.PersistentRoot()} {
		if RootsOverlap(target, root) {
			return nil, fmt.Errorf("agent connector target overlaps source or state")
		}
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return nil, fmt.Errorf("agent connector candidate already exists or is inaccessible: %s", target)
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return nil, err
	}
	result := make([]Package, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		pkg, err := s.Load(id)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{".state", ".credentials", "connector-state", "credentials.json", "oauth.json"} {
			if _, err := os.Lstat(filepath.Join(pkg.Dir, name)); !os.IsNotExist(err) {
				return nil, fmt.Errorf("connector %s contains reserved persistent state entry %s", id, name)
			}
		}
		before, err := packageDigest(pkg.Dir)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(target, id)
		if err := copyPackage(pkg.Dir, dest); err != nil {
			return nil, err
		}
		after, err := packageDigest(dest)
		if err != nil {
			return nil, err
		}
		if before != after {
			return nil, fmt.Errorf("connector %s changed during assembly", id)
		}
		if err := prepareRuntimeState(pkg, dest); err != nil {
			return nil, err
		}
		mounted, err := Load(target, id)
		if err != nil {
			return nil, err
		}
		mounted.Builtin = pkg.Builtin
		mounted.StateRoot = s.PersistentRoot()
		result = append(result, mounted)
	}
	return result, nil
}

// RuntimeFingerprint includes all executable and skill bytes, so changing a
// binary without changing mcp.json also invalidates the corresponding session.
func RuntimeFingerprint(root string) (string, error) {
	hash, err := packageDigest(root)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash), nil
}

func packageDigest(root string) ([32]byte, error) {
	if err := validateTree(root); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Mode()&(os.ModeType|0o777))
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := linkInPackage(root, path)
			if err != nil {
				return err
			}
			fmt.Fprint(h, target)
		} else if !entry.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		fmt.Fprint(h, "\x00")
		return nil
	})
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, err
}

func linkInPackage(root, path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Rel(canonical, target)
}

func copyPackage(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destination, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := linkInPackage(source, path)
			if err != nil {
				return err
			}
			link, err = filepath.Rel(filepath.Dir(destination), filepath.Join(target, link))
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if err := output.Chmod(info.Mode().Perm()); err != nil {
			output.Close()
			return err
		}
		_, err = io.Copy(output, input)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

// The managed Host CLI launcher needs only its state directory, never a token.
// This generated descriptor keeps custom source/runtime/state roots independent.
func prepareRuntimeState(pkg Package, dir string) error {
	if pkg.AuthMode != "cli" {
		return nil
	}
	stateDir, err := StateDir(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"stateDir": stateDir})
	if err != nil {
		return err
	}
	descriptor := filepath.Join(dir, ".platform-runtime.json")
	if err := os.Remove(descriptor); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(descriptor, data, 0600); err != nil {
		return err
	}
	// Upgrade the known Platform-generated launcher in legacy imported archives.
	// The authoritative source remains unchanged; arbitrary launchers are untouched.
	launcher := filepath.Join(dir, "bin", "launcher.cjs")
	data, err = os.ReadFile(launcher)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	old := []byte("const state = path.join(path.dirname(pkgDir), '.state', manifest.id);")
	current := []byte("const state = JSON.parse(fs.readFileSync(path.join(pkgDir, '.platform-runtime.json'), 'utf8')).stateDir;")
	if !bytes.Contains(data, old) {
		return nil
	}
	info, err := os.Stat(launcher)
	if err != nil {
		return err
	}
	return os.WriteFile(launcher, bytes.ReplaceAll(data, old, current), info.Mode().Perm())
}
