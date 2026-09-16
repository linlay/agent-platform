package connectorauth

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/pathutil"
	"path/filepath"
	"strings"
)

// DeclaredEntries freezes only the command named by versionCheck. Extra files
// found in bin are never implicitly granted execution rights by a new session.
func (m *Manager) DeclaredEntries(ids []string) ([]connector.CLIEntry, []string) {
	var entries []connector.CLIEntry
	var bins []string
	for _, id := range ids {
		pkg, err := m.sources.Load(id)
		if err != nil || pkg.Builtin || pkg.CLI == nil {
			continue
		}
		setting, err := cliSettingsFor(pkg)
		if err != nil {
			continue
		}
		if err = m.requirePrepared(pkg); err != nil {
			continue
		}
		entry, err := m.cliPrivateEntry(pkg, setting.Command)
		if err != nil {
			continue
		}
		canonical, err := pathutil.Canonicalize(entry)
		if err != nil {
			continue
		}
		digest, err := connector.CLIFileHash(canonical.Host)
		if err != nil {
			continue
		}
		root, err := pkg.InstallDir()
		if err != nil {
			continue
		}
		if rel, e := filepath.Rel(root, canonical.Host); e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root = filepath.Dir(canonical.Host)
		}
		relative, err := filepath.Rel(root, canonical.Host)
		if err != nil {
			continue
		}
		entries = append(entries, connector.CLIEntry{Root: root, ConnectorID: id, Path: canonical.Host, PathKey: canonical.Key, RelativePath: filepath.ToSlash(relative), SHA256: digest})
		bins = append(bins, filepath.Dir(entry))
	}
	return entries, bins
}
