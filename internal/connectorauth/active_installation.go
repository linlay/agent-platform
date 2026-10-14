package connectorauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// A preparation attempt and the active installation have separate durable
// records. A failed update may replace preparation.json, never this reference.
type activeInstallationRecord struct {
	ConnectorID        string            `json:"connectorId"`
	Dir                string            `json:"dir"`
	Digest             string            `json:"digest"`
	PackageVersion     string            `json:"packageVersion"`
	InstallationDigest string            `json:"installationDigest"`
	Preparation        preparationRecord `json:"preparation"`
}

func activeInstallationPath(sources connector.Sources, id string) (string, error) {
	dir, err := StateDir(sources.PersistentRoot(), id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "active-installation.json"), nil
}

func installationDigestValid(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func (m *Manager) activatePreparedCLI(ctx context.Context, pkg connector.Package, prepared Preparation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if prepared.Status != "ready" || prepared.Fingerprint != pkg.InstallationDigest {
		return fmt.Errorf("CLI source changed during preparation")
	}
	// Hold assembly across shared-package publication and active-file commit.
	// InstallShared's own shared lease is compatible; GC's exclusive lease
	// cannot collect a newly copied package before the reference is visible.
	assembly, err := m.sources.AssemblyLease()
	if err != nil {
		return err
	}
	defer assembly()
	mounted, err := m.sources.InstallShared(pkg)
	if err != nil {
		return err
	}
	settings, err := cliSettingsFor(mounted)
	if err != nil {
		return err
	}
	env, err := m.cliEnvironment(mounted)
	if err != nil {
		return err
	}
	entry, err := hostenv.LookPath(settings.Command, env)
	if err != nil {
		return err
	}
	hash, err := connector.CLIFileHash(entry)
	if err != nil || hash != prepared.ExecutableSHA256 {
		return fmt.Errorf("CLI entry changed during activation")
	}
	activePrepared := prepared
	activePrepared.Executable, activePrepared.ExecutableSHA256 = entry, hash
	record := activeInstallationRecord{
		ConnectorID: pkg.ID, Dir: mounted.Dir, Digest: filepath.Base(mounted.Dir),
		PackageVersion: mounted.Version, InstallationDigest: mounted.InstallationDigest,
		Preparation: preparationRecord{Preparation: activePrepared, Executable: entry, ExecutableSHA256: hash},
	}
	path, err := activeInstallationPath(m.sources, pkg.ID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return savePrivateJSON(path, record)
}

func readActiveCLIPackage(sources connector.Sources, id string) (connector.Package, Preparation, error) {
	path, err := activeInstallationPath(sources, id)
	if err != nil {
		return connector.Package{}, Preparation{}, err
	}
	var record activeInstallationRecord
	if err := connector.ReadJSON(path, &record); err != nil {
		return connector.Package{}, Preparation{}, err
	}
	prepared := record.Preparation.Preparation
	prepared.Executable = record.Preparation.Executable
	prepared.ExecutableSHA256 = record.Preparation.ExecutableSHA256
	if record.ConnectorID != id || !installationDigestValid(record.Digest) || !installationDigestValid(record.InstallationDigest) ||
		prepared.ConnectorID != id || prepared.Status != "ready" || prepared.Fingerprint != record.InstallationDigest ||
		prepared.Executable == "" || !installationDigestValid(prepared.ExecutableSHA256) {
		return connector.Package{}, Preparation{}, fmt.Errorf("invalid active CLI installation record")
	}
	expected, err := filepath.Abs(filepath.Join(sources.SharedRoot(), id, record.Digest))
	if err != nil {
		return connector.Package{}, Preparation{}, err
	}
	dir, err := filepath.Abs(record.Dir)
	if err != nil || dir != expected || sources.SharedRoot() == "" {
		return connector.Package{}, Preparation{}, fmt.Errorf("active CLI package is outside the shared root")
	}
	for _, parent := range []string{sources.SharedRoot(), filepath.Dir(dir), dir} {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return connector.Package{}, Preparation{}, fmt.Errorf("active CLI package directory is invalid")
		}
	}
	digest, err := connector.RuntimeFingerprint(dir)
	if err != nil || digest != record.Digest {
		return connector.Package{}, Preparation{}, fmt.Errorf("active CLI package integrity check failed")
	}
	pkg, err := connector.LoadDirectory(dir, id)
	if err != nil {
		return connector.Package{}, Preparation{}, err
	}
	if pkg.CLI == nil || pkg.Version != record.PackageVersion || pkg.InstallationDigest != record.InstallationDigest {
		return connector.Package{}, Preparation{}, fmt.Errorf("active CLI package identity changed")
	}
	pkg.StateRoot, pkg.InstallationRoot = sources.PersistentRoot(), sources.PersistentRoot()
	manager := &Manager{sources: sources}
	if err := manager.verifyPreparedEntry(pkg, prepared); err != nil {
		return connector.Package{}, Preparation{}, err
	}
	return pkg, prepared, nil
}

// ResolveActiveCLIPackage selects only a previously verified immutable package.
// Current packages activate after explicit successful preparation; an absent or
// failed update retains the last valid active package. There is no directory or
// command-name guessing when no such record exists.
func ResolveActiveCLIPackage(sources connector.Sources, pkg connector.Package) (connector.Package, error) {
	if pkg.CLI == nil || pkg.Builtin {
		return pkg, nil
	}
	active, _, err := readActiveCLIPackage(sources, pkg.ID)
	if os.IsNotExist(err) {
		return pkg, nil
	}
	if err != nil {
		return connector.Package{}, err
	}
	return active, nil
}

func activePreparationFor(sources connector.Sources, pkg connector.Package) (Preparation, bool, error) {
	active, prepared, err := readActiveCLIPackage(sources, pkg.ID)
	if os.IsNotExist(err) {
		return Preparation{}, false, nil
	}
	if err != nil {
		return Preparation{}, false, err
	}
	activeDir, _ := filepath.Abs(active.Dir)
	pkgDir, _ := filepath.Abs(pkg.Dir)
	return prepared, activeDir == pkgDir && active.InstallationDigest == pkg.InstallationDigest, nil
}
