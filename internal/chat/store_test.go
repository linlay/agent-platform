package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/stream"
)

func TestEnsureChatDoesNotCreateChatDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	summary, created, err := store.EnsureChat("chat-no-dir", "agent", "", "hello")
	if err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if !created {
		t.Fatal("expected chat to be created")
	}
	if summary.ChatID != "chat-no-dir" {
		t.Fatalf("chat id = %q", summary.ChatID)
	}
	if _, err := os.Stat(store.ChatDir("chat-no-dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no chat directory, stat err=%v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expected chat root to exist: %v", err)
	}
}

func TestFileStoreSetPendingAwaitingPersistsIntoSummaryAndListChats(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-pending", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	pending := PendingAwaiting{
		AwaitingID: "await_1",
		RunID:      "run_1",
		Mode:       "question",
		CreatedAt:  12345,
	}
	if err := store.SetPendingAwaiting("chat-pending", pending); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}

	summary, err := store.Summary("chat-pending")
	if err != nil {
		t.Fatalf("load summary: %v", err)
	}
	if summary == nil || summary.PendingAwaiting == nil || *summary.PendingAwaiting != pending {
		t.Fatalf("unexpected summary pending awaiting %#v", summary)
	}

	items, err := store.ListChats("", "")
	if err != nil {
		t.Fatalf("list chats: %v", err)
	}
	if len(items) != 1 || items[0].PendingAwaiting == nil || *items[0].PendingAwaiting != pending {
		t.Fatalf("unexpected listed pending awaiting %#v", items)
	}
}

func TestFileStoreUpdateAgentKeyPersistsIntoSummary(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-agent-key", "", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.UpdateAgentKey("chat-agent-key", "agent-b"); err != nil {
		t.Fatalf("update agent key: %v", err)
	}

	summary, err := store.Summary("chat-agent-key")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.AgentKey != "agent-b" {
		t.Fatalf("expected agent-b, got %q", summary.AgentKey)
	}
}

func TestFileStoreSetSourceChannelPersistsIntoSummaryAndListChats(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("wecom#single#u1#1", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.SetSourceChannel("wecom#single#u1#1", "wecom:langyage"); err != nil {
		t.Fatalf("set source channel: %v", err)
	}
	sourceChannel, err := store.SourceChannel("wecom#single#u1#1")
	if err != nil {
		t.Fatalf("source channel: %v", err)
	}
	if sourceChannel != "wecom:langyage" {
		t.Fatalf("expected source channel wecom:langyage, got %q", sourceChannel)
	}
	summary, err := store.Summary("wecom#single#u1#1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.SourceChannel != "wecom:langyage" {
		t.Fatalf("expected source channel in summary, got %#v", summary)
	}
	items, err := store.ListChats("", "")
	if err != nil {
		t.Fatalf("list chats: %v", err)
	}
	if len(items) != 1 || items[0].SourceChannel != "wecom:langyage" {
		t.Fatalf("expected source channel in list, got %#v", items)
	}
}

func TestFileStoreClearPendingAwaitingClearsMatchingAwaitingID(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-clear", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.SetPendingAwaiting("chat-clear", PendingAwaiting{
		AwaitingID: "await_1",
		RunID:      "run_1",
		Mode:       "approval",
		CreatedAt:  45678,
	}); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}

	if err := store.ClearPendingAwaiting("chat-clear", "await_1"); err != nil {
		t.Fatalf("clear pending awaiting: %v", err)
	}

	summary, err := store.Summary("chat-clear")
	if err != nil {
		t.Fatalf("load summary: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected cleared pending awaiting, got %#v", summary)
	}
}

func TestFileStoreClearPendingAwaitingIgnoresStaleAwaitingID(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-stale", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	pending := PendingAwaiting{
		AwaitingID: "await_latest",
		RunID:      "run_latest",
		Mode:       "form",
		CreatedAt:  78901,
	}
	if err := store.SetPendingAwaiting("chat-stale", pending); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}

	if err := store.ClearPendingAwaiting("chat-stale", "await_old"); err != nil {
		t.Fatalf("clear stale pending awaiting: %v", err)
	}

	summary, err := store.Summary("chat-stale")
	if err != nil {
		t.Fatalf("load summary: %v", err)
	}
	if summary == nil || summary.PendingAwaiting == nil || *summary.PendingAwaiting != pending {
		t.Fatalf("expected pending awaiting to remain after stale clear, got %#v", summary)
	}
}

func TestFileStoreLoadAllPendingAwaitingsReturnsPersistedRows(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-pending-a", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat a: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-pending-b", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat b: %v", err)
	}
	if err := store.SetPendingAwaiting("chat-pending-a", PendingAwaiting{
		AwaitingID: "await_a",
		RunID:      "run_a",
		Mode:       "question",
		CreatedAt:  111,
	}); err != nil {
		t.Fatalf("set pending a: %v", err)
	}
	if err := store.SetPendingAwaiting("chat-pending-b", PendingAwaiting{
		AwaitingID: "await_b",
		RunID:      "run_b",
		Mode:       "approval",
		CreatedAt:  222,
	}); err != nil {
		t.Fatalf("set pending b: %v", err)
	}

	items, err := store.LoadAllPendingAwaitings()
	if err != nil {
		t.Fatalf("load all pending awaitings: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 pending awaitings, got %#v", items)
	}
	if items[0].ChatID != "chat-pending-a" || items[0].AwaitingID != "await_a" || items[1].ChatID != "chat-pending-b" || items[1].AwaitingID != "await_b" {
		t.Fatalf("unexpected pending awaitings %#v", items)
	}
}

func TestFileStoreLoadAwaitingAskFindsStepAndEventLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-step", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure step chat: %v", err)
	}
	if err := store.AppendStepLine("chat-awaiting-step", StepLine{
		ChatID:    "chat-awaiting-step",
		RunID:     "run-step",
		UpdatedAt: 100,
		Type:      "react",
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"awaitingId": "await_step",
				"mode":       "question",
				"questions": []any{
					map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	ask, err := store.LoadAwaitingAsk("chat-awaiting-step", "await_step")
	if err != nil {
		t.Fatalf("load step awaiting ask: %v", err)
	}
	if ask == nil || ask.AwaitingID != "await_step" || ask.RunID != "run-step" || ask.Mode != "question" {
		t.Fatalf("unexpected step awaiting ask %#v", ask)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-event", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure event chat: %v", err)
	}
	if err := store.AppendEventLine("chat-awaiting-event", EventLine{
		ChatID:    "chat-awaiting-event",
		RunID:     "run-event",
		UpdatedAt: 200,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "await_event",
			"mode":       "approval",
			"approvals": []any{
				map[string]any{"id": "cmd-1", "command": "rm -rf /tmp/demo"},
			},
		},
	}); err != nil {
		t.Fatalf("append event line: %v", err)
	}
	ask, err = store.LoadAwaitingAsk("chat-awaiting-event", "await_event")
	if err != nil {
		t.Fatalf("load event awaiting ask: %v", err)
	}
	if ask == nil || ask.AwaitingID != "await_event" || ask.RunID != "run-event" || ask.Mode != "approval" {
		t.Fatalf("unexpected event awaiting ask %#v", ask)
	}
}

