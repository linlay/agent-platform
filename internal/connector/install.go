package connector

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Programs installed by a CLI's init live in <runtime>/connector-installs:
// sources stay in connectors-center, programs here, credentials in .state.
// The directory persists across restarts, unlike the ru-* runtime copies.
const installsDirName = "connector-installs"

const activeInstallationFile = "active.json"

func (s Sources) InstallRoot() string {
	if s.ExternalRoot == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.ExternalRoot), installsDirName)
}

// Installation is the last successfully prepared CLI of one connector. A
// failed later preparation never replaces it, so the CLI stays usable.
type Installation struct {
	ConnectorID string `json:"connectorId"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	// Prefix is the private npm prefix init installed into. It is empty for a
	// package that ships bin/ or relies on a command installed elsewhere.
	Prefix           string `json:"prefix,omitempty"`
	Executable       string `json:"executable,omitempty"`
	ExecutableSHA256 string `json:"executableSha256,omitempty"`
	// A Windows npm .cmd launcher runs a separate script through Node; that
	// script is pinned as well.
	LauncherTarget       string    `json:"launcherTarget,omitempty"`
	LauncherTargetSHA256 string    `json:"launcherTargetSha256,omitempty"`
	ActivatedAt          time.Time `json:"activatedAt"`
}

// PinExecutable records the executable and, for a Windows npm launcher, the
// script it actually runs.
func (i *Installation) PinExecutable(entry, goos string) error {
	hash, err := CLIFileHash(entry)
	if err != nil {
		return err
	}
	i.Executable, i.ExecutableSHA256, i.LauncherTarget, i.LauncherTargetSHA256 = entry, hash, "", ""
	ext := strings.ToLower(filepath.Ext(entry))
	if goos != "windows" || (ext != ".cmd" && ext != ".bat") {
		return nil
	}
	target, err := WindowsNodeShimTarget(entry)
	if errors.Is(err, ErrUnsupportedWindowsNodeShim) {
		return nil
	}
	if err != nil {
		return err
	}
	if i.LauncherTargetSHA256, err = CLIFileHash(target); err != nil {
		return err
	}
	i.LauncherTarget = target
	return nil
}

// VerifyExecutable checks the pinned executable and launcher script content.
func (i Installation) VerifyExecutable() error {
	return i.verifyExecutable(runtime.GOOS)
}

func (i Installation) verifyExecutable(goos string) error {
	changed := fmt.Errorf("CLI executable changed or is missing; prepare again")
	if hash, err := CLIFileHash(i.Executable); err != nil || hash != i.ExecutableSHA256 {
		return changed
	}
	if i.LauncherTarget == "" {
		// A record without the script, such as one written before scripts
		// were pinned, cannot vouch for what a Windows npm launcher runs.
		ext := strings.ToLower(filepath.Ext(i.Executable))
		if goos == "windows" && (ext == ".cmd" || ext == ".bat") {
			if _, err := WindowsNodeShimTarget(i.Executable); !errors.Is(err, ErrUnsupportedWindowsNodeShim) {
				return changed
			}
		}
		return nil
	}
	target, err := WindowsNodeShimTarget(i.Executable)
	if err != nil || filepath.Clean(target) != filepath.Clean(i.LauncherTarget) {
		return changed
	}
	if hash, err := CLIFileHash(target); err != nil || hash != i.LauncherTargetSHA256 {
		return changed
	}
	return nil
}

// Bin is the directory npm links global commands into for this prefix.
func (i Installation) Bin() string { return InstallBin(i.Prefix) }

func InstallBin(prefix string) string {
	if prefix == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return prefix
	}
	return filepath.Join(prefix, "bin")
}

// InstallDir is one connector's directory below the installs root.
func InstallDir(root, id string) (string, error) {
	return StateDir(root, id)
}

// NewInstallPrefix creates a fresh directory for one preparation attempt,
// below the package version and source fingerprint. Every attempt, including
// a retry of the same package, installs beside the active installation and
// never into it. The directory is leased before it exists, so collection never
// removes an installation in progress; release after the attempt is recorded
// as active or removed.
func NewInstallPrefix(root, id, version, fingerprint string) (string, func(), error) {
	dir, err := InstallDir(root, id)
	if err != nil {
		return "", nil, err
	}
	if !versionPattern.MatchString(version) || strings.ContainsAny(version, `/\`) {
		return "", nil, fmt.Errorf("CLI installation requires a package version")
	}
	if len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		return "", nil, fmt.Errorf("invalid CLI installation fingerprint")
	}
	if err := os.MkdirAll(filepath.Join(dir, version), 0o755); err != nil {
		return "", nil, err
	}
	for range 8 {
		suffix := make([]byte, 6)
		if _, err := rand.Read(suffix); err != nil {
			return "", nil, err
		}
		prefix := filepath.Join(dir, version, fingerprint[:16]+"-"+hex.EncodeToString(suffix))
		release, err := RetainInstallation(prefix)
		if err != nil {
			return "", nil, err
		}
		if err := os.Mkdir(prefix, 0o755); err == nil {
			return prefix, release, nil
		} else if !os.IsExist(err) {
			release()
			return "", nil, err
		}
		release()
	}
	return "", nil, fmt.Errorf("cannot allocate a CLI installation directory")
}

func ReadInstallation(root, id string) (Installation, error) {
	dir, err := InstallDir(root, id)
	if err != nil {
		return Installation{}, err
	}
	var installation Installation
	if err := ReadJSON(filepath.Join(dir, activeInstallationFile), &installation); err != nil {
		return Installation{}, err
	}
	if installation.ConnectorID != id {
		return Installation{}, fmt.Errorf("invalid active CLI installation record")
	}
	if installation.Prefix != "" {
		rel, err := filepath.Rel(dir, installation.Prefix)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return Installation{}, fmt.Errorf("active CLI installation escapes its directory")
		}
	}
	return installation, nil
}

func WriteInstallation(root string, installation Installation) error {
	dir, err := InstallDir(root, installation.ConnectorID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(installation, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, activeInstallationFile), data)
}

// ActiveInstallBin returns the active private npm bin directory, if any.
func ActiveInstallBin(root, id string) string {
	installation, err := ReadInstallation(root, id)
	if err != nil {
		return ""
	}
	bin := installation.Bin()
	if info, err := os.Stat(bin); bin == "" || err != nil || !info.IsDir() {
		return ""
	}
	return bin
}

// installLease is the OS lock protecting one installation directory. Holders
// share it; collection needs it exclusively.
func installLease(prefix string) (*os.File, error) {
	prefix = filepath.Clean(prefix)
	attempt, version := filepath.Base(prefix), filepath.Base(filepath.Dir(prefix))
	idDir := filepath.Dir(filepath.Dir(prefix))
	id, installRoot := filepath.Base(idDir), filepath.Dir(idDir)
	if !ValidID(id) || filepath.Base(installRoot) != installsDirName || !versionPattern.MatchString(version) ||
		attempt == "." || strings.HasPrefix(attempt, ".") || strings.ContainsAny(attempt, `/\`) {
		return nil, fmt.Errorf("invalid CLI installation directory")
	}
	return openRuntimeLock(filepath.Dir(installRoot), "connectors", "install-leases", id, version+"_"+attempt+".lock")
}

// RetainInstallation keeps an installation that an Agent version or a Run
// still uses from being collected, including by another process.
func RetainInstallation(prefix string) (func(), error) {
	f, err := installLease(prefix)
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
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }, nil
}

// PruneInstallations removes installations other than keep that no Agent
// version or Run retains. A removal failure (a program still running on
// Windows) leaves the directory for a later collection.
func PruneInstallations(root, id string, keep ...string) {
	dir, err := InstallDir(root, id)
	if err != nil {
		return
	}
	kept := map[string]bool{}
	for _, prefix := range keep {
		if prefix != "" {
			kept[filepath.Clean(prefix)] = true
		}
	}
	versions, _ := os.ReadDir(dir)
	for _, version := range versions {
		if !version.IsDir() || strings.HasPrefix(version.Name(), ".") {
			continue
		}
		installs, _ := os.ReadDir(filepath.Join(dir, version.Name()))
		for _, install := range installs {
			prefix := filepath.Join(dir, version.Name(), install.Name())
			if !install.IsDir() || kept[prefix] {
				continue
			}
			lock, err := installLease(prefix)
			if err != nil {
				continue
			}
			if ok, err := tryOperationLock(lock); err == nil && ok {
				_ = os.RemoveAll(prefix)
			}
			lock.Close()
		}
		_ = os.Remove(filepath.Join(dir, version.Name()))
	}
}

// CollectInstallations keeps each connector's active installation and every
// retained one, and removes the rest.
func (s Sources) CollectInstallations() {
	root := s.InstallRoot()
	ids, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range ids {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		active, _ := ReadInstallation(root, entry.Name())
		PruneInstallations(root, entry.Name(), active.Prefix)
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
