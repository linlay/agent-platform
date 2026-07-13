package chat

import "testing"

func TestTeamRunOwnerPersistsWithoutSyntheticAgentKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	summary, created, err := store.EnsureChatWithSourceAndMode("chat-team-owner", "", "team-a", "hello", "", "REACT")
	if err != nil {
		t.Fatalf("ensure team chat: %v", err)
	}
	if !created || summary.OwnerType != "team" || summary.AgentKey != "" || summary.AgentMode != "TEAM" || summary.TeamID != "team-a" {
		t.Fatalf("unexpected team summary %#v", summary)
	}

	if err := completeRunForTest(store, RunCompletion{
		ChatID:          "chat-team-owner",
		RunID:           "run-team-owner",
		OwnerType:       "team",
		AgentKey:        "__team_coordinator",
		TeamID:          "team-a",
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
	if runs[0].OwnerType != "team" || runs[0].AgentKey != "" || runs[0].AgentMode != "TEAM" || runs[0].TeamID != "team-a" {
		t.Fatalf("synthetic agent leaked into persisted run %#v", runs[0])
	}

	loaded, err := store.Summary("chat-team-owner")
	if err != nil {
		t.Fatalf("load team summary: %v", err)
	}
	if loaded == nil || loaded.OwnerType != "team" || loaded.AgentKey != "" || loaded.AgentMode != "TEAM" || loaded.TeamID != "team-a" {
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

	if _, _, err := active.EnsureChatWithSourceAndMode("chat-team-archive", "", "team-a", "hello", "", "REACT"); err != nil {
		t.Fatalf("ensure team chat: %v", err)
	}
	if err := completeRunForTest(active, RunCompletion{
		ChatID:          "chat-team-archive",
		RunID:           "run-team-archive",
		OwnerType:       "team",
		TeamID:          "team-a",
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
	if archived.Summary.OwnerType != "team" || archived.Summary.AgentKey != "" || archived.Summary.AgentMode != "TEAM" || archived.Summary.TeamID != "team-a" {
		t.Fatalf("unexpected archived owner %#v", archived.Summary)
	}
	if len(archived.Runs) != 1 || archived.Runs[0].OwnerType != "team" || archived.Runs[0].AgentKey != "" || archived.Runs[0].AgentMode != "TEAM" || archived.Runs[0].TeamID != "team-a" {
		t.Fatalf("unexpected archived runs %#v", archived.Runs)
	}

	restored, err := archiver.RestoreChat("chat-team-archive")
	if err != nil {
		t.Fatalf("restore team chat: %v", err)
	}
	if restored.OwnerType != "team" || restored.AgentKey != "" || restored.AgentMode != "TEAM" || restored.TeamID != "team-a" {
		t.Fatalf("unexpected restored owner %#v", restored)
	}
	restoredRuns, err := active.ListRuns("chat-team-archive")
	if err != nil {
		t.Fatalf("list restored runs: %v", err)
	}
	if len(restoredRuns) != 1 || restoredRuns[0].OwnerType != "team" || restoredRuns[0].AgentKey != "" || restoredRuns[0].AgentMode != "TEAM" || restoredRuns[0].TeamID != "team-a" {
		t.Fatalf("unexpected restored runs %#v", restoredRuns)
	}
}