func TestFileStoreMigratesLegacyDBAndRoundTripsPendingAwaiting(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("open legacy chats db: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE CHATS (
			CHAT_ID_ TEXT PRIMARY KEY,
			CHAT_NAME_ TEXT NOT NULL,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			TEAM_ID_ TEXT,
			CREATED_AT_ INTEGER NOT NULL,
			UPDATED_AT_ INTEGER NOT NULL,
			LAST_RUN_ID_ TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_STATUS_ INTEGER NOT NULL DEFAULT 1,
			READ_AT_ INTEGER,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			PENDING_AWAITING_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("create legacy chats table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO CHATS (
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_,
			PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_
		) VALUES (?, ?, ?, ?, ?, '', '', 1, ?, ?, ?, ?)`,
		"chat-legacy", "legacy", "agent", 100, 200, "await_legacy", "run_legacy", "question", 24680)
	if err != nil {
		t.Fatalf("insert legacy chat: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy chats db: %v", err)
	}

	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store from legacy db: %v", err)
	}

	summary, err := store.Summary("chat-legacy")
	if err != nil {
		t.Fatalf("load migrated summary: %v", err)
	}
	if summary == nil || summary.PendingAwaiting == nil {
		t.Fatalf("expected migrated summary with pending awaiting, got %#v", summary)
	}
	if summary.Read.ReadRunID != "" || !summary.Read.IsRead {
		t.Fatalf("expected migrated summary to preserve read state, got %#v", summary.Read)
	}
	if *summary.PendingAwaiting != (PendingAwaiting{
		AwaitingID: "await_legacy",
		RunID:      "run_legacy",
		Mode:       "question",
		CreatedAt:  24680,
	}) {
		t.Fatalf("expected migrated summary pending awaiting, got %#v", summary)
	}
	if err := store.ClearPendingAwaiting("chat-legacy", "await_legacy"); err != nil {
		t.Fatalf("clear pending awaiting after migration: %v", err)
	}
	summary, err = store.Summary("chat-legacy")
	if err != nil {
		t.Fatalf("reload migrated summary after clear: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting cleared after migration round trip, got %#v", summary)
	}

	db, err = sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("reopen migrated chats db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA table_info(CHATS)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		if name == "READ_STATUS_" {
			t.Fatal("expected READ_STATUS_ column to be removed during migration")
		}
		if name == "PENDING_AWAITING_ID_" {
			t.Fatal("expected pending awaiting columns to be renamed during migration")
		}
	}
}

func TestFileStoreMarkReadAdvancesWatermarkAndClampsFutureRunID(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-read", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	run1 := "loyw3v28"
	run2 := "loyw3v2s"
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-read",
		RunID:           run1,
		AssistantText:   "first",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-read",
		RunID:           run2,
		AssistantText:   "second",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("complete second run: %v", err)
	}

	sum, err := store.MarkRead("chat-read", run1)
	if err != nil {
		t.Fatalf("mark read run1: %v", err)
	}
	if sum.Read.ReadRunID != run1 || sum.Read.IsRead {
		t.Fatalf("expected partial read watermark at run1, got %#v", sum.Read)
	}
	firstReadAt := sum.Read.ReadAt

	sum, err = store.MarkRead("chat-read", "zzzzzzzz")
	if err != nil {
		t.Fatalf("mark read future run: %v", err)
	}
	if sum.Read.ReadRunID != run2 || !sum.Read.IsRead {
		t.Fatalf("expected future run to clamp to last run, got %#v", sum.Read)
	}
	if sum.Read.ReadAt == nil || firstReadAt == nil || *sum.Read.ReadAt < *firstReadAt {
		t.Fatalf("expected readAt to refresh monotonically, got old=%v new=%v", firstReadAt, sum.Read.ReadAt)
	}

	sum, err = store.MarkRead("chat-read", run1)
	if err != nil {
		t.Fatalf("mark read stale run: %v", err)
	}
	if sum.Read.ReadRunID != run2 {
		t.Fatalf("expected read watermark not to roll back, got %#v", sum.Read)
	}
}

func TestFileStoreRunMetadataTruncatesAndFeedbackUpdates(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-runs", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	longText := strings.Repeat("界", 250)
	longInitialMessage := strings.Repeat("问", 250)
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-runs",
		RunID:           "loyw3v28",
		AgentKey:        "agent-a",
		AssistantText:   longText,
		InitialMessage:  longInitialMessage,
		FinishReason:    "complete",
		StartedAtMillis: 100,
		UpdatedAtMillis: 200,
		Usage:           UsageData{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	sum, err := store.Summary("chat-runs")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got := len([]rune(sum.LastRunContent)); got != 200 {
		t.Fatalf("expected truncated lastRunContent, got %d runes", got)
	}

	runs, err := store.ListRuns("chat-runs")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %#v", runs)
	}
	if got := len([]rune(runs[0].AssistantText)); got != 200 {
		t.Fatalf("expected truncated assistant text, got %d runes", got)
	}
	if got := len([]rune(runs[0].InitialMessage)); got != 200 {
		t.Fatalf("expected truncated initial message, got %d runes", got)
	}
	if runs[0].AgentKey != "agent-a" || runs[0].FinishReason != "complete" || runs[0].Usage.TotalTokens != 3 {
		t.Fatalf("unexpected run summary: %#v", runs[0])
	}

	setAt, err := store.SetFeedback("chat-runs", "loyw3v28", "thumbs_down", "not useful")
	if err != nil {
		t.Fatalf("set feedback: %v", err)
	}
	runs, err = store.ListRuns("chat-runs")
	if err != nil {
		t.Fatalf("list runs after feedback: %v", err)
	}
	if runs[0].FeedbackType != "thumbs_down" || runs[0].FeedbackComment != "not useful" || runs[0].FeedbackAt != setAt {
		t.Fatalf("expected feedback in run summary, got %#v", runs[0])
	}
	if _, err := store.SetFeedback("chat-runs", "loyw3v28", "clear", "ignored"); err != nil {
		t.Fatalf("clear feedback: %v", err)
	}
	runs, err = store.ListRuns("chat-runs")
	if err != nil {
		t.Fatalf("list runs after clearing feedback: %v", err)
	}
	if runs[0].FeedbackType != "" || runs[0].FeedbackComment != "" || runs[0].FeedbackAt != 0 {
		t.Fatalf("expected cleared feedback in run summary, got %#v", runs[0])
	}
}

func TestFileStoreMarkAllReadFiltersAgent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	for _, item := range []struct {
		chatID   string
		agentKey string
		runID    string
	}{
		{"chat-a1", "agent-a", "loyw3v28"},
		{"chat-a2", "agent-a", "loyw3v2s"},
		{"chat-b1", "agent-b", "loyw3v34"},
	} {
		if _, _, err := store.EnsureChat(item.chatID, item.agentKey, "", "hello"); err != nil {
			t.Fatalf("ensure %s: %v", item.chatID, err)
		}
		if err := store.OnRunCompleted(RunCompletion{ChatID: item.chatID, RunID: item.runID, UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
			t.Fatalf("complete %s: %v", item.chatID, err)
		}
	}
	updated, err := store.MarkAllRead("agent-a")
	if err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	if updated != 2 {
		t.Fatalf("expected 2 updated chats, got %d", updated)
	}
	stats, err := store.AgentChatStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["agent-a"].UnreadCount != 0 || stats["agent-b"].UnreadCount != 1 {
		t.Fatalf("unexpected stats after mark all read: %#v", stats)
	}
}

func TestFileStoreDeleteChatRemovesRowsAndFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-delete", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-delete", QueryLine{
		ChatID: "chat-delete",
		RunID:  "loyw3v28",
		Query:  map[string]any{"message": "hello"},
		Type:   "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := os.MkdirAll(store.ChatDir("chat-delete"), 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-delete"), "artifact.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(store.ChatDir("chat-delete"), ToolResultsDirName), 0o755); err != nil {
		t.Fatalf("mkdir tool results dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-delete"), ToolResultsDirName, "call_1.json"), []byte(`{"stdout":"x"}`), 0o644); err != nil {
		t.Fatalf("write tool result: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{ChatID: "chat-delete", RunID: "loyw3v28", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if err := store.DeleteChat("chat-delete"); err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	if sum, err := store.Summary("chat-delete"); err != nil {
		t.Fatalf("summary after delete: %v", err)
	} else if sum != nil {
		t.Fatal("expected deleted summary to be nil")
	}
	if _, err := os.Stat(filepath.Join(root, "chat-delete.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected jsonl removed, got %v", err)
	}
	if _, err := os.Stat(store.ChatDir("chat-delete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected chat dir removed, got %v", err)
	}
}

func TestFileStoreAgentChatStatsAggregatesUnreadCounts(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-a1", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a1: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-a2", "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat-a2: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-b1", "agent-b", "", "hello"); err != nil {
		t.Fatalf("ensure chat-b1: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{ChatID: "chat-a1", RunID: "loyw3v28", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("complete chat-a1: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{ChatID: "chat-a2", RunID: "loyw3v2s", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("complete chat-a2: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{ChatID: "chat-b1", RunID: "loyw3v34", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("complete chat-b1: %v", err)
	}
	if _, err := store.MarkRead("chat-a1", "loyw3v28"); err != nil {
		t.Fatalf("mark chat-a1 read: %v", err)
	}

	stats, err := store.AgentChatStats()
	if err != nil {
		t.Fatalf("agent chat stats: %v", err)
	}
	if got := stats["agent-a"]; got.TotalCount != 2 || got.UnreadCount != 1 {
		t.Fatalf("unexpected agent-a stats: %#v", got)
	}
	if got := stats["agent-b"]; got.TotalCount != 1 || got.UnreadCount != 1 {
		t.Fatalf("unexpected agent-b stats: %#v", got)
	}
}

func TestFileStoreRecentChatsByAgentFiltersLimitsAndSorts(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	for _, seed := range []struct {
		chatID  string
		agent   string
		runID   string
		updated int64
	}{
		{chatID: "chat-a-old", agent: "agent-a", runID: "loyw3v20", updated: 1000},
		{chatID: "chat-a-new", agent: "agent-a", runID: "loyw3v28", updated: 3000},
		{chatID: "chat-a-mid", agent: "agent-a", runID: "loyw3v24", updated: 2000},
		{chatID: "chat-b-new", agent: "agent-b", runID: "loyw3v2s", updated: 4000},
	} {
		if _, _, err := store.EnsureChat(seed.chatID, seed.agent, "", seed.chatID); err != nil {
			t.Fatalf("ensure %s: %v", seed.chatID, err)
		}
		if err := store.OnRunCompleted(RunCompletion{ChatID: seed.chatID, RunID: seed.runID, UpdatedAtMillis: seed.updated}); err != nil {
			t.Fatalf("complete %s: %v", seed.chatID, err)
		}
	}

	items, err := store.RecentChatsByAgent("agent-a", 2)
	if err != nil {
		t.Fatalf("recent chats: %v", err)
	}
	if len(items) != 2 || items[0].ChatID != "chat-a-new" || items[1].ChatID != "chat-a-mid" {
		t.Fatalf("unexpected recent chats: %#v", items)
	}
	if items[0].AgentKey != "agent-a" || items[1].AgentKey != "agent-a" {
		t.Fatalf("unexpected agent filtering: %#v", items)
	}

	empty, err := store.RecentChatsByAgent("agent-a", 0)
	if err != nil {
		t.Fatalf("recent chats limit 0: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no chats for limit 0, got %#v", empty)
	}
}

func TestFileStoreListChatsUsesParsedRunIDCursor(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-new", "agent", "", "new"); err != nil {
		t.Fatalf("ensure new chat: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-old", "agent", "", "old"); err != nil {
		t.Fatalf("ensure old chat: %v", err)
	}

	base36Later := "loyw3v2a"
	base36Earlier := "loyw3v28"
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-new",
		RunID:           base36Later,
		AssistantText:   "later",
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("complete new chat: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          "chat-old",
		RunID:           base36Earlier,
		AssistantText:   "earlier",
		UpdatedAtMillis: time.Now().Add(-time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("complete old chat: %v", err)
	}

	items, err := store.ListChats("loyw3v29", "")
	if err != nil {
		t.Fatalf("list chats with base36 cursor: %v", err)
	}
	if len(items) != 1 || items[0].ChatID != "chat-new" {
		t.Fatalf("expected later base36 run after cursor, got %#v", items)
	}
}

func TestRebuildSnapshotEventsGroupsByRunAndBackfillsLegacyIDs(t *testing.T) {
	run1 := "loyw3v28"
	run2 := "loyw3v2s"

	events := rebuildSnapshotEvents([]map[string]any{
		{"type": "request.query", "chatId": "chat_1", "message": "first"},
		{"type": "chat.start", "chatId": "chat_1", "chatName": "demo"},
		{"type": "run.start", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.start", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.end", "chatId": "chat_1", "runId": run1},
		{"type": "reasoning.snapshot", "chatId": "chat_1", "runId": run1, "text": "thinking"},
		{"type": "content.snapshot", "chatId": "chat_1", "runId": run1, "text": "answer 1"},
		{"type": "run.complete", "runId": run1},
		{"type": "request.query", "chatId": "chat_1", "message": "second"},
		{"type": "chat.start", "chatId": "chat_1", "chatName": "demo"},
		{"type": "run.start", "chatId": "chat_1", "runId": run2},
		{"type": "tool.start", "chatId": "chat_1", "runId": run2, "toolName": "datetime"},
		{"type": "tool.end", "chatId": "chat_1", "runId": run2},
		{"type": "tool.snapshot", "chatId": "chat_1", "runId": run2, "arguments": "{}"},
		{"type": "tool.result", "chatId": "chat_1", "runId": run2, "output": map[string]any{"ok": true}},
		{"type": "action.start", "chatId": "chat_1", "runId": run2, "actionName": "approval_action"},
		{"type": "action.end", "chatId": "chat_1", "runId": run2},
		{"type": "action.result", "chatId": "chat_1", "runId": run2, "result": map[string]any{"confirmed": true}},
		{"type": "run.complete", "runId": run2},
	})

	if len(events) != 18 {
		t.Fatalf("expected 18 rebuilt events, got %d: %#v", len(events), events)
	}
	if events[0]["type"] != "chat.start" {
		t.Fatalf("expected chat.start first, got %#v", events[0])
	}
	if events[1]["type"] != "request.query" || events[1]["runId"] != run1 {
		t.Fatalf("expected first request.query to bind to run1, got %#v", events[1])
	}
	if events[2]["type"] != "run.start" || events[2]["runId"] != run1 {
		t.Fatalf("expected run1 start after request, got %#v", events[2])
	}
	if events[8]["type"] != "request.query" || events[8]["runId"] != run2 {
		t.Fatalf("expected second request.query to bind to run2, got %#v", events[8])
	}
	if events[9]["type"] != "run.start" || events[9]["runId"] != run2 {
		t.Fatalf("expected run2 start after second request, got %#v", events[9])
	}

	if got := events[3]["reasoningId"]; got != run1+"_r_1" {
		t.Fatalf("expected reasoning.start to backfill run-scoped id, got %#v", events[3])
	}
	if got := events[3]["reasoningLabel"]; got != stream.ReasoningLabelForID(run1+"_r_1") {
		t.Fatalf("expected reasoning.start to backfill reasoningLabel, got %#v", events[3])
	}
	if got := events[5]["reasoningId"]; got != run1+"_r_1" {
		t.Fatalf("expected reasoning.snapshot to reuse prior id, got %#v", events[5])
	}
	if got := events[5]["reasoningLabel"]; got != stream.ReasoningLabelForID(run1+"_r_1") {
		t.Fatalf("expected reasoning.snapshot to reuse deterministic reasoningLabel, got %#v", events[5])
	}
	if got := events[6]["contentId"]; got != run1+"_c_1" {
		t.Fatalf("expected content.snapshot to backfill run-scoped id, got %#v", events[6])
	}

	if got := events[10]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.start fallback id, got %#v", events[10])
	}
	if got := events[11]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.end to reuse fallback id, got %#v", events[11])
	}
	if got := events[12]["toolId"]; got != run2+"_tool_1" {
		t.Fatalf("expected tool.snapshot to reuse fallback id, got %#v", events[12])
	}
	if got := events[13]["toolId"]; got != run2+"_tool_result_1" {
		t.Fatalf("expected tool.result fallback id after closed block, got %#v", events[13])
	}
	if got := events[14]["actionId"]; got != run2+"_action_1" {
		t.Fatalf("expected action.start fallback id, got %#v", events[14])
	}
	if got := events[15]["actionId"]; got != run2+"_action_1" {
		t.Fatalf("expected action.end to reuse fallback id, got %#v", events[15])
	}
	if got := events[16]["actionId"]; got != run2+"_action_result_1" {
		t.Fatalf("expected action.result fallback id after closed block, got %#v", events[16])
	}

	for index, event := range events {
		if got := int64(index + 1); event["seq"] != got {
			t.Fatalf("expected contiguous seq at index %d, got %#v", index, event)
		}
	}
}

func TestStoredMessageToEventsAddsReasoningLabel(t *testing.T) {
	runID := "run_1"
	events := storedMessageToEvents(map[string]any{
		"role":              "assistant",
		"_reasoningId":      runID + "_r_2",
		"reasoning_content": []any{map[string]any{"type": "text", "text": "thinking"}},
	}, runID, "task_1", "plan", func() int64 { return 1 })

	if len(events) != 1 {
		t.Fatalf("expected one event, got %#v", events)
	}
	if events[0].Type != "reasoning.snapshot" {
		t.Fatalf("expected reasoning.snapshot, got %#v", events[0])
	}
	if got := events[0].Payload["reasoningLabel"]; got != stream.ReasoningLabelForID(runID+"_r_2") {
		t.Fatalf("expected reasoningLabel in storedMessageToEvents, got %#v", events[0].Payload)
	}
}

func TestStoredMessageToEventsPreservesTimestamp(t *testing.T) {
	const ts int64 = 12345

	testCases := []struct {
		name     string
		msg      map[string]any
		wantType string
	}{
		{
			name: "reasoning snapshot",
			msg: map[string]any{
				"role":              "assistant",
				"ts":                ts,
				"_reasoningId":      "run_1_r_1",
				"reasoning_content": []any{map[string]any{"type": "text", "text": "thinking"}},
			},
			wantType: "reasoning.snapshot",
		},
		{
			name: "content snapshot",
			msg: map[string]any{
				"role":       "assistant",
				"ts":         ts,
				"_contentId": "run_1_c_1",
				"content":    []any{map[string]any{"type": "text", "text": "answer"}},
			},
			wantType: "content.snapshot",
		},
		{
			name: "action snapshot",
			msg: map[string]any{
				"role":      "assistant",
				"ts":        ts,
				"_actionId": "stored-action",
				"tool_calls": []any{
					map[string]any{
						"id": "action-call-1",
						"function": map[string]any{
							"name":      "approval_action",
							"arguments": "{\"approved\":true}",
						},
					},
				},
			},
			wantType: "action.snapshot",
		},
		{
			name: "tool snapshot",
			msg: map[string]any{
				"role":    "assistant",
				"ts":      ts,
				"_toolId": "stored-tool",
				"tool_calls": []any{
					map[string]any{
						"id": "tool-call-1",
						"function": map[string]any{
							"name":      "datetime",
							"arguments": "{}",
						},
					},
				},
			},
			wantType: "tool.snapshot",
		},
		{
			name: "action result",
			msg: map[string]any{
				"role":         "tool",
				"ts":           ts,
				"_actionId":    "stored-action",
				"tool_call_id": "action-call-1",
				"content":      "approved",
			},
			wantType: "action.result",
		},
		{
			name: "tool result",
			msg: map[string]any{
				"role":         "tool",
				"ts":           ts,
				"_toolId":      "stored-tool",
				"tool_call_id": "tool-call-1",
				"content":      "ok",
			},
			wantType: "tool.result",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			events := storedMessageToEvents(tc.msg, "run_1", "task_1", "execute", func() int64 { return 1 })
			if len(events) != 1 {
				t.Fatalf("expected one event, got %#v", events)
			}
			if events[0].Type != tc.wantType {
				t.Fatalf("expected %s, got %#v", tc.wantType, events[0])
			}
			if events[0].Timestamp != ts {
				t.Fatalf("expected timestamp %d, got %#v", ts, events[0])
			}
		})
	}
}

func TestLoadChatSynthesizesRunBoundaryTimestamps(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-ts", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-ts", QueryLine{
		ChatID:    "chat-ts",
		RunID:     "run-ts",
		UpdatedAt: 1001,
		Query: map[string]any{
			"chatId":  "chat-ts",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendStepLine("chat-ts", StepLine{
		ChatID:    "chat-ts",
		RunID:     "run-ts",
		UpdatedAt: 1002,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:      "assistant",
				Content:   textContent("answer"),
				ContentID: "run-ts_c_1",
				MsgID:     "msg-1",
				Ts:        func() *int64 { v := int64(2002); return &v }(),
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-ts")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	var runStart, runComplete *stream.EventData
	for i := range detail.Events {
		switch detail.Events[i].Type {
		case "run.start":
			runStart = &detail.Events[i]
		case "run.complete":
			runComplete = &detail.Events[i]
		}
	}
	if runStart == nil || runComplete == nil {
		t.Fatalf("expected synthesized run boundaries, got %#v", detail.Events)
	}
	if runStart.Timestamp != 1001 {
		t.Fatalf("expected run.start timestamp 1001, got %#v", runStart)
	}
	if runComplete.Timestamp != 2002 {
		t.Fatalf("expected run.complete timestamp 2002, got %#v", runComplete)
	}
}

func TestLoadChatSynthesizedRunStartContainsAgentKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-ak", "my-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-ak", QueryLine{
		ChatID:    "chat-ak",
		RunID:     "run-ak",
		UpdatedAt: 1001,
		Query: map[string]any{
			"chatId":  "chat-ak",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendStepLine("chat-ak", StepLine{
		ChatID:    "chat-ak",
		RunID:     "run-ak",
		UpdatedAt: 1002,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:      "assistant",
				Content:   textContent("answer"),
				ContentID: "run-ak_c_1",
				MsgID:     "msg-1",
				Ts:        func() *int64 { v := int64(2002); return &v }(),
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-ak")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	var runStart *stream.EventData
	for i := range detail.Events {
		if detail.Events[i].Type == "run.start" {
			runStart = &detail.Events[i]
			break
		}
	}
	if runStart == nil {
		t.Fatalf("expected synthesized run.start, got %#v", detail.Events)
	}
	if runStart.String("runId") != "run-ak" {
		t.Fatalf("expected run.start runId run-ak, got %#v", runStart)
	}
	if runStart.String("chatId") != "chat-ak" {
		t.Fatalf("expected run.start chatId chat-ak, got %#v", runStart)
	}
	if runStart.String("agentKey") != "my-agent" {
		t.Fatalf("expected run.start agentKey my-agent, got %#v", runStart)
	}
}

func TestStepWriterActionSnapshotPersistsTsAndReplaysTimestamp(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-action-ts", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-action-ts", "run-action-ts", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "action.snapshot",
		Timestamp: 3456,
		Payload: map[string]any{
			"actionId":   "action-1",
			"actionName": "approval_action",
			"arguments":  "{\"approved\":true}",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-action-ts"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one persisted line, got %#v", lines)
	}

	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one persisted message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if got := int64FromAny(msg["ts"]); got != 3456 {
		t.Fatalf("expected persisted ts=3456, got %#v", msg)
	}

	detail, err := store.LoadChat("chat-action-ts")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	found := false
	for _, event := range detail.Events {
		if event.Type == "action.snapshot" {
			found = true
			if event.Timestamp != 3456 {
				t.Fatalf("expected replayed action.snapshot timestamp 3456, got %#v", event)
			}
		}
	}
	if !found {
		t.Fatalf("expected action.snapshot in replayed events, got %#v", detail.Events)
	}
}

func TestStepWriterEmbedsAwaitingInStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-awaiting-step", "run-awaiting-step", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "ask_user",
			"arguments": "{\"question\":\"How many?\"}",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1002,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
			"runId":      "run-awaiting-step",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"type":     "number",
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "request.submit",
		Timestamp: 1004,
		Payload: map[string]any{
			"requestId":  "req-1",
			"chatId":     "chat-awaiting-step",
			"runId":      "run-awaiting-step",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-awaiting-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected one step line and one submit line, got %#v", lines)
	}

	if got := lines[0]["_type"]; got != "react" {
		t.Fatalf("expected first persisted line to be step, got %#v", lines[0])
	}
	awaiting, _ := lines[0]["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected embedded awaiting events on step line, got %#v", lines[0])
	}
	for _, raw := range awaiting {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if _, ok := item["seq"]; ok {
			t.Fatalf("did not expect seq on embedded awaiting item, got %#v", item)
		}
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one tool snapshot message, got %#v", lines[0])
	}

	if got := lines[1]["_type"]; got != "submit" {
		t.Fatalf("expected second persisted line to be submit, got %#v", lines[1])
	}
	submit, _ := lines[1]["submit"].(map[string]any)
	if submit == nil || submit["type"] != "request.submit" {
		t.Fatalf("expected request.submit submit line, got %#v", lines[1])
	}
	if _, ok := lines[1]["answer"]; ok {
		t.Fatalf("did not expect answer on submit-only line, got %#v", lines[1])
	}
}

func TestStepWriterMergesSubmitAndAnswer(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-submit-merge", "run-submit-merge", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "request.submit",
		Timestamp: 1001,
		Payload: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-submit-merge",
			"runId":      "run-submit-merge",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.answer",
		Seq:       34,
		Timestamp: 1002,
		Payload: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"questions": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-submit-merge"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one merged submit line, got %#v", lines)
	}
	if got := lines[0]["_type"]; got != "submit" {
		t.Fatalf("expected submit line, got %#v", lines[0])
	}
	submit, _ := lines[0]["submit"].(map[string]any)
	answer, _ := lines[0]["answer"].(map[string]any)
	if submit == nil || submit["type"] != "request.submit" {
		t.Fatalf("expected merged submit payload, got %#v", lines[0])
	}
	if answer == nil || answer["type"] != "awaiting.answer" {
		t.Fatalf("expected merged answer payload, got %#v", lines[0])
	}
	if _, ok := answer["seq"]; ok {
		t.Fatalf("did not expect seq on merged answer payload, got %#v", answer)
	}
}

func TestStepWriterTimeoutAnswerDoesNotSplitToolStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-timeout-submit", "run-timeout-submit", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"mock create-leave"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1002,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "approval",
			"timeout":    120000,
			"runId":      "run-timeout-submit",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.answer",
		Seq:       12,
		Timestamp: 1003,
		Payload: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "approval",
			"status":     "error",
			"error": map[string]any{
				"code": "timeout",
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 1004,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": map[string]any{
				"error": "hitl_timeout",
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-timeout-submit"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected one step line and one submit line, got %#v", lines)
	}
	var stepLine map[string]any
	var submitLine map[string]any
	for _, line := range lines {
		switch line["_type"] {
		case "react":
			stepLine = line
		case "submit":
			submitLine = line
		}
	}
	if stepLine == nil || submitLine == nil {
		t.Fatalf("expected both step and submit lines, got %#v", lines)
	}
	if toIntValue(stepLine["seq"]) != 1 {
		t.Fatalf("expected timeout tool step seq=1, got %#v", stepLine)
	}
	messages, _ := stepLine["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected tool snapshot and tool result in same step line, got %#v", stepLine)
	}
	answer, _ := submitLine["answer"].(map[string]any)
	if answer == nil || answer["type"] != "awaiting.answer" {
		t.Fatalf("expected awaiting.answer on submit line, got %#v", submitLine)
	}
	if _, ok := answer["seq"]; ok {
		t.Fatalf("did not expect seq on submit answer payload, got %#v", answer)
	}
}

func TestStepWriterReusesReactSeqForSplitHITLToolResult(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-hitl-seq", "run-hitl-seq", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "debug.preCall",
		Timestamp: 1000,
		Payload: map[string]any{
			"data": map[string]any{
				"usage": map[string]any{},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"git push origin main"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1002,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "approval",
			"timeout":    120000,
			"runId":      "run-hitl-seq",
		},
	})
	writer.RecordApproval(StepApproval{
		Summary: `[HITL] git push origin main -> approve`,
		Notice:  `[System audit - HITL approval batch]`,
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "git push origin main",
			Decision: "approve",
			RuleKey:  "git::push",
		}},
	})
	writer.OnEvent(stream.EventData{
		Type:      "request.submit",
		Timestamp: 1003,
		Payload: map[string]any{
			"requestId":  "req-1",
			"chatId":     "chat-hitl-seq",
			"runId":      "run-hitl-seq",
			"awaitingId": "tool-1",
			"params":     []any{map[string]any{"id": "tool-1", "decision": "approve"}},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.answer",
		Timestamp: 1004,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "approval",
			"status":     "answered",
			"approvals":  []any{map[string]any{"id": "tool-1", "decision": "approve"}},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 1005,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": "pushed",
		},
	})
	writer.OnStageMarker("react-step-2")
	writer.OnEvent(stream.EventData{
		Type:      "debug.preCall",
		Timestamp: 1007,
		Payload: map[string]any{
			"data": map[string]any{
				"usage": map[string]any{},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1008,
		Payload: map[string]any{
			"contentId": "run-hitl-seq_c_2",
			"text":      "done",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-hitl-seq"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("expected assistant step, submit line, tool step, final assistant step; got %#v", lines)
	}
	firstStep := lines[0]
	if firstStep["_type"] != "react" || toIntValue(firstStep["seq"]) != 1 {
		t.Fatalf("expected first react step seq=1, got %#v", firstStep)
	}
	if _, ok := firstStep["approval"]; ok {
		t.Fatalf("did not expect approval on assistant tool-call step, got %#v", firstStep)
	}
	toolStep := lines[2]
	if toolStep["_type"] != "react" || toIntValue(toolStep["seq"]) != 1 {
		t.Fatalf("expected split tool result step to reuse seq=1, got %#v", toolStep)
	}
	if _, ok := toolStep["approval"]; ok {
		t.Fatalf("did not expect approval sidecar on tool result step, got %#v", toolStep)
	}
	toolMessages, _ := toolStep["messages"].([]any)
	if len(toolMessages) != 2 {
		t.Fatalf("expected tool message followed by inline approval message, got %#v", toolStep)
	}
	toolMessage, _ := toolMessages[0].(map[string]any)
	if toolMessage["role"] != "tool" {
		t.Fatalf("expected split step role=tool, got %#v", toolMessage)
	}
	approvalMessage, _ := toolMessages[1].(map[string]any)
	if approvalMessage["role"] != "user" {
		t.Fatalf("expected inline approval user message, got %#v", approvalMessage)
	}
	approval, ok := approvalMessage["approval"].(map[string]any)
	if !ok || approval["summary"] != `[HITL] git push origin main -> approve` {
		t.Fatalf("expected inline approval metadata, got %#v", approvalMessage)
	}
	finalStep := lines[3]
	if finalStep["_type"] != "react" || toIntValue(finalStep["seq"]) != 2 {
		t.Fatalf("expected next model step seq=2, got %#v", finalStep)
	}
}

func TestStepWriterFormatsStructuredToolResultAsJSON(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-tool-result-json", "run-tool-result-json", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"echo hi"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": map[string]any{
				"error":    "hitl_timeout",
				"exitCode": -1,
				"output":   "hitl_timeout: command execution timed out while waiting for user approval",
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-tool-result-json"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if lines[0]["_type"] != "react" || toIntValue(lines[0]["seq"]) != 1 {
		t.Fatalf("expected unsplit tool call step seq=1, got %#v", lines[0])
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected tool snapshot and tool result messages, got %#v", lines[0])
	}
	resultMsg, _ := messages[1].(map[string]any)
	content, _ := resultMsg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content item, got %#v", resultMsg)
	}
	textPart, _ := content[0].(map[string]any)
	text, _ := textPart["text"].(string)
	if strings.Contains(text, "map[") {
		t.Fatalf("expected JSON tool result text, got %q", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("expected JSON tool result text, got %q err=%v", text, err)
	}
	if decoded["error"] != "hitl_timeout" {
		t.Fatalf("unexpected decoded tool result %#v", decoded)
	}
	if decoded["output"] != "hitl_timeout: command execution timed out while waiting for user approval" {
		t.Fatalf("unexpected decoded tool output %#v", decoded)
	}
}

func TestStepWriterEmbedsUsageAtStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-usage-step", "run-usage-step", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 2001,
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "debug.postCall",
		Timestamp: 2002,
		Payload: map[string]any{
			"data": map[string]any{
				"contextWindow": map[string]any{
					"maxSize":       128000,
					"estimatedSize": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
						"promptTokensDetails": map[string]any{
							"cacheHitTokens":  64,
							"cacheMissTokens": 36,
						},
						"completionTokensDetails": map[string]any{
							"reasoningTokens": 12,
						},
						"llmChatCompletionCount": 1,
					},
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete", Timestamp: 2003})

	lines, err := readJSONLines(store.chatJSONLPath("chat-usage-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}

	usage, _ := lines[0]["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected step-level usage, got %#v", lines[0])
	}
	promptDetails, _ := usage["promptTokensDetails"].(map[string]any)
	completionDetails, _ := usage["completionTokensDetails"].(map[string]any)
	if toIntValue(promptDetails["cacheHitTokens"]) != 64 || toIntValue(promptDetails["cacheMissTokens"]) != 36 ||
		toIntValue(completionDetails["reasoningTokens"]) != 12 ||
		toIntValue(usage["llmChatCompletionCount"]) != 1 {
		t.Fatalf("expected detailed step-level usage, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["actualSize"]) != 100 || toIntValue(contextWindow["estimatedSize"]) != 200 {
		t.Fatalf("expected step-level context window, got %#v", lines[0])
	}

	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one stored message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if _, ok := msg["_usage"]; ok {
		t.Fatalf("did not expect message-level _usage in new format, got %#v", msg)
	}
	if _, ok := msg["_contextWindow"]; ok {
		t.Fatalf("did not expect message-level _contextWindow in new format, got %#v", msg)
	}
}

func TestStepWriterPersistsSystemRefWithoutDebugPayload(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-system-snapshot", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-system-snapshot", "run-system-snapshot", "react", false, WithDebugEventsEnabled(true))
	systemRef := map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:first"}
	requestBody := map[string]any{
		"model": "gpt-5.2",
		"messages": []any{
			map[string]any{"role": "system", "content": "你是一个有用的助手"},
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "bash",
				"description": "run shell command",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		"stream": true,
	}

	emitDebugPreCall := func(request map[string]any) {
		writer.OnEvent(stream.EventData{
			Type: "debug.preCall",
			Payload: map[string]any{
				"data": map[string]any{
					"provider":  map[string]any{"key": "mock"},
					"systemRef": systemRef,
					"contextWindow": map[string]any{
						"maxSize":       128000,
						"estimatedSize": 200,
					},
					"usage": map[string]any{
						"runUsage": map[string]any{
							"promptTokens":     1,
							"completionTokens": 2,
							"totalTokens":      3,
						},
					},
					"requestBody": request,
				},
			},
		})
	}
	emitContent := func(contentID string, text string) {
		writer.OnEvent(stream.EventData{
			Type: "content.snapshot",
			Payload: map[string]any{
				"contentId": contentID,
				"text":      text,
			},
		})
	}

	emitDebugPreCall(requestBody)
	emitContent("content-1", "first")
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-system-snapshot"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}

	if _, ok := lines[0]["debug"]; ok {
		t.Fatalf("did not expect debug payload in chat jsonl, got %#v", lines[0])
	}
	if _, ok := lines[0]["system"]; ok {
		t.Fatalf("did not expect full system snapshot on step, got %#v", lines[0])
	}
	gotSystemRef, _ := lines[0]["systemRef"].(map[string]any)
	if gotSystemRef["cacheKey"] != "react:main" || gotSystemRef["fingerprint"] != "sha256:first" {
		t.Fatalf("expected systemRef on step, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["estimatedSize"]) != 200 {
		t.Fatalf("expected contextWindow on step, got %#v", lines[0])
	}

	detail, err := store.LoadChat("chat-system-snapshot")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "debug.preCall" || event.Type == "debug.postCall" {
			t.Fatalf("did not expect debug events in chat history, got %#v", detail.Events)
		}
	}
}

func TestStepWriterOmitsPreCallDebugWhenDebugEventsDisabled(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-debug-disabled", "run-debug-disabled", "react", false)
	writer.OnEvent(stream.EventData{
		Type: "debug.preCall",
		Payload: map[string]any{
			"data": map[string]any{
				"provider":    map[string]any{"key": "mock"},
				"requestBody": map[string]any{"model": "gpt-5.2"},
				"systemRef":   map[string]any{"cacheKey": "react:main"},
				"contextWindow": map[string]any{
					"maxSize":       128000,
					"estimatedSize": 200,
				},
				"usage": map[string]any{
					"runUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 0,
						"totalTokens":      100,
					},
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "debug.postCall",
		Payload: map[string]any{
			"data": map[string]any{
				"contextWindow": map[string]any{
					"maxSize":       128000,
					"estimatedSize": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
					},
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-debug-disabled"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["debug"]; ok {
		t.Fatalf("did not expect debug payload when debug events are disabled, got %#v", lines[0])
	}
	if _, ok := lines[0]["systemRef"].(map[string]any); !ok {
		t.Fatalf("expected non-debug systemRef to remain, got %#v", lines[0])
	}
	usage, _ := lines[0]["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected usage to remain, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["actualSize"]) != 100 || toIntValue(contextWindow["estimatedSize"]) != 200 {
		t.Fatalf("expected context window to remain, got %#v", lines[0])
	}
}

func TestStepWriterPersistsUsageSnapshotWhenDebugEventsDisabled(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-usage-snapshot", "run-usage-snapshot", "react", false)
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"contextWindow": map[string]any{
				"maxSize":               128000,
				"currentSize":           100,
				"estimatedNextCallSize": 200,
			},
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     100,
					"completionTokens": 50,
					"totalTokens":      150,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  64,
						"cacheMissTokens": 36,
					},
					"llmChatCompletionCount": 1,
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-usage-snapshot"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["debug"]; ok {
		t.Fatalf("did not expect debug payload, got %#v", lines[0])
	}
	usage, _ := lines[0]["usage"].(map[string]any)
	promptDetails, _ := usage["promptTokensDetails"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["completionTokens"]) != 50 || toIntValue(usage["totalTokens"]) != 150 ||
		toIntValue(promptDetails["cacheHitTokens"]) != 64 || toIntValue(promptDetails["cacheMissTokens"]) != 36 {
		t.Fatalf("expected usage snapshot to persist, got %#v", lines[0])
	}
	if _, exists := usage["llmChatCompletionCount"]; exists {
		t.Fatalf("did not expect persisted usage snapshot llmChatCompletionCount, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["actualSize"]) != 100 || toIntValue(contextWindow["estimatedSize"]) != 200 {
		t.Fatalf("expected context window to persist, got %#v", lines[0])
	}
}

func TestStepWriterIgnoresEmptyUsageSnapshot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-empty-usage-snapshot", "run-empty-usage-snapshot", "react", false)
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"contextWindow": map[string]any{
				"maxSize":               128000,
				"currentSize":           0,
				"estimatedNextCallSize": 5703,
			},
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":           0,
					"completionTokens":       0,
					"totalTokens":            0,
					"llmChatCompletionCount": 1,
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-empty-usage-snapshot"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["usage"]; ok {
		t.Fatalf("did not expect empty usage snapshot to persist usage, got %#v", lines[0])
	}
	if _, ok := lines[0]["contextWindow"]; ok {
		t.Fatalf("did not expect empty usage snapshot to persist context window, got %#v", lines[0])
	}
}

func TestStepWriterPlanningLifecyclePersistsSnapshotByDefault(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-planning-snapshot", "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, "chat-planning-snapshot", "run-planning", "coder", false)
	emitPlanningLifecycleForTest(writer, "chat-planning-snapshot")

	lines, err := readJSONLines(store.chatJSONLPath("chat-planning-snapshot"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one planning snapshot line, got %#v", lines)
	}
	event, _ := lines[0]["event"].(map[string]any)
	if lines[0]["_type"] != "planning" || event["type"] != "planning.snapshot" {
		t.Fatalf("expected planning.snapshot line, got %#v", lines[0])
	}
	if event["text"] != "# Plan\n\nBody" || event["planningId"] != "plan-run-planning" || event["planningFile"] != "plan-run-planning.md" {
		t.Fatalf("unexpected planning snapshot event %#v", event)
	}
	if _, ok := event["markdown"]; ok {
		t.Fatalf("did not expect public planning snapshot markdown field %#v", event)
	}

	detail, err := store.LoadChat("chat-planning-snapshot")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Planning == nil || detail.Planning.Markdown != "# Plan\n\nBody" {
		t.Fatalf("expected latest planning state from snapshot, got %#v", detail.Planning)
	}
	if !detailHasEventType(detail.Events, "planning.snapshot") ||
		detailHasEventType(detail.Events, "planning.start") ||
		detailHasEventType(detail.Events, "planning.delta") ||
		detailHasEventType(detail.Events, "planning.end") {
		t.Fatalf("unexpected replayed planning events %#v", detail.Events)
	}
}

func TestStepWriterPlanningLifecyclePersistsOnlySnapshotWhenDebugEventsEnabled(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	writer := NewStepWriter(store, "chat-planning-debug", "run-planning", "coder", false, WithDebugEventsEnabled(true))
	emitPlanningLifecycleForTest(writer, "chat-planning-debug")

	lines, err := readJSONLines(store.chatJSONLPath("chat-planning-debug"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		event, _ := line["event"].(map[string]any)
		got = append(got, stringVal(event["type"]))
	}
	want := []string{"planning.snapshot"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("planning event lines = %#v, want %#v", got, want)
	}
}

func TestLoadChatReplaysSinglePlanningSnapshotFromLegacyLifecycleEvents(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-legacy-planning"
	runID := "run-planning"
	if _, _, err := store.EnsureChat(chatID, "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	appendPlanningEventLineForTest(t, store, chatID, runID, 1001, map[string]any{
		"type":         "planning.start",
		"timestamp":    int64(1001),
		"planningId":   "plan-run-planning",
		"planningFile": "plan-run-planning.md",
		"chatId":       chatID,
		"runId":        runID,
		"title":        "Plan",
		"updatedAt":    int64(1001),
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1002, map[string]any{
		"type":       "planning.delta",
		"timestamp":  int64(1002),
		"planningId": "plan-run-planning",
		"delta":      "# Draft",
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1003, map[string]any{
		"type":       "planning.end",
		"timestamp":  int64(1003),
		"planningId": "plan-run-planning",
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1004, map[string]any{
		"type":         "planning.snapshot",
		"timestamp":    int64(1004),
		"planningId":   "plan-run-planning",
		"planningFile": "plan-run-planning.md",
		"chatId":       chatID,
		"runId":        runID,
		"title":        "Plan",
		"markdown":     "# Final\n\nBody",
		"updatedAt":    int64(1004),
	})

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if got := detailEventTypeCount(detail.Events, "planning.snapshot"); got != 1 {
		t.Fatalf("planning.snapshot count = %d, events %#v", got, detail.Events)
	}
	if detailHasEventType(detail.Events, "planning.start") ||
		detailHasEventType(detail.Events, "planning.delta") ||
		detailHasEventType(detail.Events, "planning.end") {
		t.Fatalf("unexpected replayed planning lifecycle events %#v", detail.Events)
	}
	snapshot := detailEventByType(detail.Events, "planning.snapshot")
	if snapshot.String("text") != "# Final\n\nBody" {
		t.Fatalf("expected snapshot text to prefer canonical snapshot, got %#v", snapshot.Map())
	}
	if snapshot.Value("markdown") != nil {
		t.Fatalf("did not expect replayed planning snapshot markdown, got %#v", snapshot.Map())
	}
	if detail.Planning == nil || detail.Planning.Markdown != "# Final\n\nBody" {
		t.Fatalf("expected planning state from canonical snapshot, got %#v", detail.Planning)
	}
}

func TestLoadChatSynthesizesPlanningSnapshotFromLegacyDeltas(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-legacy-planning-deltas"
	runID := "run-planning"
	if _, _, err := store.EnsureChat(chatID, "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	appendPlanningEventLineForTest(t, store, chatID, runID, 1001, map[string]any{
		"type":         "planning.start",
		"timestamp":    int64(1001),
		"planningId":   "plan-run-planning",
		"planningFile": "plan-run-planning.md",
		"chatId":       chatID,
		"runId":        runID,
		"title":        "Plan",
		"updatedAt":    int64(1001),
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1002, map[string]any{
		"type":       "planning.delta",
		"timestamp":  int64(1002),
		"planningId": "plan-run-planning",
		"delta":      "# Draft",
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1003, map[string]any{
		"type":       "planning.delta",
		"timestamp":  int64(1003),
		"planningId": "plan-run-planning",
		"delta":      "\n\nBody",
	})
	appendPlanningEventLineForTest(t, store, chatID, runID, 1004, map[string]any{
		"type":       "planning.end",
		"timestamp":  int64(1004),
		"planningId": "plan-run-planning",
	})

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if got := detailEventTypeCount(detail.Events, "planning.snapshot"); got != 1 {
		t.Fatalf("planning.snapshot count = %d, events %#v", got, detail.Events)
	}
	snapshot := detailEventByType(detail.Events, "planning.snapshot")
	if snapshot.String("text") != "# Draft\n\nBody" {
		t.Fatalf("expected synthesized snapshot text from deltas, got %#v", snapshot.Map())
	}
	if snapshot.Value("markdown") != nil {
		t.Fatalf("did not expect synthesized planning snapshot markdown, got %#v", snapshot.Map())
	}
	if detail.Planning == nil || detail.Planning.Markdown != "# Draft\n\nBody" {
		t.Fatalf("expected planning state from synthesized snapshot, got %#v", detail.Planning)
	}
}

func emitPlanningLifecycleForTest(writer *StepWriter, chatID string) {
	writer.OnEvent(stream.EventData{
		Type:      "planning.start",
		Timestamp: 1001,
		Payload: map[string]any{
			"planningId":   "plan-run-planning",
			"planningFile": "plan-run-planning.md",
			"chatId":       chatID,
			"runId":        "run-planning",
			"title":        "Plan",
			"updatedAt":    int64(1001),
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "planning.delta",
		Timestamp: 1002,
		Payload: map[string]any{
			"planningId": "plan-run-planning",
			"delta":      "# Plan\n\nBody",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "planning.end",
		Timestamp: 1003,
		Payload: map[string]any{
			"planningId": "plan-run-planning",
		},
	})
}

func appendPlanningEventLineForTest(t *testing.T, store *FileStore, chatID string, runID string, updatedAt int64, event map[string]any) {
	t.Helper()
	if err := store.AppendEventLine(chatID, EventLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: updatedAt,
		Event:     event,
		Type:      "planning",
	}); err != nil {
		t.Fatalf("append planning event: %v", err)
	}
}

func detailHasEventType(events []stream.EventData, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func detailEventTypeCount(events []stream.EventData, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func detailEventByType(events []stream.EventData, eventType string) stream.EventData {
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	return stream.EventData{}
}

func TestStepWriterPersistsTaskScopedUsageAndSlimMetadataWithoutDebugPayload(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-task-debug", "run-task-debug", "react", false, WithDebugEventsEnabled(true))
	writer.OnEvent(stream.EventData{
		Type: "task.start",
		Payload: map[string]any{
			"taskId":         "task_1",
			"taskName":       "分析",
			"description":    "run analysis",
			"subAgentKey":    "analyzer",
			"invokingToolId": "tool_main_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "debug.preCall",
		Payload: map[string]any{
			"taskId": "task_1",
			"data": map[string]any{
				"provider":  map[string]any{"key": "mock"},
				"systemRef": map[string]any{"cacheKey": "react:analyzer", "fingerprint": "sha256:child"},
				"requestBody": map[string]any{
					"model":    "gpt-5.2",
					"messages": []any{map[string]any{"role": "user", "content": "secret"}},
					"tools":    []any{map[string]any{"name": "bash"}},
					"stream":   true,
				},
				"contextWindow": map[string]any{
					"maxSize":       128000,
					"estimatedSize": 200,
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"taskId":    "task_1",
			"contentId": "child_1",
			"text":      "child result",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "debug.postCall",
		Payload: map[string]any{
			"taskId": "task_1",
			"data": map[string]any{
				"contextWindow": map[string]any{
					"maxSize":       128000,
					"estimatedSize": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
					},
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "task.complete",
		Payload: map[string]any{
			"taskId": "task_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "root_1",
			"text":      "root result",
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-task-debug"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected task and root step lines, got %#v", lines)
	}
	taskLine := lines[0]
	if taskLine["taskId"] != "task_1" || taskLine["taskStatus"] != "completed" || taskLine["taskSubAgentKey"] != "analyzer" {
		t.Fatalf("expected slim task metadata, got %#v", taskLine)
	}
	for _, field := range []string{"taskName", "taskDescription", "taskToolId"} {
		if _, ok := taskLine[field]; ok {
			t.Fatalf("did not expect %s on task react line: %#v", field, taskLine)
		}
	}
	if _, ok := taskLine["debug"]; ok {
		t.Fatalf("did not expect task debug payload in chat jsonl, got %#v", taskLine)
	}
	systemRef, _ := taskLine["systemRef"].(map[string]any)
	if systemRef["cacheKey"] != "react:analyzer" || systemRef["fingerprint"] != "sha256:child" {
		t.Fatalf("expected task systemRef, got %#v", taskLine)
	}
	usage, _ := taskLine["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected task usage, got %#v", taskLine)
	}
	contextWindow, _ := taskLine["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["actualSize"]) != 100 || toIntValue(contextWindow["estimatedSize"]) != 200 {
		t.Fatalf("expected task contextWindow, got %#v", taskLine)
	}
	if _, ok := lines[1]["debug"]; ok {
		t.Fatalf("did not expect child debug to pollute root step, got %#v", lines[1])
	}
}

func TestStepWriterSubTaskReactFlushOrder(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-task-order", "run-task-order", "react", false)
	writer.OnEvent(stream.EventData{
		Type: "tool.snapshot",
		Payload: map[string]any{
			"toolId":    "tool_main_1",
			"toolName":  "agent_invoke",
			"arguments": `{"tasks":[]}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "task.start",
		Payload: map[string]any{
			"taskId":      "task_1",
			"subAgentKey": "analyzer",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"taskId":    "task_1",
			"contentId": "child_1",
			"text":      "child done",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "task.complete",
		Payload: map[string]any{
			"taskId": "task_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "tool.result",
		Payload: map[string]any{
			"toolId": "tool_main_1",
			"result": "aggregate",
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-task-order"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected root tool, task, root result lines, got %#v", lines)
	}
	if _, ok := lines[0]["taskId"]; ok {
		t.Fatalf("expected first line to be main tool snapshot, got %#v", lines[0])
	}
	if lines[1]["taskId"] != "task_1" {
		t.Fatalf("expected second line to be child task react, got %#v", lines)
	}
	if _, ok := lines[2]["taskId"]; ok {
		t.Fatalf("expected third line to be main aggregate tool.result, got %#v", lines[2])
	}
}

func TestQueryLineTaskToolIDFieldName(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if err := store.AppendQueryLine("chat-task-query", QueryLine{
		ChatID:      "chat-task-query",
		RunID:       "run-task-query",
		UpdatedAt:   1001,
		TaskID:      "task_1",
		TaskName:    "分析",
		TaskToolID:  "tool_main_1",
		SubAgentKey: "analyzer",
		Query: map[string]any{
			"message":   "run analysis",
			"agentKey":  "analyzer",
			"requestId": "sub_1",
			"role":      "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath("chat-task-query"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one query line, got %#v", lines)
	}
	line := lines[0]
	if line["taskId"] != "task_1" || line["taskName"] != "分析" || line["taskToolId"] != "tool_main_1" || line["subAgentKey"] != "analyzer" {
		t.Fatalf("expected sub-agent query metadata, got %#v", line)
	}
	if _, ok := line["taskGroupId"]; ok {
		t.Fatalf("did not expect taskGroupId on sub-agent query, got %#v", line)
	}
	if _, ok := line["taskMainToolId"]; ok {
		t.Fatalf("did not expect taskMainToolId on sub-agent query, got %#v", line)
	}
}

func TestLoadRunTraceKeepsMainQueryWhenSubTaskQueriesExist(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-run-trace-query", "agent", "", "main prompt"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-run-trace-query", QueryLine{
		ChatID:    "chat-run-trace-query",
		RunID:     "run_1",
		UpdatedAt: 1001,
		Query: map[string]any{
			"message": "main prompt",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append main query line: %v", err)
	}
	if err := store.AppendQueryLine("chat-run-trace-query", QueryLine{
		ChatID:      "chat-run-trace-query",
		RunID:       "run_1",
		UpdatedAt:   1002,
		TaskID:      "task_1",
		SubAgentKey: "analyzer",
		Query: map[string]any{
			"message":  "child prompt",
			"agentKey": "analyzer",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append child query line: %v", err)
	}

	trace, err := store.LoadRunTrace("chat-run-trace-query", "run_1")
	if err != nil {
		t.Fatalf("load run trace: %v", err)
	}
	if trace.Query == nil || trace.Query.TaskID != "" || trace.Query.Query["message"] != "main prompt" {
		t.Fatalf("expected run trace to keep main query, got %#v", trace.Query)
	}
}

func TestFileStoreLoadsLatestSystemInitByCacheKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-system-init", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	first := QueryLineSystemInit{
		Fingerprint:   "sha256:first",
		CacheKey:      "react:main",
		SystemMessage: map[string]any{"role": "system", "content": "first"},
		Tools:         []any{map[string]any{"type": "function"}},
	}
	second := first
	second.Fingerprint = "sha256:second"
	second.SystemMessage = map[string]any{"role": "system", "content": "second"}
	other := first
	other.CacheKey = "plan-execute:plan"
	other.Fingerprint = "sha256:other"
	other.SystemMessage = map[string]any{"role": "system", "content": "other"}

	for _, line := range []QueryLine{
		{
			Type:      "query",
			ChatID:    "chat-system-init",
			RunID:     "run-1",
			UpdatedAt: 1,
			Query:     map[string]any{"role": "user", "message": "first"},
			Systems:   []QueryLineSystemInit{first, other},
		},
		{
			Type:      "query",
			ChatID:    "chat-system-init",
			RunID:     "run-2",
			UpdatedAt: 2,
			Query:     map[string]any{"role": "user", "message": "second"},
			Systems:   []QueryLineSystemInit{second},
		},
	} {
		if err := store.AppendQueryLine("chat-system-init", line); err != nil {
			t.Fatalf("append query line: %v", err)
		}
	}

	loaded, err := store.LoadSystemInit("chat-system-init", "react:main")
	if err != nil {
		t.Fatalf("load system init: %v", err)
	}
	if loaded == nil || loaded.Fingerprint != "sha256:second" {
		t.Fatalf("expected latest matching system cache line, got %#v", loaded)
	}
	if loaded.SystemMessage["content"] != "second" {
		t.Fatalf("unexpected system message %#v", loaded.SystemMessage)
	}
	if loaded.ChatID != "chat-system-init" || loaded.RunID != "run-2" || loaded.CreatedAt != 2 {
		t.Fatalf("expected container query metadata on system init, got %#v", loaded)
	}
	if loaded.Mode != "react" || loaded.Stage != "main" {
		t.Fatalf("expected mode/stage parsed from cacheKey, got %#v", loaded)
	}
	all, err := store.LoadAllSystemInits("chat-system-init")
	if err != nil {
		t.Fatalf("load all system inits: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected two cache keys, got %#v", all)
	}
	if got := all["react:main"]; got == nil || got.Fingerprint != "sha256:second" {
		t.Fatalf("expected latest react profile, got %#v", got)
	}
	if got := all["plan-execute:plan"]; got == nil || got.Fingerprint != "sha256:other" {
		t.Fatalf("expected plan profile, got %#v", got)
	}
}

func TestQueryLineSystemInitOmitsModeStageAgentKey(t *testing.T) {
	raw, err := json.Marshal(QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:init",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{},
	})
	if err != nil {
		t.Fatalf("marshal query system init: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal query system init: %v", err)
	}
	for _, field := range []string{"mode", "stage", "agentKey"} {
		if _, ok := got[field]; ok {
			t.Fatalf("did not expect %s in serialized system init: %s", field, raw)
		}
	}
	for _, field := range []string{"cacheKey", "fingerprint", "systemMessage", "tools"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("expected %s in serialized system init: %s", field, raw)
		}
	}
}

func TestLoadSystemInitsParsesCacheKeyToModeStage(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-system-cache-key", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	for _, system := range []QueryLineSystemInit{
		{
			CacheKey:      "react:main",
			Fingerprint:   "sha256:react",
			SystemMessage: map[string]any{"role": "system", "content": "react"},
			Tools:         []any{},
		},
		{
			CacheKey:      "plan-execute:execute",
			Fingerprint:   "sha256:execute",
			SystemMessage: map[string]any{"role": "system", "content": "execute"},
			Tools:         []any{},
		},
	} {
		if err := store.AppendQueryLine("chat-system-cache-key", QueryLine{
			Type:      "query",
			ChatID:    "chat-system-cache-key",
			RunID:     "run-1",
			UpdatedAt: 1,
			Query:     map[string]any{"role": "user", "message": "hello", "agentKey": "agent"},
			Systems:   []QueryLineSystemInit{system},
		}); err != nil {
			t.Fatalf("append query line: %v", err)
		}
	}
	all, err := store.LoadAllSystemInits("chat-system-cache-key")
	if err != nil {
		t.Fatalf("load all system inits: %v", err)
	}
	if got := all["react:main"]; got == nil || got.Mode != "react" || got.Stage != "main" {
		t.Fatalf("expected react:main to parse mode/stage, got %#v", got)
	}
	if got := all["plan-execute:execute"]; got == nil || got.Mode != "plan-execute" || got.Stage != "execute" {
		t.Fatalf("expected plan-execute:execute to parse mode/stage, got %#v", got)
	}
}

func TestFileStoreLoadsLegacySystemInitByCacheKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-legacy-system", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.appendJSONLine(store.chatJSONLPath("chat-legacy-system"), SystemInitLine{
		Type:          "system-init",
		ChatID:        "chat-legacy-system",
		AgentKey:      "agent",
		RunID:         "run-legacy",
		CreatedAt:     1,
		Fingerprint:   "sha256:legacy",
		CacheKey:      "react:main",
		Mode:          "react",
		Stage:         "main",
		SystemMessage: map[string]any{"role": "system", "content": "legacy"},
		Tools:         []any{},
	}); err != nil {
		t.Fatalf("append legacy system cache line: %v", err)
	}

	loaded, err := store.LoadSystemInit("chat-legacy-system", "react:main")
	if err != nil {
		t.Fatalf("load system cache line: %v", err)
	}
	if loaded == nil || loaded.Fingerprint != "sha256:legacy" || loaded.Type != "system-init" {
		t.Fatalf("expected legacy system cache line, got %#v", loaded)
	}
}

func TestRawMessagesSkipSystemInitLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-system-init-raw", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-system-init-raw", QueryLine{
		Type:      "query",
		ChatID:    "chat-system-init-raw",
		RunID:     "run-1",
		UpdatedAt: 2,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Systems: []QueryLineSystemInit{{
			Fingerprint:   "sha256:init",
			CacheKey:      "react:main",
			SystemMessage: map[string]any{"role": "system", "content": "frozen"},
			Tools:         []any{},
		}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	messages, err := store.LoadRawMessages("chat-system-init-raw", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(messages) != 1 || messages[0]["role"] != "user" || messages[0]["content"] != "hello" {
		t.Fatalf("expected only query message, got %#v", messages)
	}
}

func TestStepWriterWritesSystemInitAfterQuery(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-query-system-init", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-query-system-init", "run-1", "react", false)
	writer.SetPendingSystemInits([]QueryLineSystemInit{{
		Fingerprint:   "sha256:first",
		CacheKey:      "react:main",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{map[string]any{"type": "function"}},
	}})
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  "chat-query-system-init",
			"runId":   "run-1",
			"message": "hello",
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath("chat-query-system-init"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one query line with inline systems, got %#v", lines)
	}
	if lines[0]["_type"] != "query" {
		t.Fatalf("expected query line, got %#v", lines)
	}
	systems, _ := lines[0]["systems"].([]any)
	if len(systems) != 1 {
		t.Fatalf("expected inline system cache on query line, got %#v", lines[0])
	}
	system, _ := systems[0].(map[string]any)
	if system["cacheKey"] != "react:main" || system["fingerprint"] != "sha256:first" {
		t.Fatalf("unexpected inline system cache %#v", system)
	}
}

func TestStepWriterPersistsHiddenQueryWithSystemInits(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-hidden-query-system-init", "agent", "", "hidden hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-hidden-query-system-init", "run-hidden", "react", true)
	writer.SetPendingSystemInits([]QueryLineSystemInit{{
		Fingerprint:   "sha256:hidden",
		CacheKey:      "react:main",
		SystemMessage: map[string]any{"role": "system", "content": "hidden system"},
		Tools:         []any{map[string]any{"type": "function"}},
	}})
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  "chat-hidden-query-system-init",
			"runId":   "run-hidden",
			"message": "hidden hello",
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath("chat-hidden-query-system-init"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 || lines[0]["_type"] != "query" {
		t.Fatalf("expected one hidden query line, got %#v", lines)
	}
	if hidden, _ := lines[0]["hidden"].(bool); !hidden {
		t.Fatalf("expected hidden=true on query line, got %#v", lines[0])
	}
	systems, _ := lines[0]["systems"].([]any)
	if len(systems) != 1 {
		t.Fatalf("expected hidden query to keep inline systems, got %#v", lines[0])
	}
	system, _ := systems[0].(map[string]any)
	if system["cacheKey"] != "react:main" || system["fingerprint"] != "sha256:hidden" {
		t.Fatalf("unexpected inline system cache %#v", system)
	}

	detail, err := store.LoadChat("chat-hidden-query-system-init")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	var queryEvent *stream.EventData
	for i := range detail.Events {
		if detail.Events[i].Type == "request.query" {
			queryEvent = &detail.Events[i]
			break
		}
	}
	if queryEvent == nil {
		t.Fatalf("expected replayed request.query, got %#v", detail.Events)
	}
	if hidden, _ := queryEvent.Value("hidden").(bool); !hidden {
		t.Fatalf("expected replayed request.query hidden=true, got %#v", queryEvent)
	}
}

func TestStepWriterOmitsSystemsWhenNoPendingSystemInits(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-query-no-system-init", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-query-no-system-init", "run-1", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  "chat-query-no-system-init",
			"runId":   "run-1",
			"message": "hello",
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath("chat-query-no-system-init"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 || lines[0]["_type"] != "query" {
		t.Fatalf("expected one query line, got %#v", lines)
	}
	if _, ok := lines[0]["systems"]; ok {
		t.Fatalf("did not expect systems on cache-hit/no-pending query, got %#v", lines[0])
	}
}

func TestStepWriterPersistsAwaitingWithoutMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-awaiting-standalone", "run-awaiting-standalone", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 3001,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
		},
	})
	writer.Flush()

	if len(writer.pendingAwaiting) != 0 {
		t.Fatalf("expected pending awaiting to be cleared, got %#v", writer.pendingAwaiting)
	}

	lines, err := readJSONLines(store.chatJSONLPath("chat-awaiting-standalone"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one line for standalone awaiting, got %#v", lines)
	}
	awaiting, _ := lines[0]["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected standalone awaiting on step line, got %#v", lines[0])
	}
	item, _ := awaiting[0].(map[string]any)
	if item["type"] != "awaiting.ask" || item["awaitingId"] != "tool-1" {
		t.Fatalf("unexpected standalone awaiting item %#v", item)
	}
}

