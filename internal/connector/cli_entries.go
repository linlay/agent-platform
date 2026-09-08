package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
)

// CLIEntry is a frozen entry in an Agent's mounted runtime package. It grants
// execution of the entry, not of an arbitrary program with the same name.
type CLIEntry struct {
	ConnectorID  string
	Path         string
	PathKey      string
	RelativePath string
	SHA256       string
}

func SnapshotCLIEntries(id, dir string) ([]CLIEntry, error) {
	bin := filepath.Join(dir, "bin")
	items, err := os.ReadDir(bin)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	root, err := pathutil.Canonicalize(dir)
	if err != nil {
		return nil, err
	}
	var entries []CLIEntry
	for _, item := range items {
		p := filepath.Join(bin, item.Name())
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(item.Name()))
		script := ext == ".sh" || ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".py" || ext == ".ps1" || ext == ".cmd" || ext == ".bat" || ext == ".exe" || ext == ".com"
		if info.Mode()&0o111 == 0 && !script {
			continue
		}
		canonical, err := pathutil.Canonicalize(p)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root.Host, canonical.Host)
		if err != nil || !pathutil.WithinRoot(canonical, root) {
			return nil, fmt.Errorf("connector CLI escapes runtime package: %s", p)
		}
		hash, err := CLIFileHash(canonical.Host)
		if err != nil {
			return nil, err
		}
		entries = append(entries, CLIEntry{ConnectorID: id, Path: canonical.Host, PathKey: canonical.Key, RelativePath: filepath.ToSlash(rel), SHA256: hash})
	}
	return entries, nil
}

func CLIFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("connector CLI is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
