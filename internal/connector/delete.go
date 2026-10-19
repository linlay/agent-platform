package connector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
)

var ErrPackageNotFound = errors.New("connector is not installed")
var ErrDeleteReload = errors.New("connector deletion could not reload the catalog")
var ErrDeleteCleanup = errors.New("connector deletion cleanup is incomplete; retry deletion")

type deletionStage struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Only same-filesystem staging basenames are persisted, never caller-provided
// paths. A retry resolves them against the current, validated managed roots.
type deletionRecord struct {
	ID         string          `json:"id"`
	Generation string          `json:"generation"`
	RootsHash  string          `json:"rootsHash"`
	Stages     []deletionStage `json:"stages"`
}

type deletionMove struct {
	stage  deletionStage
	target string
	path   string
	moved  bool
}

// Per-call filesystem operations allow deterministic failure coverage without
// changing global behavior or introducing races between deletion tests.
type deletionFileOps struct {
	rename    func(string, string) error
	removeAll func(string) error
}

// DeletePackage removes an external package, all of its private state and its
// managed CLI installations. check must inspect configured and active references
// while its caller holds the Agent mutation lock. The operation lock spans the
// whole transaction; the credential lock is released before catalog reload.
func DeletePackage(ctx context.Context, sources Sources, id string, check func(string) error, reload func() error) error {
	return deletePackage(ctx, sources, id, check, reload, deletionFileOps{rename: os.Rename, removeAll: os.RemoveAll})
}

func deletePackage(ctx context.Context, sources Sources, id string, check func(string) error, reload func() error, fs deletionFileOps) error {
	if IsBuiltin(id) {
		return ErrBuiltinReadOnly
	}
	if !ValidID(id) || strings.TrimSpace(sources.ExternalRoot) == "" {
		return fmt.Errorf("invalid connector deletion target")
	}
	if err := validateDeletionRoots(sources); err != nil {
		return err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	release, err := AcquireOperation(sources.ExternalRoot, id)
	if err != nil {
		return err
	}
	defer release()
	target := filepath.Join(sources.ExternalRoot, id)
	pending, err := readDeletionRecord(sources, id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) && pending == nil {
		return ErrPackageNotFound
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if check != nil {
		if err := check(id); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stateRoot := sources.PersistentRoot()
	unlock, err := AcquireStateLock(ctx, stateRoot, id)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	previous, err := ReadStateControl(stateRoot, id)
	if err != nil {
		return err
	}
	pending, err = readDeletionRecord(sources, id)
	if err != nil {
		return err
	}
	if pending != nil {
		if !previous.Deleted || previous.Generation != pending.Generation {
			return fmt.Errorf("%w: deletion control does not match its cleanup record", ErrDeleteCleanup)
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return fmt.Errorf("%w: package source reappeared before cleanup", ErrDeleteCleanup)
		}
		unlock()
		locked = false
		return cleanupDeletion(sources, *pending, fs)
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return ErrPackageNotFound
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || unsafeDirectoryAlias(target, info) {
		return fmt.Errorf("connector target is not a real directory")
	}
	if previous.Deleting || previous.Deleted {
		return fmt.Errorf("%w: connector lifecycle requires recovery", ErrBusy)
	}
	stateDir, err := StateDir(stateRoot, id)
	if err != nil {
		return err
	}
	installDir, err := InstallDir(sources.InstallRoot(), id)
	if err != nil {
		return err
	}
	for _, dir := range []string{stateDir, installDir} {
		if err := realDirectory(dir, false); err != nil {
			return err
		}
	}
	deleting := previous
	deleting.Deleting = true
	if err := WriteStateControl(stateRoot, id, deleting); err != nil {
		return errors.Join(err, WriteStateControl(stateRoot, id, previous))
	}
	moves := []deletionMove{}
	rollback := func(cause error, recoverCatalog bool) error {
		// Reload may read credentials, so it must never execute under StateLock.
		if !locked {
			unlock, err = AcquireStateLock(context.WithoutCancel(ctx), stateRoot, id)
			if err != nil {
				return errors.Join(cause, fmt.Errorf("cannot lock deletion rollback: %w", err))
			}
			locked = true
		}
		var restoreErrors []error
		for i := len(moves) - 1; i >= 0; i-- {
			move := &moves[i]
			if !move.moved {
				continue
			}
			if _, err := os.Lstat(move.target); !os.IsNotExist(err) {
				restoreErrors = append(restoreErrors, fmt.Errorf("cannot restore %s; backup retained at %s", move.target, move.path))
				continue
			}
			if err := fs.rename(filepath.Join(move.path, id), move.target); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("cannot restore %s; backup retained at %s: %w", move.target, move.path, err))
			} else {
				move.moved = false
			}
		}
		if len(restoreErrors) != 0 {
			return errors.Join(append([]error{cause}, restoreErrors...)...)
		}
		if err := removeDeletionRecord(sources, id); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
		if err := WriteStateControl(stateRoot, id, previous); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("cannot restore connector lifecycle: %w", err))
		}
		unlock()
		locked = false
		for _, move := range moves {
			if err := fs.removeAll(move.path); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("cannot remove empty rollback stage %s: %w", move.path, err))
			}
		}
		if recoverCatalog && reload != nil {
			if err := reload(); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("package restored but catalog recovery failed: %w", err))
			}
		}
		return errors.Join(append([]error{cause}, restoreErrors...)...)
	}
	for i, dir := range []string{target, stateDir, installDir} {
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return rollback(err, false)
		}
		kind := []string{"source", "state", "install"}[i]
		base := deletionStageBase(sources, kind)
		stage, err := os.MkdirTemp(base, deletionStagePrefix(id))
		if err != nil {
			return rollback(err, false)
		}
		moves = append(moves, deletionMove{stage: deletionStage{Kind: kind, Name: filepath.Base(stage)}, target: dir, path: stage})
		if err := fs.rename(dir, filepath.Join(stage, id)); err != nil {
			return rollback(err, false)
		}
		moves[len(moves)-1].moved = true
	}
	unlock()
	locked = false
	if reload != nil {
		if err := reload(); err != nil {
			return rollback(fmt.Errorf("%w: %w", ErrDeleteReload, err), true)
		}
	}
	unlock, err = AcquireStateLock(context.WithoutCancel(ctx), stateRoot, id)
	if err != nil {
		return rollback(err, true)
	}
	locked = true
	record := deletionRecord{ID: id, Generation: rand.Text(), RootsHash: deletionRootsHash(sources)}
	for _, move := range moves {
		record.Stages = append(record.Stages, move.stage)
	}
	if err := writeDeletionRecord(sources, record); err != nil {
		return rollback(err, true)
	}
	if err := WriteStateControl(stateRoot, id, StateControl{Generation: record.Generation, Deleted: true}); err != nil {
		return rollback(err, true)
	}
	unlock()
	locked = false
	return cleanupDeletion(sources, record, fs)
}