func TestStepWriterPersistsInlineApprovalMessage(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-approval-step", "run-approval-step", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 4001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"chmod 777 ~/a.sh"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 4002,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": "",
		},
	})
	writer.RecordApproval(StepApproval{
		Summary: `[HITL] chmod 777 ~/a.sh → approve`,
		Notice:  `[System audit — HITL approval batch]`,
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "chmod 777 ~/a.sh",
			Decision: "approve",
			RuleKey:  "dangerous-commands::chmod",
		}},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-approval-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["approval"]; ok {
		t.Fatalf("did not expect top-level approval sidecar on step line, got %#v", lines[0])
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected tool snapshot, tool result, and approval message, got %#v", lines[0])
	}
	approvalMessage, _ := messages[2].(map[string]any)
	if approvalMessage["role"] != "user" {
		t.Fatalf("expected inline approval user message after tool result, got %#v", approvalMessage)
	}
	if text := extractTextFromContent(approvalMessage["content"]); text != `[System audit — HITL approval batch]` {
		t.Fatalf("expected approval notice as message content, got %#v", approvalMessage)
	}
	approval, ok := approvalMessage["approval"].(map[string]any)
	if !ok {
		t.Fatalf("expected approval metadata on inline user message, got %#v", approvalMessage)
	}
	if _, ok := approval["ruleKey"]; ok {
		t.Fatalf("did not expect approval.ruleKey outside decisions, got %#v", approval)
	}
	if approval["summary"] != `[HITL] chmod 777 ~/a.sh → approve` {
		t.Fatalf("expected approval summary metadata, got %#v", approval)
	}
	if _, ok := approval["notice"]; ok {
		t.Fatalf("did not expect persisted approval notice metadata, got %#v", approval)
	}
}

