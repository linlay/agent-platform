package adminsource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"agent-platform/internal/catalog"
)

type archiveEditorStub struct {
	beginErr    error
	commitErr   error
	rollbackErr error
	commits     int
	rollbacks   int
}

func (s *archiveEditorStub) BeginImportEditableAgentArchive(_ io.ReaderAt, _ int64, _ bool) (*catalog.EditableAgentArchiveMutation, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return &catalog.EditableAgentArchiveMutation{Key: "agent-a"}, nil
}

func (s *archiveEditorStub) RollbackEditableAgentArchiveMutation(*catalog.EditableAgentArchiveMutation) error {
	s.rollbacks++
	return s.rollbackErr
}

func (s *archiveEditorStub) CommitEditableAgentArchiveMutation(*catalog.EditableAgentArchiveMutation) error {
	s.commits++
	return s.commitErr
}

func TestImportAgentArchiveCommitsOnlyAfterReloadAndValidation(t *testing.T) {
	service := NewService()
	editor := &archiveEditorStub{}
	reloads := 0
	validated := ""
	key, err := service.ImportAgentArchive(context.Background(), editor, bytes.NewReader([]byte("zip")), 3, false,
		func(context.Context) error { reloads++; return nil },
		func(key string) error { validated = key; return nil },
	)
	if err != nil || key != "agent-a" || validated != "agent-a" || reloads != 1 || editor.commits != 1 || editor.rollbacks != 0 {
		t.Fatalf("unexpected transaction result key=%q err=%v validated=%q reloads=%d commits=%d rollbacks=%d", key, err, validated, reloads, editor.commits, editor.rollbacks)
	}
}

func TestImportAgentArchiveReportsRollbackRecoveryFailure(t *testing.T) {
	service := NewService()
	editor := &archiveEditorStub{rollbackErr: errors.New("restore failed")}
	reloads := 0
	_, err := service.ImportAgentArchive(context.Background(), editor, bytes.NewReader([]byte("zip")), 3, true,
		func(context.Context) error { reloads++; return nil },
		func(string) error { return errors.New("invalid published agent") },
	)
	var rollbackErr *ArchiveRollbackError
	if !errors.As(err, &rollbackErr) || rollbackErr.AgentKey != "agent-a" || rollbackErr.RollbackErr == nil {
		t.Fatalf("unexpected rollback error: %T %v", err, err)
	}
	if reloads != 2 || editor.commits != 0 || editor.rollbacks != 1 {
		t.Fatalf("unexpected recovery calls reloads=%d commits=%d rollbacks=%d", reloads, editor.commits, editor.rollbacks)
	}
}
