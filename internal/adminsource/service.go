// Package adminsource owns serialization boundaries for catalog/source
// mutations. File validation and atomic replacement are migrated behind this
// service without changing their HTTP contracts.
package adminsource

import (
	"context"
	"fmt"
	"io"
	"sync"

	"agent-platform/internal/catalog"
)

type Service struct {
	sourceMutation sync.Mutex
	agentMutation  sync.Mutex
}

func NewService() *Service { return &Service{} }

func (s *Service) LockSourceMutation() func() {
	if s == nil {
		return func() {}
	}
	s.sourceMutation.Lock()
	return s.sourceMutation.Unlock
}

func (s *Service) LockAgentMutation() func() {
	if s == nil {
		return func() {}
	}
	s.agentMutation.Lock()
	return s.agentMutation.Unlock
}

type AgentArchiveEditor interface {
	BeginImportEditableAgentArchive(source io.ReaderAt, size int64, overwrite bool) (*catalog.EditableAgentArchiveMutation, error)
	RollbackEditableAgentArchiveMutation(mutation *catalog.EditableAgentArchiveMutation) error
	CommitEditableAgentArchiveMutation(mutation *catalog.EditableAgentArchiveMutation) error
}

type ArchiveBeginError struct{ Cause error }

func (e *ArchiveBeginError) Error() string {
	if e == nil || e.Cause == nil {
		return "begin agent archive import"
	}
	return e.Cause.Error()
}

func (e *ArchiveBeginError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ArchiveRollbackError struct {
	AgentKey    string
	Cause       error
	RollbackErr error
	ReloadErr   error
}

func (e *ArchiveRollbackError) Error() string {
	if e == nil {
		return ""
	}
	message := "agent archive import failed"
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	if e.RollbackErr != nil {
		message += "; rollback agent source: " + e.RollbackErr.Error()
	}
	if e.ReloadErr != nil {
		message += "; reload restored catalog: " + e.ReloadErr.Error()
	}
	return message
}

func (e *ArchiveRollbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ImportAgentArchive owns the source mutation transaction: staging, reload
// validation, commit, and best-effort rollback/reload recovery. HTTP mapping
// and response DTO construction remain in server.
func (s *Service) ImportAgentArchive(
	ctx context.Context,
	editor AgentArchiveEditor,
	source io.ReaderAt,
	size int64,
	overwrite bool,
	reload func(context.Context) error,
	validate func(string) error,
) (string, error) {
	if s == nil || editor == nil || reload == nil {
		return "", fmt.Errorf("agent archive import is not configured")
	}
	unlock := s.LockAgentMutation()
	defer unlock()
	mutation, err := editor.BeginImportEditableAgentArchive(source, size, overwrite)
	if err != nil {
		return "", &ArchiveBeginError{Cause: err}
	}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		_ = editor.RollbackEditableAgentArchiveMutation(mutation)
		_ = reload(context.WithoutCancel(ctx))
	}()
	rollback := func(cause error) error {
		finalized = true
		rollbackErr := editor.RollbackEditableAgentArchiveMutation(mutation)
		reloadErr := reload(context.WithoutCancel(ctx))
		if rollbackErr == nil && reloadErr == nil {
			return cause
		}
		return &ArchiveRollbackError{AgentKey: mutation.Key, Cause: cause, RollbackErr: rollbackErr, ReloadErr: reloadErr}
	}
	if err := reload(ctx); err != nil {
		return "", rollback(err)
	}
	if validate != nil {
		if err := validate(mutation.Key); err != nil {
			return "", rollback(err)
		}
	}
	if err := editor.CommitEditableAgentArchiveMutation(mutation); err != nil {
		return "", rollback(err)
	}
	finalized = true
	return mutation.Key, nil
}