func TestStepWriterPersistsFormApprovalDecisionPayload(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	payload := map[string]any{
		"applicant_id": "E1001",
		"days":         2.0,
		"leave_type":   "annual",
	}
	writer := NewStepWriter(store, "chat-form-approval-step", "run-form-approval-step", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 4101,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"mock create-leave --payload '{...}'"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 4102,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": "executed",
		},
	})
	writer.RecordApproval(StepApproval{
		Summary: "[HITL] mock create-leave --payload '{...}' → approve\n  提交参数: {\"applicant_id\":\"E1001\",\"days\":2,\"leave_type\":\"annual\"}",
		Notice:  "[System audit — HITL approval batch]\nsubmitted_payload={\"applicant_id\":\"E1001\",\"days\":2,\"leave_type\":\"annual\"}",
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "mock create-leave --payload '{...}'",
			Decision: "approve",
			RuleKey:  "leave::create",
			Mode:     "form",
			Payload:  payload,
		}},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-form-approval-step"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["approval"]; ok {
		t.Fatalf("did not expect top-level approval sidecar on step line, got %#v", lines[0])
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected inline approval message after tool result, got %#v", lines[0])
	}
	approvalMessage, _ := messages[2].(map[string]any)
	if approvalMessage["role"] != "user" {
		t.Fatalf("expected inline approval user message, got %#v", approvalMessage)
	}
	approval, ok := approvalMessage["approval"].(map[string]any)
	if !ok {
		t.Fatalf("expected approval metadata on inline user message, got %#v", approvalMessage)
	}
	if approval["summary"] == "" {
		t.Fatalf("expected persisted form approval summary, got %#v", approval)
	}
	if _, ok := approval["notice"]; ok {
		t.Fatalf("did not expect persisted form approval notice metadata, got %#v", approval)
	}
	decisions, ok := approval["decisions"].([]any)
	if !ok || len(decisions) != 1 {
		t.Fatalf("expected one approval decision, got %#v", approval)
	}
	decision, ok := decisions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected decision object, got %#v", decisions[0])
	}
	if decision["mode"] != "form" {
		t.Fatalf("expected form decision mode, got %#v", decision)
	}
	gotPayload, ok := decision["payload"].(map[string]any)
	if !ok || gotPayload["applicant_id"] != "E1001" || gotPayload["days"] != 2.0 || gotPayload["leave_type"] != "annual" {
		t.Fatalf("expected persisted form payload, got %#v", decision)
	}
}

