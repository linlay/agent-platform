package connector

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstallEmbeddedDesktop uses only resources compiled into Platform. The
// temporary source is discarded; the versioned shared package is the sole
// persistent copy. The returned lease protects this source while the caller
// serves catalog/management requests, including when no Agent has mounted it.
func (s Sources) InstallEmbeddedDesktop() (Package, func(), error) {
	if err := s.ValidateRoots(); err != nil {
		return Package{}, nil, err
	}
	if err := s.ensureSharedLayout(); err != nil {
		return Package{}, nil, err
	}
	assembly, err := s.AssemblyLease()
	if err != nil {
		return Package{}, nil, err
	}
	defer assembly()
	stage, err := os.MkdirTemp("", "platform-desktop-")
	if err != nil {
		return Package{}, nil, err
	}
	defer os.RemoveAll(stage)
	dir := filepath.Join(stage, "builtin.desktop")
	if err := WriteBuiltin(dir, "desktop", "", ""); err != nil {
		return Package{}, nil, fmt.Errorf("extract embedded Desktop: %w", err)
	}
	pkg, err := LoadDirectory(dir, "builtin.desktop")
	if err != nil {
		return Package{}, nil, fmt.Errorf("validate embedded Desktop: %w", err)
	}
	pkg.Builtin = true
	pkg, err = s.InstallShared(pkg)
	if err != nil {
		return Package{}, nil, err
	}
	release, err := RetainShared(pkg.Dir)
	if err != nil {
		return Package{}, nil, err
	}
	return pkg, release, nil
}