func validateDeletionRoots(s Sources) error {
	if err := s.ValidateRoots(); err != nil {
		return err
	}
	roots := []string{s.ExternalRoot, s.PersistentRoot(), s.InstallRoot()}
	for i, root := range roots {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("invalid connector deletion root")
		}
		abs, err := filepath.Abs(root)
		if err != nil || filepath.Dir(abs) == abs {
			return fmt.Errorf("invalid connector deletion root: %s", root)
		}
		if err := realDirectory(root, false); err != nil {
			return err
		}
		for _, other := range roots[:i] {
			if RootsOverlap(root, other) {
				return fmt.Errorf("connector deletion roots must not overlap")
			}
		}
	}
	return realDirectory(filepath.Dir(filepath.Clean(s.ExternalRoot)), false)
}

func deletionStagePrefix(id string) string {
	return fmt.Sprintf(".connector-delete-%x-", sha256.Sum256([]byte(id)))
}

func deletionRootsHash(s Sources) string {
	roots := make([]string, 0, 3)
	for _, root := range []string{s.ExternalRoot, s.PersistentRoot(), s.InstallRoot()} {
		abs, _ := filepath.Abs(root)
		roots = append(roots, pathutil.CanonicalKey(abs))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(roots, "\x00"))))
}

func deletionStageBase(s Sources, kind string) string {
	switch kind {
	case "source":
		return filepath.Dir(filepath.Clean(s.ExternalRoot))
	case "state":
		return s.PersistentRoot()
	case "install":
		return s.InstallRoot()
	}
	return ""
}

func deletionRecordPath(s Sources, id string, create bool) (string, error) {
	return stateControlPath(s.PersistentRoot(), id, ".delete.json", create)
}

func readDeletionRecord(s Sources, id string) (*deletionRecord, error) {
	path, err := deletionRecordPath(s, id, false)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record deletionRecord
	if err := DecodeJSON(data, &record); err != nil {
		return nil, fmt.Errorf("invalid connector deletion cleanup record: %w", err)
	}
	if err := validateDeletionRecord(s, id, record); err != nil {
		return nil, err
	}
	return &record, nil
}

func validateDeletionRecord(s Sources, id string, record deletionRecord) error {
	if record.ID != id || record.Generation == "" || len(record.Stages) == 0 || len(record.Stages) > 3 {
		return fmt.Errorf("invalid connector deletion cleanup record")
	}
	if record.RootsHash != deletionRootsHash(s) {
		return fmt.Errorf("connector deletion roots changed; retry cleanup with the original deployment paths")
	}
	seen := map[string]bool{}
	for _, stage := range record.Stages {
		base := deletionStageBase(s, stage.Kind)
		suffix := strings.TrimPrefix(stage.Name, deletionStagePrefix(id))
		if base == "" || seen[stage.Kind] || suffix == stage.Name || suffix == "" || strings.Trim(suffix, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
			return fmt.Errorf("invalid connector deletion staging path")
		}
		seen[stage.Kind] = true
		if err := realDirectory(base, false); err != nil {
			return err
		}
		path := filepath.Join(base, stage.Name)
		if err := realDirectory(path, false); err != nil {
			return err
		}
		if err := realDirectory(filepath.Join(path, id), false); err != nil {
			return err
		}
	}
	if !seen["source"] {
		return fmt.Errorf("invalid connector deletion cleanup record")
	}
	return nil
}

func writeDeletionRecord(s Sources, record deletionRecord) error {
	if err := validateDeletionRecord(s, record.ID, record); err != nil {
		return err
	}
	path, err := deletionRecordPath(s, record.ID, true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeControlFile(path, data)
}

func removeDeletionRecord(s Sources, id string) error {
	path, err := deletionRecordPath(s, id, false)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cleanupDeletion(s Sources, record deletionRecord, fs deletionFileOps) error {
	if err := validateDeletionRecord(s, record.ID, record); err != nil {
		return fmt.Errorf("%w: %w", ErrDeleteCleanup, err)
	}
	var failures []error
	for _, stage := range record.Stages {
		path := filepath.Join(deletionStageBase(s, stage.Kind), stage.Name)
		if err := fs.removeAll(path); err != nil {
			failures = append(failures, fmt.Errorf("cannot clean connector %s data at %s: %w", stage.Kind, path, err))
		}
	}
	if len(failures) == 0 {
		if err := removeDeletionRecord(s, record.ID); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(append([]error{ErrDeleteCleanup}, failures...)...)
	}
	return nil
}
