package adminsource

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
)

type SkillFileEditor interface {
	ReadEditableSkillFile(key, path string) (catalog.EditableSkillFileContent, error)
	WriteEditableSkillFile(key, path, content, encoding, baseSHA256 string) (catalog.EditableSkillFile, error)
	DeleteEditableSkillFile(key, path string, recursive bool, baseSHA256 string) error
}

type SkillFileReloadError struct {
	Cause       error
	RollbackErr error
	ReloadErr   error
}

func (e *SkillFileReloadError) Error() string {
	message := "reload skill catalog: " + e.Cause.Error()
	if e.RollbackErr != nil {
		return message + "; restore skill source failed: " + e.RollbackErr.Error()
	}
	message += "; previous skill source restored"
	if e.ReloadErr != nil {
		message += "; reload restored catalog: " + e.ReloadErr.Error()
	}
	return message
}

func (e *SkillFileReloadError) Unwrap() error { return e.Cause }

// WriteSkillFile serializes text saves through publication. Failed reloads
// restore the previous bytes (and therefore the editor's base hash), but only
// while the file still matches this write, so external changes are preserved.
func (s *Service) WriteSkillFile(ctx context.Context, editor SkillFileEditor, key, path, content, encoding, baseSHA256 string, reload func(context.Context) error) (catalog.EditableSkillFile, error) {
	unlock := s.LockSourceMutation()
	defer unlock()
	before, err := editor.ReadEditableSkillFile(key, path)
	existed := err == nil
	if err != nil && !errors.Is(err, catalog.ErrSkillNotFound) {
		return catalog.EditableSkillFile{}, err
	}
	if existed && strings.TrimSpace(baseSHA256) == "" {
		// Even an unconditional client save must not race with a writer
		// between taking the rollback snapshot and replacing the file.
		baseSHA256 = before.SHA256
	}
	written, err := editor.WriteEditableSkillFile(key, path, content, encoding, baseSHA256)
	if err != nil {
		return catalog.EditableSkillFile{}, err
	}
	// A disconnected client cannot cancel publication/recovery after persistence.
	ctx = context.WithoutCancel(ctx)
	if err := reload(ctx); err != nil {
		failure := &SkillFileReloadError{Cause: err}
		// Compare with our exact bytes, not metadata another writer could change.
		writtenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if existed {
			_, failure.RollbackErr = editor.WriteEditableSkillFile(key, path, before.Content, before.Encoding, writtenHash)
		} else {
			failure.RollbackErr = editor.DeleteEditableSkillFile(key, path, false, writtenHash)
		}
		if failure.RollbackErr == nil {
			failure.ReloadErr = reload(ctx)
		}
		return catalog.EditableSkillFile{}, failure
	}
	return written, nil
}
