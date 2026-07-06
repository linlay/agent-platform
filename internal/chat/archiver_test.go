package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiverMovesChatToArchiveAndPreservesAttachments(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archive)

	if _, _, err := active.EnsureChat("chat-archiver", "agent-a", "", "hello archive"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := active.AppendQueryLine("chat-archiver", QueryLine{
		ChatID:    "chat-archiver",
		RunID:     "run-archiver",
		UpdatedAt: 1000,
		Query:     map[string]any{"role": "user", "message": "hello archive"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := active.AppendStepLine("chat-archiver", StepLine{
		ChatID:    "chat-archiver",
		RunID:     "run-archiver",
		UpdatedAt: 2000,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "archived response"}},
		}},
		Type: "react",
		Usage: map[string]any{
			"promptTokens":     1,
			"completionTokens": 2,
			"totalTokens":      3,
		},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := active.OnRunCompleted(RunCompletion{
		ChatID:          "chat-archiver",
		RunID:           "run-archiver",
		AgentKey:        "agent-a",
		AssistantText:   "archived response",
		InitialMessage:  "hello archive",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
		Usage: UsageData{
			ModelKey:               "mock-model",
			PromptTokens:           1,
			CompletionTokens:       2,
			TotalTokens:            3,
			EstimatedCostCurrency:  "CNY",
			EstimatedCostInputMiss: 0.01,
			EstimatedCostOutput:    0.02,
			EstimatedCostTotal:     0.03,
		},
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if err := os.MkdirAll(active.ChatDir("chat-archiver"), 0o755); err != nil {
		t.Fatalf("create chat attachments dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-archiver"), "artifact.txt"), []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(active.ChatDir("chat-archiver"), ToolRootDirName, ToolResultsDirName), 0o755); err != nil {
		t.Fatalf("create tool results dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-archiver"), ToolRootDirName, ToolResultsDirName, "call_1.json"), []byte(`{"stdout":"x"}`), 0o644); err != nil {
		t.Fatalf("write tool result: %v", err)
	}

	if err := archiver.ArchiveChat("chat-archiver"); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	if sum, err := active.Summary("chat-archiver"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum != nil {
		t.Fatalf("expected active summary removed, got %#v", sum)
	}
	if _, err := os.Stat(filepath.Join(root, "chat-archiver.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected active jsonl removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.ChatDir("chat-archiver"), "artifact.txt")); err != nil {
		t.Fatalf("expected artifact moved to archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.ChatDir("chat-archiver"), ToolRootDirName, ToolResultsDirName, "call_1.json")); err != nil {
		t.Fatalf("expected tool result moved to archive: %v", err)
	}

	loaded, err := archive.LoadArchived("chat-archiver")
	if err != nil {
		t.Fatalf("load archived: %v", err)
	}
	if !loaded.Summary.HasAttachments || loaded.Summary.AgentKey != "agent-a" || len(loaded.Detail.Events) == 0 {
		t.Fatalf("unexpected archived chat: %#v", loaded)
	}
	if loaded.Summary.Usage == nil || loaded.Summary.Usage.EstimatedCostTotal != 0.03 || loaded.Summary.Usage.ModelKey != "" {
		t.Fatalf("expected archived summary cost without aggregate modelKey, got %#v", loaded.Summary.Usage)
	}
	if len(loaded.Runs) != 1 || loaded.Runs[0].Usage.ModelKey != "mock-model" || loaded.Runs[0].Usage.EstimatedCostTotal != 0.03 {
		t.Fatalf("expected archived run modelKey and cost, got %#v", loaded.Runs)
	}
}

func TestArchiverMovesToolStateWithoutMarkingAttachments(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archive)

	if _, _, err := active.EnsureChat("chat-tool-state", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := active.AppendQueryLine("chat-tool-state", QueryLine{
		ChatID: "chat-tool-state",
		RunID:  "loyw3v28",
		Query:  map[string]any{"message": "hello"},
		Type:   "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := active.OnRunCompleted(RunCompletion{
		ChatID:          "chat-tool-state",
		RunID:           "loyw3v28",
		AgentKey:        "agent-a",
		AssistantText:   "done",
		InitialMessage:  "hello",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	stateDir := filepath.Join(active.ChatDir("chat-tool-state"), ToolRootDirName, ToolStateDirName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir tool state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, FileVersionsFileName), []byte(`{"version":1,"files":{}}`), 0o644); err != nil {
		t.Fatalf("write tool state: %v", err)
	}

	if err := archiver.ArchiveChat("chat-tool-state"); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archive.ChatDir("chat-tool-state"), ToolRootDirName, ToolStateDirName, FileVersionsFileName)); err != nil {
		t.Fatalf("expected tool state moved to archive: %v", err)
	}
	loaded, err := archive.LoadArchived("chat-tool-state")
	if err != nil {
		t.Fatalf("load archived chat: %v", err)
	}
	if loaded.Summary.HasAttachments {
		t.Fatalf("tool state should not mark archived chat as having user attachments")
	}
}

func TestArchiverRestoresArchivedChatAndRemovesArchive(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archive)

	if _, _, err := active.EnsureChatWithSource("chat-restore", "agent-a", "team-a", "hello restore", "automation:daily"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := active.SetSourceChannel("chat-restore", "desktop"); err != nil {
		t.Fatalf("set source channel: %v", err)
	}
	if err := active.AppendQueryLine("chat-restore", QueryLine{
		ChatID:    "chat-restore",
		RunID:     "run-restore",
		UpdatedAt: 1000,
		Query:     map[string]any{"role": "user", "message": "hello restore"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := active.OnRunCompleted(RunCompletion{
		ChatID:          "chat-restore",
		RunID:           "run-restore",
		AgentKey:        "agent-a",
		AssistantText:   "restored response",
		InitialMessage:  "hello restore",
		FinishReason:    "complete",
		StartedAtMillis: 1000,
		UpdatedAtMillis: 2000,
		Usage:           UsageData{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if err := os.MkdirAll(active.ChatDir("chat-restore"), 0o755); err != nil {
		t.Fatalf("mkdir active chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.ChatDir("chat-restore"), "artifact.txt"), []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	if err := archiver.ArchiveChat("chat-restore"); err != nil {
		t.Fatalf("archive chat: %v", err)
	}
	if _, err := archive.LoadArchived("chat-restore"); err != nil {
		t.Fatalf("expected archive before restore: %v", err)
	}

	summary, err := archiver.RestoreChat("chat-restore")
	if err != nil {
		t.Fatalf("restore chat: %v", err)
	}
	if summary.ChatID != "chat-restore" || summary.AgentKey != "agent-a" || summary.TeamID != "team-a" || summary.Source != "automation:daily" || summary.SourceChannel != "desktop" {
		t.Fatalf("unexpected restored summary: %#v", summary)
	}
	if summary.Read.IsRead {
		t.Fatalf("new archived unread state should be preserved, got %#v", summary.Read)
	}
	if summary.Usage == nil || summary.Usage.TotalTokens != 9 {
		t.Fatalf("expected restored usage, got %#v", summary.Usage)
	}
	runs, err := active.ListRuns("chat-restore")
	if err != nil {
		t.Fatalf("list restored runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-restore" || runs[0].Usage.TotalTokens != 9 {
		t.Fatalf("unexpected restored runs: %#v", runs)
	}
	detail, err := active.LoadChat("chat-restore")
	if err != nil {
		t.Fatalf("load restored chat: %v", err)
	}
	if len(detail.Events) == 0 {
		t.Fatalf("expected restored replay events")
	}
	if _, err := os.Stat(filepath.Join(active.ChatDir("chat-restore"), "artifact.txt")); err != nil {
		t.Fatalf("expected artifact restored to active dir: %v", err)
	}
	if _, err := archive.LoadArchived("chat-restore"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("expected archive removed after restore, got %v", err)
	}
}

func TestArchiverRestoreConflictsWithActiveChat(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	if err := archive.ArchiveChat(testArchivedChat("chat-restore-conflict", "agent-a", "Archived", "done")); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := active.EnsureChat("chat-restore-conflict", "agent-a", "", "active"); err != nil {
		t.Fatalf("ensure active: %v", err)
	}

	if _, err := NewArchiver(active, archive).RestoreChat("chat-restore-conflict"); !errors.Is(err, ErrChatAlreadyActive) {
		t.Fatalf("expected ErrChatAlreadyActive, got %v", err)
	}
	if _, err := archive.LoadArchived("chat-restore-conflict"); err != nil {
		t.Fatalf("archive should remain after conflict: %v", err)
	}
}

func TestArchiverLeavesActiveChatWhenArchiveAlreadyExists(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	if _, _, err := active.EnsureChat("chat-duplicate-archive", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure active chat: %v", err)
	}
	if err := archive.ArchiveChat(testArchivedChat("chat-duplicate-archive", "agent-a", "hello", "done")); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	err = NewArchiver(active, archive).ArchiveChat("chat-duplicate-archive")
	if !errors.Is(err, ErrChatAlreadyArchived) {
		t.Fatalf("expected ErrChatAlreadyArchived, got %v", err)
	}
	if sum, err := active.Summary("chat-duplicate-archive"); err != nil {
		t.Fatalf("active summary: %v", err)
	} else if sum == nil {
		t.Fatalf("expected active chat to remain")
	}
}
