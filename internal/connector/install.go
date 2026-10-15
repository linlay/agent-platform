package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	Prefix           string    `json:"prefix,omitempty"`
	Executable       string    `json:"executable,omitempty"`
	ExecutableSHA256 string    `json:"executableSha256,omitempty"`
	ActivatedAt      time.Time `json:"activatedAt"`
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

// InstallPrefix keys an installation by package version and source fingerprint,
// so an update never overwrites the installation that is still active.
func InstallPrefix(root, id, version, fingerprint string) (string, error) {
	dir, err := InstallDir(root, id)
	if err != nil {
		return "", err
	}
	if !versionPattern.MatchString(version) || strings.ContainsAny(version, `/\`) {
		return "", fmt.Errorf("CLI installation requires a package version")
	}
	if len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid CLI installation fingerprint")
	}
	return filepath.Join(dir, version, fingerprint), nil
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

// PruneInstallations removes version directories other than keep. Removal is
// best effort: a program still running on Windows keeps its files locked.
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
			if !kept[prefix] {
				_ = os.RemoveAll(prefix)
			}
		}
		_ = os.Remove(filepath.Join(dir, version.Name()))
	}
}

// RemoveInstallations deletes every program of a deleted package. Credentials
// in .state are kept, so a re-import only needs a new preparation.
func RemoveInstallations(root, id string) {
	if dir, err := InstallDir(root, id); err == nil {
		_ = os.RemoveAll(dir)
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
