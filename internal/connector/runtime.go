package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agent-platform/internal/pathutil"
)

var assemblyMu sync.Mutex

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
	roots := []string{s.ExternalRoot, s.BuiltinRoot, s.RuntimeRoot, s.StateRoot}
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

// AssembleRuntime publishes one shared copy per connector. All source packages
// and candidates are validated before publication; a failed publication rolls
// back the changed packages. Unchanged packages retain their paths and inodes.
// Persistent credentials and CLI preparation are never copied into this tree.
func (s Sources) AssembleRuntime(validate func([]Package) error) ([]Package, error) {
	assemblyMu.Lock()
	defer assemblyMu.Unlock()
	packages, err := s.LoadAll()
	if err != nil {
		return nil, err
	}
	if validate != nil {
		if err := validate(packages); err != nil {
			return nil, err
		}
	}
	if s.RuntimeRoot == "" {
		return packages, nil
	}
	if err := s.ValidateRoots(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.RuntimeRoot, 0o700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(s.RuntimeRoot, ".assembly-")
	if err != nil {
		return nil, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	changes := []string{}
	present := map[string]bool{}
	for _, pkg := range packages {
		present[pkg.ID] = true
		target := filepath.Join(s.RuntimeRoot, pkg.ID)
		sourceHash, err := packageDigest(pkg.Dir)
		if err != nil {
			return nil, err
		}
		if targetHash, err := packageDigest(target); pkg.AuthMode != "cli" && err == nil && sourceHash == targetHash {
			continue
		}
		candidate := filepath.Join(stage, "new", pkg.ID)
		if err := copyPackage(pkg.Dir, candidate); err != nil {
			return nil, err
		}
		if _, err := Load(filepath.Join(stage, "new"), pkg.ID); err != nil {
			return nil, err
		}
		candidateHash, err := packageDigest(candidate)
		if err != nil {
			return nil, err
		}
		if sourceHash != candidateHash {
			return nil, fmt.Errorf("connector %s changed during assembly", pkg.ID)
		}
		if err := prepareRuntimeState(pkg, candidate); err != nil {
			return nil, err
		}
		candidateHash, err = packageDigest(candidate)
		if err != nil {
			return nil, err
		}
		if targetHash, err := packageDigest(target); err == nil && candidateHash == targetHash {
			continue
		}
		changes = append(changes, pkg.ID)
	}
	entries, err := os.ReadDir(s.RuntimeRoot)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") && !present[entry.Name()] {
			changes = append(changes, entry.Name())
		}
	}
	if err := os.MkdirAll(filepath.Join(stage, "old"), 0o700); err != nil {
		return nil, err
	}
	type change struct {
		id  string
		old bool
	}
	var applied []change
	rollback := func(cause error) ([]Package, error) {
		keepStage = true
		for i := len(applied) - 1; i >= 0; i-- {
			c := applied[i]
			if err := os.RemoveAll(filepath.Join(s.RuntimeRoot, c.id)); err != nil {
				return nil, fmt.Errorf("%w; rollback failed, backup retained at %s: %v", cause, stage, err)
			}
			if c.old {
				if err := os.Rename(filepath.Join(stage, "old", c.id), filepath.Join(s.RuntimeRoot, c.id)); err != nil {
					return nil, fmt.Errorf("%w; rollback failed, backup retained at %s: %v", cause, stage, err)
				}
			}
		}
		keepStage = false
		return nil, cause
	}
	for _, id := range changes {
		target := filepath.Join(s.RuntimeRoot, id)
		c := change{id: id}
		if _, err := os.Lstat(target); err == nil {
			if err := os.Rename(target, filepath.Join(stage, "old", id)); err != nil {
				return rollback(err)
			}
			c.old = true
		} else if !os.IsNotExist(err) {
			return rollback(err)
		}
		applied = append(applied, c)
		if present[id] {
			if err := os.Rename(filepath.Join(stage, "new", id), target); err != nil {
				return rollback(err)
			}
		}
	}
	result := make([]Package, 0, len(packages))
	for _, source := range packages {
		pkg, err := s.LoadRuntime(source.ID)
		if err != nil {
			return rollback(err)
		}
		result = append(result, pkg)
	}
	return result, nil
}

func (s Sources) LoadRuntime(id string) (Package, error) {
	if s.RuntimeRoot == "" {
		return s.Load(id)
	}
	// Check the authoritative source so an orphaned runtime package is never
	// sufficient to mount a removed external connector or a missing builtin.
	if _, err := s.Load(id); err != nil {
		return Package{}, err
	}
	pkg, err := Load(s.RuntimeRoot, id)
	pkg.Builtin = IsBuiltin(id)
	pkg.StateRoot = s.PersistentRoot()
	return pkg, err
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
