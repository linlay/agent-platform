package chat

import "testing"

func TestTeamRunOwnerPersistsWithoutSyntheticAgentKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	summary, created, err := store.EnsureChatWithSourceAndMode("chat-team-owner", "research", "hello", "", "TEAM")
	if err != nil {
		t.Fatalf("ensure team chat: %v", err)
	}
	if !created || summary.AgentKey != "research" || summary.AgentMode != "TEAM" {
		t.Fatalf("unexpected team summary %#v", summary)
	}

	if err := completeRunForTest(store, RunCompletion{
		ChatID:   "chat-team-owner",
		RunID:    "run-team-owner",
		AgentKey: "research",

		AssistantText:   "done",
		FinishReason:    "complete",
		UpdatedAtMillis: testEpochMillis(1000),
	}); err != nil {
		t.Fatalf("complete team run: %v", err)
	}

	runs, err := store.ListRuns("chat-team-owner")
	if err != nil {
		t.Fatalf("list team runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[0].AgentKey != "research" {
		t.Fatalf("synthetic agent leaked into persisted run %#v", runs[0])
	}

	loaded, err := store.Summary("chat-team-owner")
	if err != nil {
		t.Fatalf("load team summary: %v", err)
	}
	if loaded.AgentKey != "research" {
		t.Fatalf("unexpected reloaded team summary %#v", loaded)
	}
}

func TestTeamRunOwnerSurvivesArchiveAndRestore(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	t.Cleanup(func() { _ = active.Close() })
	archives, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archives)

	if _, _, err := active.EnsureChatWithSourceAndMode("chat-team-archive", "research", "hello", "", "TEAM"); err != nil {
		t.Fatalf("ensure team chat: %v", err)
	}
	if err := completeRunForTest(active, RunCompletion{
		ChatID:   "chat-team-archive",
		RunID:    "run-team-archive",
		AgentKey: "research",

		AssistantText:   "done",
		FinishReason:    "complete",
		UpdatedAtMillis: testEpochMillis(1000),
	}); err != nil {
		t.Fatalf("complete team run: %v", err)
	}
	if err := archiver.ArchiveChat("chat-team-archive"); err != nil {
		t.Fatalf("archive team chat: %v", err)
	}

	archived, err := archives.LoadArchived("chat-team-archive")
	if err != nil {
		t.Fatalf("load archived team chat: %v", err)
	}
	if archived.Summary.AgentKey != "research" {
		t.Fatalf("unexpected archived owner %#v", archived.Summary)
	}
	if len(archived.Runs) != 1 || archived.Runs[0].AgentKey != "research" {
		t.Fatalf("unexpected archived runs %#v", archived.Runs)
	}

	restored, err := archiver.RestoreChat("chat-team-archive")
	if err != nil {
		t.Fatalf("restore team chat: %v", err)
	}
	if restored.AgentKey != "research" {
		t.Fatalf("unexpected restored owner %#v", restored)
	}
	restoredRuns, err := active.ListRuns("chat-team-archive")
	if err != nil {
		t.Fatalf("list restored runs: %v", err)
	}
	if len(restoredRuns) != 1 || restoredRuns[0].AgentKey != "research" {
		t.Fatalf("unexpected restored runs %#v", restoredRuns)
	}
}

func TestArchivePreservesHistoricalAgentMode(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	t.Cleanup(func() { _ = active.Close() })
	archives, err := NewArchiveStore(root)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	archiver := NewArchiver(active, archives)

	if _, _, err := active.EnsureChatWithSourceAndMode("chat-historical-mode", "former-agent", "history", "", "ONESHOT"); err != nil {
		t.Fatalf("ensure historical chat: %v", err)
	}
	persistAgentModeRun(t, active, "chat-historical-mode", "run-historical-mode", "former-agent", "ONESHOT", 1_000)
	if err := archiver.ArchiveChat("chat-historical-mode"); err != nil {
		t.Fatalf("archive historical chat: %v", err)
	}
	archived, err := archives.LoadArchived("chat-historical-mode")
	if err != nil || archived == nil || archived.Summary.AgentMode != "ONESHOT" {
		t.Fatalf("archive rewrote historical mode: archived=%#v err=%v", archived, err)
	}
	restored, err := archiver.RestoreChat("chat-historical-mode")
	if err != nil || restored.AgentMode != "ONESHOT" {
		t.Fatalf("restore rewrote historical mode: restored=%#v err=%v", restored, err)
	}
}