func TestLoadRawMessagesReplaysInlineApprovalMessage(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval-raw", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	toolTs := int64(5001)
	resultTs := int64(5002)
	approvalTs := int64(5003)
	approval := &StepApproval{
		Summary: `[HITL] chmod 777 ~/a.sh → approve`,
		Notice: `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="chmod 777 ~/a.sh" decision=approve reason=""
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.`,
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "chmod 777 ~/a.sh",
			Decision: "approve",
			RuleKey:  "dangerous-commands::chmod",
		}},
	}
	if err := store.AppendStepLine("chat-approval-raw", StepLine{
		ChatID:    "chat-approval-raw",
		RunID:     "run-approval-raw",
		UpdatedAt: 5003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "bash",
						Arguments: `{"command":"chmod 777 ~/a.sh"}`,
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "bash",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: ""}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
			{
				Role:     "user",
				Content:  textContent(approval.Notice),
				Approval: approval,
				Ts:       &approvalTs,
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	rawMessages, err := store.LoadRawMessages("chat-approval-raw", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 3 {
		t.Fatalf("expected assistant, tool, synthetic user messages, got %#v", rawMessages)
	}
	if rawMessages[1]["role"] != "tool" {
		t.Fatalf("expected tool result before approval summary, got %#v", rawMessages)
	}
	if rawMessages[2]["role"] != "user" || rawMessages[2]["content"] != `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="chmod 777 ~/a.sh" decision=approve reason=""
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.` {
		t.Fatalf("expected approval LLM notice replayed as user raw message, got %#v", rawMessages[2])
	}
	if approvalMeta, ok := rawMessages[2]["approval"].(map[string]any); !ok || approvalMeta["summary"] != `[HITL] chmod 777 ~/a.sh → approve` {
		t.Fatalf("expected raw message to retain approval metadata, got %#v", rawMessages[2])
	}

	detail, err := store.LoadChat("chat-approval-raw")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if len(detail.RawMessages) != 3 {
		t.Fatalf("expected load chat raw messages to include inline approval message, got %#v", detail.RawMessages)
	}
	if detail.RawMessages[2]["role"] != "user" {
		t.Fatalf("expected inline approval message at end of raw messages, got %#v", detail.RawMessages)
	}
}

func TestLoadRawMessagesReplaysAutoApprovalSummaryFromStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-auto-approval-raw", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	toolTs := int64(6001)
	resultTs := int64(6002)
	approvalTs := int64(6003)
	approval := &StepApproval{
		Summary: `[AUTO] file_read /tmp/secret.txt → auto_approved（accessLevel=auto_approve）`,
		Notice: `[System audit — auto approval]
The system auto-approved the following tool call(s) because accessLevel=auto_approve applies automatic approval to reviewable access-policy checks:
1. tool=file_read command="file_read /tmp/secret.txt" decision=auto_approved reason="accessLevel=auto_approve"
The tool results above already reflect these automatic approvals; do not re-prompt for approval.`,
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "file_read /tmp/secret.txt",
			Decision: "auto_approved",
			RuleKey:  "file-read::outside",
			Reason:   "accessLevel=auto_approve",
			Mode:     "approval",
		}},
	}
	if err := store.AppendStepLine("chat-auto-approval-raw", StepLine{
		ChatID:    "chat-auto-approval-raw",
		RunID:     "run-auto-approval-raw",
		UpdatedAt: 6003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "file_read",
						Arguments: `{"file_path":"/tmp/secret.txt"}`,
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "file_read",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "ok"}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
			{
				Role:     "user",
				Content:  textContent(approval.Notice),
				Approval: approval,
				Ts:       &approvalTs,
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	rawMessages, err := store.LoadRawMessages("chat-auto-approval-raw", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 3 {
		t.Fatalf("expected assistant, tool, synthetic user messages, got %#v", rawMessages)
	}
	if rawMessages[2]["role"] != "user" || !strings.Contains(stringValue(rawMessages[2]["content"]), "decision=auto_approved") {
		t.Fatalf("expected auto approval LLM notice replayed as user raw message, got %#v", rawMessages[2])
	}
	approvalMeta, ok := rawMessages[2]["approval"].(map[string]any)
	if !ok {
		t.Fatalf("expected raw auto approval metadata, got %#v", rawMessages[2])
	}
	decisions, _ := approvalMeta["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("expected one raw auto approval decision, got %#v", rawMessages[2])
	}
	decision, _ := decisions[0].(map[string]any)
	if decision["decision"] != "auto_approved" {
		t.Fatalf("expected auto_approved decision metadata, got %#v", rawMessages[2])
	}
}

func TestLoadRawMessagesReplaysSplitApprovalSummaryAfterToolResult(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval-split", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	toolTs := int64(5001)
	resultTs := int64(5002)
	approvalTs := int64(5003)
	approval := &StepApproval{
		Summary: `[HITL] mock create-leave → reject（timeout）`,
		Notice: `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="mock create-leave" decision=reject reason="timeout"
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.`,
		Decisions: []StepApprovalDecision{{
			ToolID:   "tool-1",
			Command:  "mock create-leave",
			Decision: "reject",
			RuleKey:  "leave::create",
		}},
	}
	if err := store.AppendStepLine("chat-approval-split", StepLine{
		ChatID:    "chat-approval-split",
		RunID:     "run-approval-split",
		UpdatedAt: 5003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role: "assistant",
			ToolCalls: []StoredToolCall{{
				ID:   "tool-1",
				Type: "function",
				Function: StoredFunction{
					Name:      "bash",
					Arguments: `{"command":"mock create-leave"}`,
				},
			}},
			ToolID: "tool-1",
			MsgID:  "msg-1",
			Ts:     &toolTs,
		}},
	}); err != nil {
		t.Fatalf("append assistant step line: %v", err)
	}
	if err := store.AppendStepLine("chat-approval-split", StepLine{
		ChatID:    "chat-approval-split",
		RunID:     "run-approval-split",
		UpdatedAt: 5004,
		Type:      "react",
		Seq:       2,
		Messages: []StoredMessage{{
			Role:       "tool",
			Name:       "bash",
			ToolCallID: "tool-1",
			Content:    []ContentPart{{Type: "text", Text: `{"error":"hitl_timeout"}`}},
			ToolID:     "tool-1",
			Ts:         &resultTs,
		}, {
			Role:     "user",
			Content:  textContent(approval.Notice),
			Approval: approval,
			Ts:       &approvalTs,
		}},
	}); err != nil {
		t.Fatalf("append tool result step line: %v", err)
	}

	rawMessages, err := store.LoadRawMessages("chat-approval-split", 10)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 3 {
		t.Fatalf("expected assistant, tool, synthetic user messages, got %#v", rawMessages)
	}
	if rawMessages[0]["role"] != "assistant" || rawMessages[1]["role"] != "tool" || rawMessages[2]["role"] != "user" {
		t.Fatalf("expected assistant -> tool -> user ordering, got %#v", rawMessages)
	}
	if rawMessages[2]["content"] != `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="mock create-leave" decision=reject reason="timeout"
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.` {
		t.Fatalf("expected approval LLM notice at end, got %#v", rawMessages[2])
	}
}

func TestLoadRawMessagesIgnoresLegacyTopLevelApproval(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval-legacy-summary", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.appendJSONLine(store.chatJSONLPath("chat-approval-legacy-summary"), map[string]any{
		"chatId":    "chat-approval-legacy-summary",
		"runId":     "run-approval-legacy-summary",
		"updatedAt": 5003,
		"_type":     "react",
		"seq":       1,
		"messages": []any{map[string]any{
			"role":         "tool",
			"name":         "bash",
			"tool_call_id": "tool-1",
			"content":      []any{map[string]any{"type": "text", "text": "ok"}},
			"_toolId":      "tool-1",
		}},
		"approval": map[string]any{
			"summary": `[HITL] legacy approval`,
		},
	}); err != nil {
		t.Fatalf("append legacy step line: %v", err)
	}

	rawMessages, err := store.LoadRawMessages("chat-approval-legacy-summary", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 1 {
		t.Fatalf("expected only the tool message; legacy top-level approval must be ignored, got %#v", rawMessages)
	}
	if rawMessages[0]["role"] != "tool" || rawMessages[0]["content"] != "ok" {
		t.Fatalf("expected tool message without synthesized approval fallback, got %#v", rawMessages)
	}
}

func TestLoadRawMessagesFlushesApprovalSummaryBeforeNextRun(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval-multirun", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-approval-multirun", QueryLine{
		ChatID:    "chat-approval-multirun",
		RunID:     "run-1",
		UpdatedAt: 1000,
		Query: map[string]any{
			"role":    "user",
			"message": "first",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append first query line: %v", err)
	}
	if err := store.AppendStepLine("chat-approval-multirun", StepLine{
		ChatID:    "chat-approval-multirun",
		RunID:     "run-1",
		UpdatedAt: 1001,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "first reply"}},
			MsgID:   "msg-1",
		}, {
			Role:    "user",
			Content: textContent("[System audit — HITL approval batch]\nfirst approval"),
			Approval: &StepApproval{
				Summary: "[HITL] first approval",
				Notice:  "[System audit — HITL approval batch]\nfirst approval",
			},
		}},
	}); err != nil {
		t.Fatalf("append first step line: %v", err)
	}
	if err := store.AppendQueryLine("chat-approval-multirun", QueryLine{
		ChatID:    "chat-approval-multirun",
		RunID:     "run-2",
		UpdatedAt: 1002,
		Query: map[string]any{
			"role":    "user",
			"message": "second",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append second query line: %v", err)
	}
	if err := store.AppendStepLine("chat-approval-multirun", StepLine{
		ChatID:    "chat-approval-multirun",
		RunID:     "run-2",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "second reply"}},
			MsgID:   "msg-2",
		}},
	}); err != nil {
		t.Fatalf("append second step line: %v", err)
	}

	rawMessages, err := store.LoadRawMessages("chat-approval-multirun", 10)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 5 {
		t.Fatalf("expected first query, first reply, summary, second query, second reply; got %#v", rawMessages)
	}
	if rawMessages[2]["role"] != "user" || rawMessages[2]["content"] != "[System audit — HITL approval batch]\nfirst approval" {
		t.Fatalf("expected first run approval LLM notice before next run query, got %#v", rawMessages)
	}
	if rawMessages[3]["runId"] != "run-2" || rawMessages[3]["content"] != "second" {
		t.Fatalf("expected second run query after first summary, got %#v", rawMessages)
	}
}

