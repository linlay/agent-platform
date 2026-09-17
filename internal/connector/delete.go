package connector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrPackageNotFound = errors.New("connector is not installed")
var ErrDeleteReload = errors.New("connector deletion could not reload the catalog")

// DeletePackage shares the import/edit mutation boundary. Only the external
// package is removed; credentials and managed CLI state have a separate lifecycle.
// check must inspect configured and active references while its caller holds the
// Agent mutation lock, so an Agent cannot acquire a new reference during deletion.
func DeletePackage(ctx context.Context, sources Sources, id string, check func(string) error, reload func() error) error {
	if IsBuiltin(id) {
		return ErrBuiltinReadOnly
	}
	if !ValidID(id) || strings.TrimSpace(sources.ExternalRoot) == "" {
		return fmt.Errorf("invalid connector deletion target")
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	release, err := AcquireOperation(sources.ExternalRoot, id)
	if err != nil {
		return err
	}
	defer release()
	target := filepath.Join(sources.ExternalRoot, id)
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return ErrPackageNotFound
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("connector target is not a real directory")
	}
	if check != nil {
		if err := check(id); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(sources.ExternalRoot, ".connector-delete-")
	if err != nil {
		return err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	backup := filepath.Join(stage, id)
	if err := os.Rename(target, backup); err != nil {
		return err
	}
	if reload != nil {
		if err := reload(); err != nil {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				keepStage = true
				return fmt.Errorf("%w: %v; backup retained at %s: %v", ErrDeleteReload, err, backup, restoreErr)
			}
			if restoreErr := reload(); restoreErr != nil {
				return fmt.Errorf("%w: %v; package restored but catalog recovery failed: %v", ErrDeleteReload, err, restoreErr)
			}
			return fmt.Errorf("%w: %v; original package restored", ErrDeleteReload, err)
		}
	}
	return nil
}
