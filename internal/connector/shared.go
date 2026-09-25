package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MountReference contains no credentials. Package bytes never live in ru-agents.
type MountReference struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`
	Digest string `json:"digest"`
}

func (s Sources) SharedRoot() string {
	if s.ExternalRoot == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.ExternalRoot), "ru-connectors")
}

// InstallShared atomically publishes a content-addressed package. Existing versions
// are verified, never overwritten. The operation lock coordinates multiple processes.
func (s Sources) InstallShared(pkg Package) (Package, error) {
	root := s.SharedRoot()
	if s.ExternalRoot == "" {
		return Package{}, fmt.Errorf("connector runtime root is required")
	}
	for _, other := range []string{s.ExternalRoot, s.BuiltinRoot, s.PersistentRoot()} {
		if RootsOverlap(root, other) {
			return Package{}, fmt.Errorf("shared connector root overlaps source or state")
		}
	}
	if info, err := os.Lstat(root); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return Package{}, fmt.Errorf("invalid shared connector root")
	}
	if err := s.ensureSharedLayout(); err != nil {
		return Package{}, err
	}
	assembly, err := s.AssemblyLease()
	if err != nil {
		return Package{}, err
	}
	defer assembly()
	release, err := acquireSharedOperation(root, pkg.ID)
	if err != nil {
		return Package{}, err
	}
	defer release()
	if err := os.WriteFile(filepath.Join(root, ".shared-v1"), []byte("1\n"), 0600); err != nil {
		return Package{}, err
	}
	parent := filepath.Join(root, pkg.ID)
	if _, err := os.Stat(filepath.Join(parent, "connector.json")); err == nil {
		backup, err := os.MkdirTemp(filepath.Dir(root), ".connector-layout-backup-")
		if err != nil {
			return Package{}, err
		}
		if err := os.Rename(parent, filepath.Join(backup, pkg.ID)); err != nil {
			return Package{}, err
		}
	}
	if info, err := os.Lstat(parent); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return Package{}, fmt.Errorf("invalid shared connector package root")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Package{}, err
	}
	before, err := RuntimeFingerprint(pkg.Dir)
	if err != nil {
		return Package{}, err
	}
	indexKey := before
	if pkg.ManagedCLI() {
		indexKey = fmt.Sprintf("%x", sha256.Sum256([]byte(before+"\x00"+s.PersistentRoot())))
	}
	indexPath := filepath.Join(parent, ".source-"+indexKey+".json")
	var cached MountReference
	if err := ReadJSON(indexPath, &cached); err == nil && cached.ID == pkg.ID && validDigest(cached.Digest) {
		dest := filepath.Join(parent, cached.Digest)
		if actual, err := RuntimeFingerprint(dest); err == nil {
			if actual != cached.Digest {
				return Package{}, fmt.Errorf("shared connector integrity validation failed: %s", pkg.ID)
			}
			mounted, err := LoadDirectory(dest, pkg.ID)
			mounted.Builtin, mounted.StateRoot = pkg.Builtin, s.PersistentRoot()
			return mounted, err
		} else if !os.IsNotExist(err) {
			return Package{}, err
		}
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return Package{}, err
	}
	defer os.RemoveAll(stage)
	if err := copyPackage(pkg.Dir, stage); err != nil {
		return Package{}, err
	}
	after, err := RuntimeFingerprint(stage)
	if err != nil {
		return Package{}, err
	}
	if before != after {
		return Package{}, fmt.Errorf("connector %s changed during installation", pkg.ID)
	}
	if err := prepareRuntimeState(pkg, stage); err != nil {
		return Package{}, err
	}
	digest, err := RuntimeFingerprint(stage)
	if err != nil {
		return Package{}, err
	}
	dest := filepath.Join(parent, digest)
	if _, err := os.Lstat(dest); err == nil {
		actual, err := RuntimeFingerprint(dest)
		if err != nil || actual != digest {
			return Package{}, fmt.Errorf("shared connector %s failed integrity validation", pkg.ID)
		}
	} else if !os.IsNotExist(err) {
		return Package{}, err
	} else if err := os.Rename(stage, dest); err != nil {
		return Package{}, err
	}
	data, _ := json.Marshal(MountReference{ID: pkg.ID, Digest: digest})
	if err := os.WriteFile(indexPath, data, 0600); err != nil {
		return Package{}, err
	}
	mounted, err := LoadDirectory(dest, pkg.ID)
	mounted.Builtin, mounted.StateRoot = pkg.Builtin, s.PersistentRoot()
	return mounted, err
}

func ReadMount(root, id string) (MountReference, error) {
	var ref MountReference
	if !ValidID(id) {
		return ref, fmt.Errorf("invalid connector id")
	}
	data, err := os.ReadFile(filepath.Join(root, id+".json"))
	if err != nil {
		return ref, err
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return ref, err
	}
	if ref.ID != id || !validDigest(ref.Digest) || filepath.Base(ref.Dir) != ref.Digest || filepath.Base(filepath.Dir(ref.Dir)) != id {
		return ref, fmt.Errorf("invalid connector mount reference")
	}
	actual, err := RuntimeFingerprint(ref.Dir)
	if err != nil || actual != ref.Digest {
		return ref, fmt.Errorf("connector mount integrity validation failed: %s", id)
	}
	return ref, nil
}

// RetainShared protects one package from collection, including across processes.
func RetainShared(dir string) (func(), error) {
	if !validDigest(filepath.Base(dir)) {
		return func() {}, nil
	} // Legacy/test mounts.
	f, err := os.OpenFile(filepath.Join(filepath.Dir(dir), ".lease-"+filepath.Base(dir)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ok, err := trySharedLock(f)
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = ErrBusy
		}
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		f.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }, nil
}
func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32
}

// AssemblyLease prevents GC between candidate installation and publication.
func (s Sources) AssemblyLease() (func(), error) {
	if err := s.ensureSharedLayout(); err != nil {
		return nil, err
	}
	root := s.SharedRoot()
	if info, err := os.Lstat(root); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("invalid shared connector root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".assembly.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ok, err := trySharedLock(f)
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = ErrBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// CollectShared removes only validated, unleased versions. Unknown files and
// legacy layouts are preserved. Crashes release OS leases without stale counters.
func (s Sources) CollectShared() error {
	root := s.SharedRoot()
	f, err := os.OpenFile(filepath.Join(root, ".assembly.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	ok, err := tryOperationLock(f)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	pins, err := s.PinnedRuntimes()
	if err != nil {
		return err
	}
	pinned := map[string]bool{}
	for _, mount := range pins {
		pinned[mount.Dir] = true
	}
	ids, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !id.IsDir() || !ValidID(id.Name()) {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, id.Name()))
		if err != nil {
			return err
		}
		for _, version := range versions {
			if !version.IsDir() || !validDigest(version.Name()) {
				continue
			}
			dir := filepath.Join(root, id.Name(), version.Name())
			if pinned[dir] {
				continue
			}
			lock, err := os.OpenFile(filepath.Join(filepath.Dir(dir), ".lease-"+version.Name()), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				return err
			}
			ok, err := tryOperationLock(lock)
			if err == nil && ok {
				actual, e := RuntimeFingerprint(dir)
				if e == nil && actual == version.Name() {
					err = os.RemoveAll(dir)
				}
			}
			lock.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// A legacy flat ru-connectors tree is backed up before the first shared install.
func (s Sources) ensureSharedLayout() error {
	root := s.SharedRoot()
	if s.ExternalRoot == "" {
		return fmt.Errorf("connector runtime root is required")
	}
	release, err := acquireSharedLayout(filepath.Dir(root))
	if err != nil {
		return err
	}
	defer release()
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid shared connector root")
		}
		if _, err := os.Stat(filepath.Join(root, ".shared-v1")); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			backup, err := os.MkdirTemp(filepath.Dir(root), ".connector-layout-backup-")
			if err != nil {
				return err
			}
			if err := os.Rename(root, filepath.Join(backup, "ru-connectors")); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ".shared-v1"), []byte("1\n"), 0600)
}

func acquireSharedOperation(root, id string) (func(), error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := AcquireOperation(root, id)
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The layout lock lives outside ru-connectors because the protected operation
// can rename that entire tree. Upgrade requires stopping old Platform processes;
// there is intentionally no fallback to the former runtime-root lock path.
func acquireSharedLayout(runtimeRoot string) (func(), error) {
	lockDir := filepath.Join(runtimeRoot, ".lock")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(lockDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("runtime lock directory must be a real directory: %s", lockDir)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := acquireOperationFile(filepath.Join(lockDir, "shared-connector-layout.lock"))
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