func TestStepWriterSubAgentStepsAreExcludedFromRawMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-subagent-raw", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-subagent-raw", "run-subagent-raw", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"contentId": "root_1",
			"text":      "root",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "task.start",
		Timestamp: 1002,
		Payload: map[string]any{
			"taskId":         "task_1",
			"runId":          "run-subagent-raw",
			"taskName":       "分析",
			"description":    "run analysis",
			"subAgentKey":    "analyzer",
			"invokingToolId": "tool_main_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1003,
		Payload: map[string]any{
			"contentId": "child_1",
			"taskId":    "task_1",
			"text":      "child",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "task.complete",
		Timestamp: 1004,
		Payload: map[string]any{
			"taskId": "task_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1005,
		Payload: map[string]any{
			"contentId": "root_2",
			"text":      "root again",
		},
	})
	writer.Flush()

	rawMessages, err := store.LoadRawMessages("chat-subagent-raw", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(rawMessages) != 2 {
		t.Fatalf("expected only root messages in raw history, got %#v", rawMessages)
	}
	for _, msg := range rawMessages {
		if msg["content"] == "child" {
			t.Fatalf("did not expect sub-agent content in raw messages, got %#v", rawMessages)
		}
	}

	lines, err := readJSONLines(store.chatJSONLPath("chat-subagent-raw"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected three step lines, got %#v", lines)
	}
	if lines[1]["taskSubAgentKey"] != "analyzer" || lines[1]["taskStatus"] != "completed" {
		t.Fatalf("expected slim sub-agent task metadata on task step, got %#v", lines[1])
	}
	if _, ok := lines[1]["taskToolId"]; ok {
		t.Fatalf("did not expect taskToolId on slim task step, got %#v", lines[1])
	}
}

func TestStepWriterTaskSnapshotsUpsertAfterComplete(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-task-upsert", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-task-upsert", "run-task-upsert", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "task.start",
		Timestamp: 1001,
		Payload: map[string]any{
			"taskId":         "task_1",
			"taskName":       "讲故事",
			"subAgentKey":    "story-agent",
			"invokingToolId": "tool_main_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"contentId": "content_1",
			"taskId":    "task_1",
			"text":      "标题",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "task.complete",
		Timestamp: 1003,
		Payload: map[string]any{
			"taskId": "task_1",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1004,
		Payload: map[string]any{
			"contentId": "content_1",
			"taskId":    "task_1",
			"text":      "标题\n完整正文",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-task-upsert"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one task step, got %#v", lines)
	}
	if lines[0]["taskId"] != "task_1" || lines[0]["taskStatus"] != "completed" || lines[0]["taskSubAgentKey"] != "story-agent" {
		t.Fatalf("expected completed task metadata, got %#v", lines[0])
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one upserted message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["_contentId"] != "content_1" || strings.Contains(extractTextFromContent(msg["content"]), "完整正文") {
		t.Fatalf("expected post-complete snapshot to be ignored, got %#v", msg)
	}
}

func TestStepWriterRootSnapshotsUpsertByContentID(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-root-upsert", "run-root-upsert", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"contentId": "content_1",
			"text":      "short",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"contentId": "content_1",
			"text":      "short and complete",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-root-upsert"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one root step, got %#v", lines)
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one upserted root message, got %#v", lines[0])
	}
	msg, _ := messages[0].(map[string]any)
	if got := extractTextFromContent(msg["content"]); got != "short and complete" {
		t.Fatalf("expected latest root content, got %q from %#v", got, msg)
	}
}

func TestStepWriterDoesNotInferTaskForUntargetedContent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-subagent-no-infer", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-subagent-no-infer", "run-subagent-no-infer", "react", false)
	writer.OnEvent(stream.EventData{
		Type:      "task.start",
		Timestamp: 1001,
		Payload: map[string]any{
			"taskId":      "task_1",
			"taskName":    "分析",
			"subAgentKey": "analyzer",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"contentId": "root_1",
			"text":      "root final",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "task.complete",
		Timestamp: 1003,
		Payload: map[string]any{
			"taskId": "task_1",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-subagent-no-infer"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	var sawRootStep, sawTaskStep bool
	for _, line := range lines {
		messages, _ := line["messages"].([]any)
		if len(messages) > 0 {
			msg, _ := messages[0].(map[string]any)
			parts, _ := msg["content"].([]any)
			if len(parts) > 0 {
				part, _ := parts[0].(map[string]any)
				if part["text"] == "root final" {
					sawRootStep = true
					if _, hasTaskID := line["taskId"]; hasTaskID {
						t.Fatalf("did not expect untargeted content to be assigned to task, got %#v", line)
					}
				}
			}
		}
		if line["taskId"] == "task_1" && line["taskStatus"] == "completed" {
			sawTaskStep = true
		}
	}
	if !sawRootStep || !sawTaskStep {
		t.Fatalf("expected separate root and task steps, got %#v", lines)
	}
}

func TestLoadChatSynthesizesTaskLifecycleFromSubAgentSteps(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-subagent-replay", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	contentTs := int64(2001)
	if err := store.AppendQueryLine("chat-subagent-replay", QueryLine{
		ChatID:      "chat-subagent-replay",
		RunID:       "run-subagent-replay",
		UpdatedAt:   2000,
		TaskID:      "task_1",
		TaskName:    "分析",
		TaskToolID:  "tool_main_1",
		SubAgentKey: "analyzer",
		Query: map[string]any{
			"chatId":   "chat-subagent-replay",
			"runId":    "run-subagent-replay",
			"agentKey": "analyzer",
			"message":  "run analysis",
			"role":     "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append sub-agent query line: %v", err)
	}
	if err := store.AppendStepLine("chat-subagent-replay", StepLine{
		ChatID:          "chat-subagent-replay",
		RunID:           "run-subagent-replay",
		UpdatedAt:       2002,
		Type:            "react",
		Seq:             1,
		TaskID:          "task_1",
		TaskStatus:      "completed",
		TaskSubAgentKey: "analyzer",
		Messages: []StoredMessage{{
			Role:             "assistant",
			ReasoningContent: []ContentPart{{Type: "text", Text: "thinking"}},
			Content:          []ContentPart{{Type: "text", Text: "child result"}},
			ReasoningID:      "reason_1",
			ContentID:        "child_1",
			ToolID:           "child_tool_1",
			MsgID:            "msg-1",
			Ts:               &contentTs,
			ToolCalls: []StoredToolCall{{
				ID:   "child_tool_1",
				Type: "function",
				Function: StoredFunction{
					Name:      "datetime",
					Arguments: `{"format":"iso"}`,
				},
			}},
		}},
	}); err != nil {
		t.Fatalf("append sub-agent step line: %v", err)
	}
	if err := store.AppendStepLine("chat-subagent-replay", StepLine{
		ChatID:    "chat-subagent-replay",
		RunID:     "run-subagent-replay",
		UpdatedAt: 2003,
		Type:      "react",
		Seq:       2,
		Messages: []StoredMessage{{
			Role:      "assistant",
			Content:   []ContentPart{{Type: "text", Text: "root result"}},
			ContentID: "root_1",
			MsgID:     "msg-2",
			Ts:        &contentTs,
		}},
	}); err != nil {
		t.Fatalf("append root step line: %v", err)
	}

	detail, err := store.LoadChat("chat-subagent-replay")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	var eventTypes []string
	for _, event := range detail.Events {
		eventTypes = append(eventTypes, event.Type)
	}
	joined := strings.Join(eventTypes, ",")
	if !strings.Contains(joined, "task.start") || !strings.Contains(joined, "task.complete") {
		t.Fatalf("expected synthesized task lifecycle events, got %v", eventTypes)
	}

	positions := map[string]int{}
	var sawStart, sawComplete bool
	for _, event := range detail.Events {
		if _, exists := positions[event.Type]; !exists {
			positions[event.Type] = len(positions)
		}
		if _, ok := event.Payload["groupId"]; ok {
			t.Fatalf("did not expect groupId in replayed task lifecycle payload: %#v", event)
		}
		switch event.Type {
		case "task.start":
			if event.String("subAgentKey") == "analyzer" && event.String("invokingToolId") == "tool_main_1" && event.String("taskName") == "分析" {
				sawStart = true
			}
		case "task.complete":
			if event.String("taskId") == "task_1" {
				sawComplete = true
			}
		}
	}
	if !sawStart || !sawComplete {
		t.Fatalf("expected synthesized start and complete payloads, got %#v", detail.Events)
	}
	order := eventOrder(detail.Events, "task.start", "reasoning.snapshot", "content.snapshot", "tool.snapshot", "task.complete")
	for _, eventType := range []string{"task.start", "reasoning.snapshot", "content.snapshot", "tool.snapshot", "task.complete"} {
		if order[eventType] < 0 {
			t.Fatalf("expected %s in replayed events, got %#v", eventType, detail.Events)
		}
	}
	if !(order["task.start"] < order["reasoning.snapshot"] &&
		order["reasoning.snapshot"] < order["content.snapshot"] &&
		order["content.snapshot"] < order["tool.snapshot"] &&
		order["tool.snapshot"] < order["task.complete"]) {
		t.Fatalf("unexpected replay event ordering: order=%#v events=%#v", order, detail.Events)
	}
}

func TestReplayedSubQueryTaskStartPrecedesRequestQuery(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-sub-query-order", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-sub-query-order", QueryLine{
		ChatID:      "chat-sub-query-order",
		RunID:       "run-sub-query-order",
		UpdatedAt:   1000,
		TaskID:      "task_1",
		TaskName:    "分析",
		TaskToolID:  "tool_main_1",
		SubAgentKey: "analyzer",
		Query: map[string]any{
			"chatId":   "chat-sub-query-order",
			"runId":    "run-sub-query-order",
			"agentKey": "analyzer",
			"message":  "run analysis",
			"role":     "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append sub-agent query line: %v", err)
	}
	if err := store.AppendStepLine("chat-sub-query-order", StepLine{
		ChatID:          "chat-sub-query-order",
		RunID:           "run-sub-query-order",
		UpdatedAt:       1001,
		Type:            "react",
		TaskID:          "task_1",
		TaskStatus:      "completed",
		TaskSubAgentKey: "analyzer",
		Messages: []StoredMessage{{
			Role:      "assistant",
			Content:   []ContentPart{{Type: "text", Text: "done"}},
			ContentID: "child_1",
		}},
	}); err != nil {
		t.Fatalf("append sub-agent step line: %v", err)
	}

	detail, err := store.LoadChat("chat-sub-query-order")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	var taskStartSeq, subQuerySeq int64
	var taskStartCount int
	for _, event := range detail.Events {
		switch event.Type {
		case "task.start":
			if event.String("taskId") == "task_1" {
				taskStartCount++
				taskStartSeq = event.Seq
			}
		case "request.query":
			if event.String("taskId") == "task_1" {
				subQuerySeq = event.Seq
			}
		}
	}
	if taskStartCount != 1 {
		t.Fatalf("expected one task.start for task_1, got %d in %#v", taskStartCount, detail.Events)
	}
	if taskStartSeq == 0 || subQuerySeq == 0 || !(taskStartSeq < subQuerySeq) {
		t.Fatalf("expected task.start to precede sub request.query, start=%d query=%d events=%#v", taskStartSeq, subQuerySeq, detail.Events)
	}
}

func TestReplayedSubQueryWithoutTaskIDUnchanged(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-root-query-no-task", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-root-query-no-task", QueryLine{
		ChatID:    "chat-root-query-no-task",
		RunID:     "run-root-query-no-task",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-root-query-no-task",
			"runId":   "run-root-query-no-task",
			"message": "hello",
			"role":    "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append root query line: %v", err)
	}

	detail, err := store.LoadChat("chat-root-query-no-task")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "task.start" {
			t.Fatalf("did not expect task.start for root query without taskId, got %#v", detail.Events)
		}
	}
}

func eventOrder(events []stream.EventData, eventTypes ...string) map[string]int {
	order := make(map[string]int, len(eventTypes))
	for _, eventType := range eventTypes {
		order[eventType] = -1
	}
	for index, event := range events {
		if _, ok := order[event.Type]; ok && order[event.Type] < 0 {
			order[event.Type] = index
		}
	}
	return order
}

func TestLoadChatReplaysQuestionAwaitLifecycleLegacyEventLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-1", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-1", QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-1",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1001,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120000,
			"runId":      "run-1",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"type":     "number",
				},
				map[string]any{
					"id":       "q2",
					"question": "Topics?",
					"type":     "select",
				},
			},
		},
	}); err != nil {
		t.Fatalf("append await ask line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1003,
		Type:      "event",
		Event: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-1",
			"runId":      "run-1",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{
					"id":     "q1",
					"answer": 3,
				},
				map[string]any{
					"id":      "q2",
					"answers": []any{"产品更新", "使用教程"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append request submit line: %v", err)
	}

	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 1004,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"answers": []any{
				map[string]any{
					"id":       "q1",
					"question": "How many?",
					"answer":   3,
				},
				map[string]any{
					"id":       "q2",
					"question": "Topics?",
					"answers":  []any{"产品更新", "使用教程"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append awaiting.answer line: %v", err)
	}

	detail, err := store.LoadChat("chat-1")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	if len(detail.Events) != 7 {
		t.Fatalf("expected 7 replayed events, got %d: %#v", len(detail.Events), detail.Events)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"awaiting.ask",
		"request.submit",
		"awaiting.answer",
		"run.complete",
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	viewport := detail.Events[3]
	if _, exists := viewport.Payload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on awaiting.ask replay %#v", viewport)
	}
	if _, exists := viewport.Payload["chatId"]; exists {
		t.Fatalf("did not expect chatId on awaiting.ask replay %#v", viewport)
	}

	questions, _ := viewport.Value("questions").([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline await ask replay, got %#v", viewport)
	}

	submit := detail.Events[4]
	submitParams, _ := submit.Value("params").([]any)
	if submit.String("awaitingId") != "tool-1" || len(submitParams) != 2 {
		t.Fatalf("unexpected request.submit replay %#v", submit)
	}
	answer := detail.Events[5]
	answerQuestions, _ := answer.Value("answers").([]any)
	if answer.String("awaitingId") != "tool-1" || len(answerQuestions) != 2 {
		t.Fatalf("unexpected awaiting.answer replay %#v", answer)
	}
}

func TestLoadChatReplaysSubmitLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-submit-replay", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-submit-replay", QueryLine{
		ChatID:    "chat-submit-replay",
		RunID:     "run-submit-replay",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-submit-replay",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendSubmitLine("chat-submit-replay", SubmitLine{
		ChatID:    "chat-submit-replay",
		RunID:     "run-submit-replay",
		UpdatedAt: 1001,
		Type:      "submit",
		Submit: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-1",
			"chatId":     "chat-submit-replay",
			"awaitingId": "tool-1",
			"params": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
		Answer: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-1",
			"mode":       "question",
			"questions": []any{
				map[string]any{"question": "How many?", "answer": 3},
			},
		},
	}); err != nil {
		t.Fatalf("append submit line: %v", err)
	}

	detail, err := store.LoadChat("chat-submit-replay")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"request.submit",
		"awaiting.answer",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[3].String("runId") != "run-submit-replay" {
		t.Fatalf("expected runId to be backfilled on request.submit, got %#v", detail.Events[3])
	}
	if detail.Events[4].String("runId") != "run-submit-replay" {
		t.Fatalf("expected runId to be backfilled on awaiting.answer, got %#v", detail.Events[4])
	}
}

func TestLoadChatReplaysAwaitingFromStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-replay", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-awaiting-replay", QueryLine{
		ChatID:    "chat-awaiting-replay",
		RunID:     "run-awaiting-replay",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-awaiting-replay",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	toolTs := int64(1001)
	resultTs := int64(1003)
	if err := store.AppendStepLine("chat-awaiting-replay", StepLine{
		ChatID:    "chat-awaiting-replay",
		RunID:     "run-awaiting-replay",
		UpdatedAt: 1004,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "ask_user",
						Arguments: "{\"question\":\"How many?\"}",
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "ask_user",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "{\"ok\":true}"}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1002,
				"awaitingId": "tool-1",
				"mode":       "question",
				"timeout":    120000,
				"questions": []any{
					map[string]any{
						"id":       "q1",
						"question": "How many?",
						"type":     "number",
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-replay")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"tool.snapshot",
		"awaiting.ask",
		"tool.result",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[4].String("runId") != "run-awaiting-replay" {
		t.Fatalf("expected runId to be backfilled on awaiting.ask, got %#v", detail.Events[4])
	}
	if detail.Events[3].String("toolId") != "tool-1" || detail.Events[4].String("awaitingId") != "tool-1" || detail.Events[5].String("toolId") != "tool-1" {
		t.Fatalf("expected awaiting.ask to be replayed between matching tool snapshot and result, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysAwaitingAfterMatchingToolSnapshotInMultiToolStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-multi", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	assistantTs := int64(1001)
	toolATs := int64(1002)
	toolBTs := int64(1003)
	if err := store.AppendStepLine("chat-awaiting-multi", StepLine{
		ChatID:    "chat-awaiting-multi",
		RunID:     "run-awaiting-multi",
		UpdatedAt: 1005,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{
					{
						ID:   "tool-a",
						Type: "function",
						Function: StoredFunction{
							Name:      "ask_a",
							Arguments: "{\"question\":\"A?\"}",
						},
					},
					{
						ID:   "tool-b",
						Type: "function",
						Function: StoredFunction{
							Name:      "ask_b",
							Arguments: "{\"question\":\"B?\"}",
						},
					},
				},
				MsgID: "msg-1",
				Ts:    &assistantTs,
			},
			{
				Role:       "tool",
				Name:       "ask_a",
				ToolCallID: "tool-a",
				Content:    []ContentPart{{Type: "text", Text: "done-a"}},
				ToolID:     "tool-a",
				Ts:         &toolATs,
			},
			{
				Role:       "tool",
				Name:       "ask_b",
				ToolCallID: "tool-b",
				Content:    []ContentPart{{Type: "text", Text: "done-b"}},
				ToolID:     "tool-b",
				Ts:         &toolBTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1001,
				"awaitingId": "tool-b",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "qb", "question": "B?"}},
			},
			{
				"type":       "awaiting.ask",
				"timestamp":  1001,
				"awaitingId": "tool-a",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "qa", "question": "A?"}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-multi")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"tool.snapshot",
		"awaiting.ask",
		"tool.snapshot",
		"awaiting.ask",
		"tool.result",
		"tool.result",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[2].String("toolId") != "tool-a" || detail.Events[3].String("awaitingId") != "tool-a" {
		t.Fatalf("expected tool-a awaiting.ask immediately after snapshot, got %#v", detail.Events)
	}
	if detail.Events[4].String("toolId") != "tool-b" || detail.Events[5].String("awaitingId") != "tool-b" {
		t.Fatalf("expected tool-b awaiting.ask immediately after snapshot, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysUnmatchedAwaitingAtStepEnd(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-awaiting-fallback", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	toolTs := int64(1001)
	resultTs := int64(1002)
	if err := store.AppendStepLine("chat-awaiting-fallback", StepLine{
		ChatID:    "chat-awaiting-fallback",
		RunID:     "run-awaiting-fallback",
		UpdatedAt: 1004,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-1",
					Type: "function",
					Function: StoredFunction{
						Name:      "ask_user",
						Arguments: "{\"question\":\"How many?\"}",
					},
				}},
				ToolID: "tool-1",
				MsgID:  "msg-1",
				Ts:     &toolTs,
			},
			{
				Role:       "tool",
				Name:       "ask_user",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "done"}},
				ToolID:     "tool-1",
				Ts:         &resultTs,
			},
		},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"timestamp":  1003,
				"awaitingId": "tool-missing",
				"mode":       "question",
				"questions":  []any{map[string]any{"id": "q1", "question": "How many?"}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-fallback")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"tool.snapshot",
		"tool.result",
		"awaiting.ask",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[4].String("awaitingId") != "tool-missing" {
		t.Fatalf("expected unmatched awaiting.ask to be preserved at step end, got %#v", detail.Events[4])
	}
}

func TestLoadChatReplaysSteerLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-steer", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-steer", QueryLine{
		ChatID:    "chat-steer",
		RunID:     "run-steer",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-steer",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-steer", EventLine{
		ChatID:    "chat-steer",
		RunID:     "run-steer",
		UpdatedAt: 1001,
		Type:      "steer",
		Event: map[string]any{
			"type":      "request.steer",
			"requestId": "req-steer",
			"chatId":    "chat-steer",
			"steerId":   "steer-1",
			"message":   "focus on the root cause",
		},
	}); err != nil {
		t.Fatalf("append steer line: %v", err)
	}

	detail, err := store.LoadChat("chat-steer")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"request.steer",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}
	if detail.Events[3].String("runId") != "run-steer" {
		t.Fatalf("expected runId to be backfilled on request.steer, got %#v", detail.Events[3])
	}
}

func TestLoadChatReadsUsageFromStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-step-usage", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-step-usage", QueryLine{
		ChatID:    "chat-step-usage",
		RunID:     "run-step-usage",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-step-usage",
			"message": "hello",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	contentTs := int64(1002)
	if err := store.AppendStepLine("chat-step-usage", StepLine{
		ChatID:    "chat-step-usage",
		RunID:     "run-step-usage",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:      "assistant",
				Content:   textContent("answer"),
				ContentID: "content-1",
				MsgID:     "msg-1",
				Ts:        &contentTs,
			},
		},
		Usage: map[string]any{
			"promptTokens":           100,
			"completionTokens":       50,
			"totalTokens":            150,
			"llmChatCompletionCount": 1,
		},
		Debug: map[string]any{
			"preCall": map[string]any{
				"provider": map[string]any{
					"key":      "minimax",
					"endpoint": "https://api.minimaxi.com/v1/chat/completions",
				},
				"model": map[string]any{
					"key": "mock-model",
					"id":  "mock-model-id",
				},
				"requestBody": map[string]any{
					"model": "mock-model-id",
				},
			},
		},
		ContextWindow: map[string]any{
			"maxSize":       128000,
			"actualSize":    100,
			"estimatedSize": 200,
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-step-usage")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	expectedTypes := []string{
		"chat.start",
		"run.start",
		"request.query",
		"debug.preCall",
		"content.snapshot",
		"usage.snapshot",
		"debug.postCall",
		"run.complete",
	}
	if len(detail.Events) != len(expectedTypes) {
		t.Fatalf("expected %d replayed events, got %d: %#v", len(expectedTypes), len(detail.Events), detail.Events)
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	preCallData, _ := detail.Events[3].Value("data").(map[string]any)
	preCallProvider, _ := preCallData["provider"].(map[string]any)
	preCallModel, _ := preCallData["model"].(map[string]any)
	preCallCW, _ := preCallData["contextWindow"].(map[string]any)
	preCallRequestBody, _ := preCallData["requestBody"].(map[string]any)
	if toIntValue(preCallCW["maxSize"]) != 128000 || toIntValue(preCallCW["actualSize"]) != 100 || toIntValue(preCallCW["estimatedSize"]) != 200 {
		t.Fatalf("unexpected debug.preCall context window %#v", detail.Events[3])
	}
	if preCallProvider["key"] != "minimax" || preCallProvider["endpoint"] != "https://api.minimaxi.com/v1/chat/completions" {
		t.Fatalf("unexpected debug.preCall provider %#v", detail.Events[3])
	}
	if preCallModel["key"] != "mock-model" || preCallModel["id"] != "mock-model-id" {
		t.Fatalf("unexpected debug.preCall model %#v", detail.Events[3])
	}
	if preCallRequestBody["model"] != "mock-model-id" {
		t.Fatalf("unexpected debug.preCall payload %#v", detail.Events[3])
	}
	if _, exists := preCallData["systemPrompt"]; exists {
		t.Fatalf("did not expect legacy systemPrompt in debug.preCall payload %#v", detail.Events[3])
	}
	if _, exists := preCallData["tools"]; exists {
		t.Fatalf("did not expect legacy tools in debug.preCall payload %#v", detail.Events[3])
	}
	if _, exists := preCallData["usage"]; exists {
		t.Fatalf("did not expect usage in debug.preCall payload %#v", detail.Events[3])
	}

	usageSnapshotUsage, _ := detail.Events[5].Value("usage").(map[string]any)
	usageSnapshotCurrent, _ := usageSnapshotUsage["current"].(map[string]any)
	if toIntValue(usageSnapshotCurrent["promptTokens"]) != 100 || toIntValue(usageSnapshotCurrent["completionTokens"]) != 50 || toIntValue(usageSnapshotCurrent["totalTokens"]) != 150 {
		t.Fatalf("unexpected usage.snapshot current usage %#v", detail.Events[5])
	}
	if _, exists := usageSnapshotCurrent["llmChatCompletionCount"]; exists {
		t.Fatalf("did not expect usage.snapshot current llmChatCompletionCount %#v", detail.Events[5])
	}
	if _, exists := usageSnapshotUsage["run"]; exists {
		t.Fatalf("did not expect usage.snapshot run usage %#v", detail.Events[5])
	}
	if _, exists := usageSnapshotUsage["chat"]; exists {
		t.Fatalf("did not expect usage.snapshot chat usage %#v", detail.Events[5])
	}
	usageSnapshotCW, _ := detail.Events[5].Value("contextWindow").(map[string]any)
	if toIntValue(usageSnapshotCW["maxSize"]) != 128000 || toIntValue(usageSnapshotCW["currentSize"]) != 100 || toIntValue(usageSnapshotCW["estimatedNextCallSize"]) != 200 {
		t.Fatalf("unexpected usage.snapshot context window %#v", detail.Events[5])
	}

	postCallData, _ := detail.Events[6].Value("data").(map[string]any)
	postCallUsage, _ := postCallData["usage"].(map[string]any)
	llmUsage, _ := postCallUsage["llmReturnUsage"].(map[string]any)
	if toIntValue(llmUsage["promptTokens"]) != 100 || toIntValue(llmUsage["completionTokens"]) != 50 || toIntValue(llmUsage["totalTokens"]) != 150 {
		t.Fatalf("unexpected debug.postCall usage %#v", detail.Events[6])
	}
	if _, exists := postCallUsage["runUsage"]; exists {
		t.Fatalf("did not expect debug.postCall run usage %#v", detail.Events[6])
	}
	if _, exists := postCallUsage["chatUsage"]; exists {
		t.Fatalf("did not expect debug.postCall chat usage %#v", detail.Events[6])
	}
	if _, exists := detail.Events[7].Value("usage").(map[string]any); exists {
		t.Fatalf("did not expect synthesized run.complete usage %#v", detail.Events[7])
	}
}

func TestLoadChatDoesNotSynthesizeEmptyUsageSnapshot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-empty-step-usage", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-empty-step-usage", QueryLine{
		ChatID:    "chat-empty-step-usage",
		RunID:     "run-empty-step-usage",
		UpdatedAt: 1000,
		Query:     map[string]any{"chatId": "chat-empty-step-usage", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	contentTs := int64(1002)
	if err := store.AppendStepLine("chat-empty-step-usage", StepLine{
		ChatID:    "chat-empty-step-usage",
		RunID:     "run-empty-step-usage",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:      "assistant",
			Content:   textContent("answer"),
			ContentID: "content-1",
			MsgID:     "msg-1",
			Ts:        &contentTs,
		}},
		Usage: map[string]any{
			"promptTokens":           0,
			"completionTokens":       0,
			"totalTokens":            0,
			"llmChatCompletionCount": 1,
		},
		ContextWindow: map[string]any{
			"maxSize":       128000,
			"estimatedSize": 5703,
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-empty-step-usage")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "usage.snapshot" {
			t.Fatalf("did not expect empty usage snapshot to be synthesized, got %#v", detail.Events)
		}
	}
	complete := detail.Events[len(detail.Events)-1]
	if complete.Type != "run.complete" {
		t.Fatalf("expected terminal run.complete, got %#v", complete)
	}
	if _, ok := complete.Value("usage").(map[string]any); ok {
		t.Fatalf("did not expect terminal usage from empty step usage, got %#v", complete)
	}
}

func TestLoadChatReadsLegacySnakeCaseUsageFromStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-legacy-step-usage", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-legacy-step-usage", QueryLine{
		ChatID:    "chat-legacy-step-usage",
		RunID:     "run-legacy-step-usage",
		UpdatedAt: 1000,
		Query:     map[string]any{"chatId": "chat-legacy-step-usage", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := store.AppendStepLine("chat-legacy-step-usage", StepLine{
		ChatID:    "chat-legacy-step-usage",
		RunID:     "run-legacy-step-usage",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:      "assistant",
			Content:   textContent("answer"),
			ContentID: "content-1",
		}},
		Usage: map[string]any{
			"prompt_tokens":             100,
			"completion_tokens":         50,
			"total_tokens":              150,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 32},
			"completion_tokens_details": map[string]any{"reasoning_tokens": 8},
			"prompt_cache_hit_tokens":   32,
			"prompt_cache_miss_tokens":  68,
			"llm_chat_completion_count": 1,
		},
		ContextWindow: map[string]any{
			"max_size":       128000,
			"actual_size":    100,
			"estimated_size": 200,
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-legacy-step-usage")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if len(detail.Events) != 6 {
		t.Fatalf("expected legacy usage replay events, got %#v", detail.Events)
	}
	for _, event := range detail.Events {
		if event.Type == "debug.preCall" || event.Type == "debug.postCall" {
			t.Fatalf("did not expect usage-only legacy step to synthesize debug event, got %#v", detail.Events)
		}
	}
	usageSnapshotUsage, _ := detail.Events[4].Value("usage").(map[string]any)
	usageSnapshotCurrent, _ := usageSnapshotUsage["current"].(map[string]any)
	usageSnapshotPromptDetails, _ := usageSnapshotCurrent["promptTokensDetails"].(map[string]any)
	if detail.Events[4].Type != "usage.snapshot" || toIntValue(usageSnapshotPromptDetails["cacheHitTokens"]) != 32 || toIntValue(usageSnapshotPromptDetails["cacheMissTokens"]) != 68 {
		t.Fatalf("expected usage.snapshot with DeepSeek cache fields, got %#v", detail.Events)
	}
	if _, exists := usageSnapshotCurrent["llmChatCompletionCount"]; exists {
		t.Fatalf("did not expect usage.snapshot current llmChatCompletionCount %#v", detail.Events[4])
	}
	if _, exists := usageSnapshotUsage["run"]; exists {
		t.Fatalf("did not expect usage.snapshot run usage %#v", detail.Events[4])
	}
	if _, exists := usageSnapshotUsage["chat"]; exists {
		t.Fatalf("did not expect usage.snapshot chat usage %#v", detail.Events[4])
	}
	if _, exists := detail.Events[5].Value("usage").(map[string]any); exists {
		t.Fatalf("did not expect synthesized run.complete usage %#v", detail.Events[5])
	}
}

func TestLoadChatReplaysApprovalAwaitLifecycleLegacyEventLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-approval", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-approval", QueryLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-approval",
			"message": "please approve",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1001,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "tool-approval",
			"mode":       "approval",
			"timeout":    120000,
			"runId":      "run-approval",
			"approvals": []any{
				map[string]any{
					"id":      "cmd-1",
					"command": "Proceed?",
					"level":   1,
				},
			},
		},
	}); err != nil {
		t.Fatalf("append approval await ask line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1002,
		Type:      "event",
		Event: map[string]any{
			"type":       "request.submit",
			"requestId":  "req-approval",
			"chatId":     "chat-approval",
			"runId":      "run-approval",
			"awaitingId": "tool-approval",
			"params": []any{
				map[string]any{"id": "cmd-1", "decision": "approve"},
			},
		},
	}); err != nil {
		t.Fatalf("append approval request submit line: %v", err)
	}

	if err := store.AppendEventLine("chat-approval", EventLine{
		ChatID:    "chat-approval",
		RunID:     "run-approval",
		UpdatedAt: 1003,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "tool-approval",
			"mode":       "approval",
			"approvals": []any{
				map[string]any{"id": "cmd-1", "command": "Proceed?", "decision": "approve"},
			},
		},
	}); err != nil {
		t.Fatalf("append approval awaiting.answer line: %v", err)
	}

	detail, err := store.LoadChat("chat-approval")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundAwaitAsk := false
	foundAwaitAnswer := false
	for _, event := range detail.Events {
		switch event.Type {
		case "awaiting.ask":
			foundAwaitAsk = true
			approvals, _ := event.Value("approvals").([]any)
			if len(approvals) != 1 {
				t.Fatalf("expected approval awaiting.ask approvals length 1, got %#v", event)
			}
		case "awaiting.answer":
			foundAwaitAnswer = true
			approvals, _ := event.Value("approvals").([]any)
			if event.String("mode") != "approval" || len(approvals) != 1 {
				t.Fatalf("unexpected approval awaiting.answer %#v", event)
			}
		}
	}
	if !foundAwaitAsk {
		t.Fatalf("expected approval awaiting.ask replay, got %#v", detail.Events)
	}
	if !foundAwaitAnswer {
		t.Fatalf("expected approval awaiting.answer replay, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysLegacyConfirmLifecycleEvents(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-legacy", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-legacy", QueryLine{
		ChatID:    "chat-legacy",
		RunID:     "run-legacy",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-legacy",
			"message": "legacy confirm flow",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	legacyEvents := []map[string]any{
		{
			"type":         "confirm.viewport",
			"confirmId":    "tool-legacy",
			"confirmName":  "_ask_user_approval_",
			"viewportType": "builtin",
			"viewportKey":  "confirm_dialog",
			"mode":         "approval",
			"timeout":      120000,
			"runId":        "run-legacy",
			"chatId":       "chat-legacy",
		},
		{
			"type":      "confirm.payload",
			"confirmId": "tool-legacy",
			"payload":   map[string]any{"mode": "approval"},
		},
		{
			"type":       "request.submit",
			"requestId":  "req-legacy",
			"chatId":     "chat-legacy",
			"runId":      "run-legacy",
			"awaitingId": "tool-legacy",
			"params":     map[string]any{"value": "approve"},
		},
	}
	for _, event := range legacyEvents {
		if err := store.AppendEventLine("chat-legacy", EventLine{
			ChatID:    "chat-legacy",
			RunID:     "run-legacy",
			UpdatedAt: 1001,
			Type:      "event",
			Event:     event,
		}); err != nil {
			t.Fatalf("append legacy event line: %v", err)
		}
	}

	detail, err := store.LoadChat("chat-legacy")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundConfirmViewport := false
	foundConfirmPayload := false
	foundRequestSubmit := false
	for _, event := range detail.Events {
		switch event.Type {
		case "confirm.viewport":
			foundConfirmViewport = true
		case "confirm.payload":
			foundConfirmPayload = true
		case "request.submit":
			foundRequestSubmit = true
		}
	}
	if !foundConfirmViewport || !foundConfirmPayload || !foundRequestSubmit {
		t.Fatalf("expected legacy confirm lifecycle events to replay, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysLegacySourcePublishEvent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-source-legacy", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	legacyEvents := []stream.EventData{
		{
			Type:      "chat.start",
			Timestamp: 1000,
			Payload: map[string]any{
				"chatId":   "chat-source-legacy",
				"chatName": "hello",
			},
		},
		{
			Type:      "request.query",
			Timestamp: 1001,
			Payload: map[string]any{
				"chatId":  "chat-source-legacy",
				"runId":   "run-source-legacy",
				"message": "where is the policy?",
			},
		},
		{
			Type:      "run.start",
			Timestamp: 1002,
			Payload: map[string]any{
				"chatId": "chat-source-legacy",
				"runId":  "run-source-legacy",
			},
		},
		{
			Type:      "source.publish",
			Timestamp: 1003,
			Payload: map[string]any{
				"publishId":   "src-legacy",
				"runId":       "run-source-legacy",
				"kind":        "ragflow",
				"sourceCount": 1,
				"chunkCount":  1,
				"sources": []map[string]any{
					{
						"id":           "doc_1",
						"name":         "policy.pdf",
						"chunkIndexes": []int{2},
						"minIndex":     2,
						"chunks": []map[string]any{
							{
								"chunkId": "chunk_2",
								"index":   2,
								"content": "policy content",
							},
						},
					},
				},
			},
		},
		{
			Type:      "content.snapshot",
			Timestamp: 1004,
			Payload: map[string]any{
				"contentId": "run-source-legacy_c_1",
				"runId":     "run-source-legacy",
				"text":      "answer",
			},
		},
		{
			Type:      "run.complete",
			Timestamp: 1005,
			Payload: map[string]any{
				"runId": "run-source-legacy",
			},
		},
	}

	for idx := range legacyEvents {
		legacyEvents[idx].Seq = int64(idx + 1)
		if err := store.AppendEvent("chat-source-legacy", legacyEvents[idx]); err != nil {
			t.Fatalf("append legacy event: %v", err)
		}
	}

	detail, err := store.LoadChat("chat-source-legacy")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundSourcePublish := false
	foundContentSnapshot := false
	for _, event := range detail.Events {
		switch event.Type {
		case "source.publish":
			foundSourcePublish = true
			if event.String("publishId") != "src-legacy" || event.String("runId") != "run-source-legacy" {
				t.Fatalf("unexpected source.publish replay %#v", event)
			}
			sources, ok := event.Value("sources").([]any)
			if !ok || len(sources) != 1 {
				t.Fatalf("expected source.publish sources to replay, got %#v", event.Value("sources"))
			}
		case "content.snapshot":
			foundContentSnapshot = true
		}
	}
	if !foundSourcePublish {
		t.Fatalf("expected source.publish to replay, got %#v", detail.Events)
	}
	if !foundContentSnapshot {
		t.Fatalf("expected content.snapshot to remain intact, got %#v", detail.Events)
	}
}

func TestStepWriterBatchedArtifactPublishUpdatesArtifactState(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-artifact-batch", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-artifact-batch", "run-artifact-batch", "REACT", false)
	writer.OnEvent(stream.EventData{
		Type:      "run.start",
		Timestamp: 1000,
		Payload:   map[string]any{"chatId": "chat-artifact-batch", "runId": "run-artifact-batch"},
	})
	writer.OnEvent(stream.EventData{
		Type:      "artifact.publish",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":        "chat-artifact-batch",
			"runId":         "run-artifact-batch",
			"artifactCount": 2,
			"artifacts": []map[string]any{
				{
					"artifactId": "artifact_1",
					"type":       "file",
					"name":       "report.md",
					"mimeType":   "text/markdown",
					"sizeBytes":  123,
					"url":        "/api/resource?file=chat-artifact-batch%2Freport.md",
					"sha256":     "abc123",
				},
				{
					"artifactId": "artifact_2",
					"type":       "file",
					"name":       "summary.txt",
					"mimeType":   "text/plain",
					"sizeBytes":  45,
					"url":        "/api/resource?file=chat-artifact-batch%2Fsummary.txt",
					"sha256":     "def456",
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"contentId": "run-artifact-batch_c_1",
			"runId":     "run-artifact-batch",
			"text":      "done",
		},
	})
	writer.Flush()

	detail, err := store.LoadChat("chat-artifact-batch")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Artifact == nil || len(detail.Artifact.Items) != 2 {
		t.Fatalf("expected two artifacts in detail state, got %#v", detail.Artifact)
	}
	if detail.Artifact.Items[0].ArtifactID != "artifact_1" || detail.Artifact.Items[0].SizeBytes != 123 {
		t.Fatalf("unexpected first artifact %#v", detail.Artifact.Items[0])
	}
	if detail.Artifact.Items[1].ArtifactID != "artifact_2" || detail.Artifact.Items[1].SHA256 != "def456" {
		t.Fatalf("unexpected second artifact %#v", detail.Artifact.Items[1])
	}
}
