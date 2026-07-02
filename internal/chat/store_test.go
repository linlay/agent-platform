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

func TestFileStoreLoadAwaitingAskUsesCanonicalStepOnly(t *testing.T) {
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
	if err := store.AppendSubmitLine("chat-awaiting-step", SubmitLine{
		ChatID:    "chat-awaiting-step",
		RunID:     "run-step",
		UpdatedAt: 101,
		Type:      "submit",
		Answer: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "await_step",
			"mode":       "question",
			"status":     "answered",
		},
	}); err != nil {
		t.Fatalf("append answer line: %v", err)
	}
	ask, err = store.LoadAwaitingAsk("chat-awaiting-step", "await_step")
	if err != nil {
		t.Fatalf("load answered awaiting ask: %v", err)
	}
	if ask != nil {
		t.Fatalf("expected answered awaiting ask to be unresolved, got %#v", ask)
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
	if ask != nil {
		t.Fatalf("did not expect event-only awaiting ask to be loaded, got %#v", ask)
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

func TestFileStoreAddsRunsUsageColumns(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("open chats db: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE RUNS (
			RUN_ID_ TEXT PRIMARY KEY,
			CHAT_ID_ TEXT NOT NULL,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			INITIAL_MESSAGE_ TEXT NOT NULL DEFAULT '',
			ASSISTANT_TEXT_ TEXT NOT NULL DEFAULT '',
			FINISH_REASON_ TEXT NOT NULL DEFAULT '',
			STARTED_AT_ INTEGER NOT NULL DEFAULT 0,
			COMPLETED_AT_ INTEGER NOT NULL,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("create runs table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close chats db: %v", err)
	}

	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if store == nil {
		t.Fatal("expected migrated store")
	}

	db, err = sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("reopen migrated chats db: %v", err)
	}
	defer db.Close()
	columns := sqliteColumnNames(t, db, "RUNS")
	for _, col := range []string{
		"USAGE_MODEL_KEY_",
		"USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_",
		"USAGE_FIRST_TOKEN_LATENCY_COUNT_",
		"USAGE_GENERATION_DURATION_MS_",
	} {
		if !columns[col] {
			t.Fatalf("expected %s column to be added to RUNS; columns=%#v", col, columns)
		}
	}
}

func TestFileStoreAddsChatsUsageTimingColumns(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("open chats db: %v", err)
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
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("create chats table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close chats db: %v", err)
	}

	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if store == nil {
		t.Fatal("expected migrated store")
	}

	db, err = sql.Open("sqlite", filepath.Join(root, "chats.db"))
	if err != nil {
		t.Fatalf("reopen migrated chats db: %v", err)
	}
	defer db.Close()
	columns := sqliteColumnNames(t, db, "CHATS")
	for _, col := range []string{
		"USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_",
		"USAGE_FIRST_TOKEN_LATENCY_COUNT_",
		"USAGE_GENERATION_DURATION_MS_",
	} {
		if !columns[col] {
			t.Fatalf("expected %s column to be added to CHATS; columns=%#v", col, columns)
		}
	}
}

func sqliteColumnNames(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("pragma table_info %s: %v", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan %s table info: %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s table info: %v", table, err)
	}
	return columns
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
		Usage: UsageData{
			ModelKey:                 "mock-model",
			PromptTokens:             1,
			CompletionTokens:         2,
			TotalTokens:              3,
			EstimatedCostCurrency:    "CNY",
			EstimatedCostInputMiss:   0.01,
			EstimatedCostOutput:      0.02,
			EstimatedCostTotal:       0.03,
			FirstTokenLatencyTotalMs: 1200,
			FirstTokenLatencyCount:   1,
			GenerationDurationMs:     2400,
		},
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
	if runs[0].AgentKey != "agent-a" || runs[0].FinishReason != "complete" || runs[0].Usage.TotalTokens != 3 ||
		runs[0].Usage.ModelKey != "mock-model" || runs[0].Usage.EstimatedCostTotal != 0.03 {
		t.Fatalf("unexpected run summary: %#v", runs[0])
	}
	if runs[0].Usage.FirstTokenLatencyTotalMs != 1200 ||
		runs[0].Usage.FirstTokenLatencyCount != 1 ||
		runs[0].Usage.GenerationDurationMs != 2400 {
		t.Fatalf("expected run timing usage, got %#v", runs[0].Usage)
	}
	if sum.Usage == nil || sum.Usage.ModelKey != "" || sum.Usage.EstimatedCostCurrency != "CNY" || sum.Usage.EstimatedCostTotal != 0.03 {
		t.Fatalf("expected chat summary cost without aggregate modelKey, got %#v", sum.Usage)
	}
	if sum.Usage.FirstTokenLatencyTotalMs != 1200 ||
		sum.Usage.FirstTokenLatencyCount != 1 ||
		sum.Usage.GenerationDurationMs != 2400 {
		t.Fatalf("expected chat summary timing usage, got %#v", sum.Usage)
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
	if err := os.MkdirAll(filepath.Join(store.ChatDir("chat-delete"), ToolRootDirName, ToolResultsDirName), 0o755); err != nil {
		t.Fatalf("mkdir tool results dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat-delete"), ToolRootDirName, ToolResultsDirName, "call_1.json"), []byte(`{"stdout":"x"}`), 0o644); err != nil {
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

func TestStoredMessageToEventsAddsReasoningLabel(t *testing.T) {
	runID := "run_1"
	events := storedMessageToEvents(map[string]any{
		"role":              "assistant",
		"_reasoningId":      runID + "_r_2",
		"reasoning_content": []any{map[string]any{"type": "text", "text": "thinking"}},
	}, runID, "task_1", "plan", 0, func() int64 { return 1 })

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
				"role": "assistant",
				"ts":   ts,
				"tool_calls": []any{
					map[string]any{
						"id":        "action-call-1",
						"_actionId": "stored-action",
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
				"role": "assistant",
				"ts":   ts,
				"tool_calls": []any{
					map[string]any{
						"id":      "tool-call-1",
						"_toolId": "stored-tool",
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
				"durationMs":   int64(42),
			},
			wantType: "tool.result",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			events := storedMessageToEvents(tc.msg, "run_1", "task_1", "execute", 0, func() int64 { return 1 })
			if len(events) != 1 {
				t.Fatalf("expected one event, got %#v", events)
			}
			if events[0].Type != tc.wantType {
				t.Fatalf("expected %s, got %#v", tc.wantType, events[0])
			}
			if events[0].Timestamp != ts {
				t.Fatalf("expected timestamp %d, got %#v", ts, events[0])
			}
			if tc.name == "tool result" && events[0].Value("durationMs") != int64(42) {
				t.Fatalf("expected tool.result durationMs to replay, got %#v", events[0])
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

	writer := NewStepWriter(store, "chat-action-ts", "run-action-ts", "react")
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

func TestStepWriterPersistsLiveSeqAndReplaysIt(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-live-seq", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-live-seq", "run-live-seq", "react")
	writer.OnEvent(stream.EventData{
		Seq:       1,
		Type:      "request.query",
		Timestamp: 1000,
		Payload: map[string]any{
			"runId":   "run-live-seq",
			"chatId":  "chat-live-seq",
			"message": "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       2,
		Type:      "content.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "partial",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       3,
		Type:      "tool.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"date"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       4,
		Type:      "awaiting.ask",
		Timestamp: 1003,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"runId":      "run-live-seq",
			"mode":       "approval",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       5,
		Type:      "tool.result",
		Timestamp: 1004,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": "ok",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       6,
		Type:      "request.submit",
		Timestamp: 1005,
		Payload: map[string]any{
			"runId":      "run-live-seq",
			"chatId":     "chat-live-seq",
			"awaitingId": "tool-1",
			"submitId":   "submit-1",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       7,
		Type:      "awaiting.answer",
		Timestamp: 1006,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"runId":      "run-live-seq",
			"mode":       "approval",
			"status":     "accepted",
			"submitId":   "submit-1",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       8,
		Type:      "request.steer",
		Timestamp: 1007,
		Payload: map[string]any{
			"runId":   "run-live-seq",
			"chatId":  "chat-live-seq",
			"message": "nudge",
			"role":    "user",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-live-seq"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	var stepLines []map[string]any
	var submitLine map[string]any
	var steerLine map[string]any
	for _, line := range lines {
		switch line["_type"] {
		case "query":
			query, _ := line["query"].(map[string]any)
			if got := int64FromAny(line["liveSeq"]); got != 1 {
				t.Fatalf("expected query line liveSeq=1, got %#v", line)
			}
			if _, ok := query["liveSeq"]; ok {
				t.Fatalf("did not expect nested query liveSeq, got %#v", query)
			}
			if _, ok := query["seq"]; ok {
				t.Fatalf("did not expect nested query seq, got %#v", query)
			}
		case StepLineTypeReact, StepLineTypeReactTool:
			stepLines = append(stepLines, line)
		case "submit":
			submitLine = line
		case "steer":
			steerLine = line
		}
	}
	if len(stepLines) != 2 {
		t.Fatalf("expected two step lines, got %#v", lines)
	}
	if got := int64FromAny(stepLines[0]["liveSeq"]); got != 4 {
		t.Fatalf("expected first step line liveSeq=4, got %#v", stepLines[0])
	}
	if got := int64FromAny(stepLines[1]["liveSeq"]); got != 5 {
		t.Fatalf("expected second step line liveSeq=5, got %#v", stepLines[1])
	}
	messages, _ := stepLines[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected content and tool messages in first step, got %#v", stepLines[0])
	}
	for _, rawMessage := range messages {
		message := rawMessage.(map[string]any)
		if _, ok := message["liveSeq"]; ok {
			t.Fatalf("did not expect message liveSeq, got %#v", message)
		}
	}
	awaiting, _ := stepLines[0]["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected awaiting in first step, got %#v", stepLines[0])
	}
	awaitingEvent := awaiting[0].(map[string]any)
	if _, ok := awaitingEvent["liveSeq"]; ok {
		t.Fatalf("did not expect nested awaiting liveSeq, got %#v", awaitingEvent)
	}
	if _, ok := awaitingEvent["seq"]; ok {
		t.Fatalf("did not expect nested awaiting seq, got %#v", awaitingEvent)
	}
	resultMessages, _ := stepLines[1]["messages"].([]any)
	if len(resultMessages) != 1 {
		t.Fatalf("expected result message in second step, got %#v", stepLines[1])
	}
	resultMessage := resultMessages[0].(map[string]any)
	if _, ok := resultMessage["liveSeq"]; ok {
		t.Fatalf("did not expect result message liveSeq, got %#v", resultMessage)
	}
	submit, _ := submitLine["submit"].(map[string]any)
	answer, _ := submitLine["answer"].(map[string]any)
	if got := int64FromAny(submitLine["liveSeq"]); got != 7 {
		t.Fatalf("expected submit line liveSeq=7, got %#v", submitLine)
	}
	if _, ok := submit["liveSeq"]; ok {
		t.Fatalf("did not expect nested submit liveSeq, got %#v", submit)
	}
	if _, ok := submit["seq"]; ok {
		t.Fatalf("did not expect persisted submit seq, got %#v", submit)
	}
	if _, ok := answer["liveSeq"]; ok {
		t.Fatalf("did not expect nested answer liveSeq, got %#v", answer)
	}
	if _, ok := answer["seq"]; ok {
		t.Fatalf("did not expect persisted answer seq, got %#v", answer)
	}
	steerEvent, _ := steerLine["event"].(map[string]any)
	if got := int64FromAny(steerLine["liveSeq"]); got != 8 {
		t.Fatalf("expected steer line liveSeq=8, got %#v", steerLine)
	}
	if _, ok := steerEvent["liveSeq"]; ok {
		t.Fatalf("did not expect nested steer liveSeq, got %#v", steerEvent)
	}
	if _, ok := steerEvent["seq"]; ok {
		t.Fatalf("did not expect persisted steer seq, got %#v", steerEvent)
	}

	detail, err := store.LoadChat("chat-live-seq")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	wantLiveSeq := map[string]int64{
		"request.query":    1,
		"content.snapshot": 4,
		"tool.snapshot":    4,
		"awaiting.ask":     4,
		"tool.result":      5,
		"request.submit":   7,
		"awaiting.answer":  7,
		"request.steer":    8,
	}
	seen := map[string]bool{}
	for _, event := range detail.Events {
		want, ok := wantLiveSeq[event.Type]
		if !ok {
			continue
		}
		seen[event.Type] = true
		if got := int64FromAny(event.Value("liveSeq")); got != want {
			t.Fatalf("expected %s liveSeq=%d, got %#v", event.Type, want, event)
		}
		if event.Seq <= 0 {
			t.Fatalf("expected replay seq for %s, got %#v", event.Type, event)
		}
	}
	for eventType := range wantLiveSeq {
		if !seen[eventType] {
			t.Fatalf("expected replayed %s event, got %#v", eventType, detail.Events)
		}
	}
}

func TestStepWriterEmbedsAwaitingInStepLine(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-awaiting-step", "run-awaiting-step", "react")
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
			"timeout":    120,
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
		t.Fatalf("expected awaiting step line and submit line, got %#v", lines)
	}

	if got := lines[0]["_type"]; got != "react" {
		t.Fatalf("expected first persisted line to be step, got %#v", lines[0])
	}
	if got := toIntValue(lines[0]["updatedAt"]); got != 1002 {
		t.Fatalf("expected awaiting step updatedAt=1002, got %#v", lines[0])
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

func TestStepWriterMergesParallelToolSnapshotsIntoAssistantToolCalls(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-tool-call-merge", "run-tool-call-merge", "react")
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "call_00",
			"toolName":  "file_read",
			"arguments": `{"file_path":"/tmp/a.txt"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId":    "call_01",
			"toolName":  "file_glob",
			"arguments": `{"pattern":"*.md"}`,
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-tool-call-merge"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	messages, _ := lines[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one merged assistant message, got %#v", lines[0])
	}
	assistant, _ := messages[0].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant message, got %#v", assistant)
	}
	if _, ok := assistant["_toolId"]; ok {
		t.Fatalf("did not expect outer _toolId on assistant message, got %#v", assistant)
	}
	if _, ok := assistant["_actionId"]; ok {
		t.Fatalf("did not expect outer _actionId on assistant message, got %#v", assistant)
	}
	if assistant["_msgId"] == "" || toIntValue(assistant["ts"]) != 1001 {
		t.Fatalf("expected outer _msgId and first snapshot ts, got %#v", assistant)
	}
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("expected two merged tool calls, got %#v", assistant)
	}
	for index, wantID := range []string{"call_00", "call_01"} {
		call, _ := calls[index].(map[string]any)
		if call["id"] != wantID || call["_toolId"] != wantID {
			t.Fatalf("expected tool call %d to carry internal _toolId %q, got %#v", index, wantID, call)
		}
		if _, ok := call["_actionId"]; ok {
			t.Fatalf("did not expect _actionId on tool call %d, got %#v", index, call)
		}
	}
}

func TestStepWriterMergesSubmitAndAnswer(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-submit-merge", "run-submit-merge", "react")
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

	writer := NewStepWriter(store, "chat-timeout-submit", "run-timeout-submit", "react")
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
			"timeout":    120,
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
	if len(lines) != 3 {
		t.Fatalf("expected awaiting step, submit line, and tool result step, got %#v", lines)
	}
	awaitingStep := lines[0]
	submitLine := lines[1]
	toolResultStep := lines[2]
	if awaitingStep["_type"] != "react" || toIntValue(awaitingStep["seq"]) != 1 || toIntValue(awaitingStep["updatedAt"]) != 1002 {
		t.Fatalf("expected awaiting step seq=1 updatedAt=1002, got %#v", awaitingStep)
	}
	awaiting, _ := awaitingStep["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected awaiting on first step line, got %#v", awaitingStep)
	}
	messages, _ := awaitingStep["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected tool snapshot on first step line, got %#v", awaitingStep)
	}
	if toolResultStep["_type"] != StepLineTypeReactTool || toIntValue(toolResultStep["seq"]) != 1 {
		t.Fatalf("expected react-tool result step to reuse seq=1, got %#v", toolResultStep)
	}
	resultMessages, _ := toolResultStep["messages"].([]any)
	if len(resultMessages) != 1 {
		t.Fatalf("expected tool result in split step line, got %#v", toolResultStep)
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

	writer := NewStepWriter(store, "chat-hitl-seq", "run-hitl-seq", "react")
	writer.OnEvent(stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: 1000,
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":           1,
					"totalTokens":            1,
					"llmChatCompletionCount": 1,
				},
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
			"timeout":    120,
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
		Type:      "usage.snapshot",
		Timestamp: 1007,
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":           1,
					"totalTokens":            1,
					"llmChatCompletionCount": 1,
				},
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
		t.Fatalf("expected assistant awaiting step, submit line, tool step, final assistant step; got %#v", lines)
	}
	firstStep := lines[0]
	if firstStep["_type"] != "react" || toIntValue(firstStep["seq"]) != 1 || toIntValue(firstStep["updatedAt"]) != 1002 {
		t.Fatalf("expected first react step seq=1 updatedAt=1002, got %#v", firstStep)
	}
	awaiting, _ := firstStep["awaiting"].([]any)
	if len(awaiting) != 1 {
		t.Fatalf("expected awaiting on first step, got %#v", firstStep)
	}
	if _, ok := firstStep["approval"]; ok {
		t.Fatalf("did not expect approval on assistant tool-call step, got %#v", firstStep)
	}
	toolStep := lines[2]
	if toolStep["_type"] != StepLineTypeReactTool || toIntValue(toolStep["seq"]) != 1 {
		t.Fatalf("expected react-tool split result step to reuse seq=1, got %#v", toolStep)
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

	writer := NewStepWriter(store, "chat-tool-result-json", "run-tool-result-json", "react")
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
			"toolId":     "tool-1",
			"durationMs": int64(77),
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
	if len(lines) != 2 {
		t.Fatalf("expected tool call and tool result step lines, got %#v", lines)
	}
	if lines[0]["_type"] != "react" || toIntValue(lines[0]["seq"]) != 1 {
		t.Fatalf("expected tool call step seq=1, got %#v", lines[0])
	}
	if lines[1]["_type"] != StepLineTypeReactTool || toIntValue(lines[1]["seq"]) != 1 {
		t.Fatalf("expected tool result step to reuse seq=1, got %#v", lines[1])
	}
	messages, _ := lines[1]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one tool result message, got %#v", lines[1])
	}
	resultMsg, _ := messages[0].(map[string]any)
	if resultMsg["durationMs"] != float64(77) {
		t.Fatalf("expected persisted durationMs on tool result, got %#v", resultMsg)
	}
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

func TestStepWriterSplitsEachLLMRequestIntoReactStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-llm-step-boundary", "run-llm-step-boundary", "PLAN_EXECUTE")
	writer.OnStageMarker("execute-task-1")
	writer.OnEvent(stream.EventData{
		Type: "llm.request",
		Payload: map[string]any{
			"inputMessages": []any{map[string]any{"role": "user", "content": "call one"}},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "tool-1",
			"toolName":  "bash",
			"arguments": `{"command":"echo one"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "tool.result",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId": "tool-1",
			"result": "one",
		},
	})
	writer.OnEvent(stream.EventData{
		Type: "llm.request",
		Payload: map[string]any{
			"inputMessages": []any{map[string]any{"role": "user", "content": "call two"}},
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 1003,
		Payload: map[string]any{
			"contentId": "content-2",
			"text":      "done",
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-llm-step-boundary"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected react, react-tool, react lines, got %#v", lines)
	}
	if lines[0]["_type"] != StepLineTypeReact || toIntValue(lines[0]["seq"]) != 1 || lines[0]["stage"] != "execute" {
		t.Fatalf("expected first llm chat react seq=1 stage=execute, got %#v", lines[0])
	}
	firstInput, _ := lines[0]["inputMessages"].([]any)
	if len(firstInput) != 1 {
		t.Fatalf("expected first inputMessages on first react, got %#v", lines[0])
	}
	firstInputMessage, _ := firstInput[0].(map[string]any)
	if stringValue(firstInputMessage["content"]) != "call one" {
		t.Fatalf("expected first inputMessages on first react, got %#v", lines[0])
	}
	if lines[1]["_type"] != StepLineTypeReactTool || toIntValue(lines[1]["seq"]) != 1 {
		t.Fatalf("expected tool result to reuse seq=1, got %#v", lines[1])
	}
	if lines[2]["_type"] != StepLineTypeReact || toIntValue(lines[2]["seq"]) != 2 || lines[2]["stage"] != "execute" {
		t.Fatalf("expected second llm chat react seq=2 stage=execute, got %#v", lines[2])
	}
	secondInput, _ := lines[2]["inputMessages"].([]any)
	if len(secondInput) != 1 {
		t.Fatalf("expected second inputMessages on second react, got %#v", lines[2])
	}
	secondInputMessage, _ := secondInput[0].(map[string]any)
	if stringValue(secondInputMessage["content"]) != "call two" {
		t.Fatalf("expected second inputMessages on second react, got %#v", lines[2])
	}
}

func TestStepWriterEmbedsUsageAtStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-usage-step", "run-usage-step", "react")
	writer.OnEvent(stream.EventData{
		Type:      "content.snapshot",
		Timestamp: 2001,
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
		},
	})
	writer.OnEvent(stream.EventData{
		Type:      "debug.llmChat",
		Timestamp: 2002,
		Payload: map[string]any{
			"data": map[string]any{
				"contextWindow": map[string]any{
					"maxSize":               128000,
					"estimatedNextCallSize": 200,
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
						"timing": map[string]any{
							"firstTokenLatencyMs":   820,
							"generationDurationMs":  2380,
							"outputTokensPerSecond": 21.01,
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
	timing, _ := usage["timing"].(map[string]any)
	if toIntValue(timing["firstTokenLatencyMs"]) != 820 || toIntValue(timing["generationDurationMs"]) != 2380 {
		t.Fatalf("expected step-level timing usage, got %#v", lines[0])
	}
	if _, ok := timing["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect derived tokens/s in step-level timing, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected step-level context window, got %#v", lines[0])
	}
	if _, ok := contextWindow["currentSize"]; ok {
		t.Fatalf("did not expect usage promptTokens to become context currentSize, got %#v", contextWindow)
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

	writer := NewStepWriter(store, "chat-system-snapshot", "run-system-snapshot", "react")
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

	emitDebugLLMChat := func(request map[string]any) {
		writer.OnEvent(stream.EventData{
			Type: "debug.llmChat",
			Payload: map[string]any{
				"data": map[string]any{
					"provider":  map[string]any{"key": "mock"},
					"systemRef": systemRef,
					"contextWindow": map[string]any{
						"maxSize":               128000,
						"estimatedNextCallSize": 200,
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

	emitDebugLLMChat(requestBody)
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
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected contextWindow on step, got %#v", lines[0])
	}

	detail, err := store.LoadChat("chat-system-snapshot")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "debug.llmChat" {
			t.Fatalf("did not expect debug events in chat history, got %#v", detail.Events)
		}
	}
}

func TestStepWriterCapturesDebugLLMChatMetadata(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-llm-call", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-llm-call", "run-llm-chat", "react")
	writer.OnEvent(stream.EventData{
		Type: "debug.llmChat",
		Payload: map[string]any{
			"data": map[string]any{
				"provider":  map[string]any{"key": "mock"},
				"systemRef": map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:llm-call"},
				"contextWindow": map[string]any{
					"maxSize":               128000,
					"currentSize":           100,
					"estimatedNextCallSize": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":           100,
						"completionTokens":       50,
						"totalTokens":            150,
						"llmChatCompletionCount": 1,
						"toolCallCount":          2,
						"modelKey":               "mock-model",
						"reasoningEffort":        "HIGH",
						"timing": map[string]any{
							"firstTokenLatencyMs":  900,
							"generationDurationMs": 2100,
						},
					},
				},
				"trace": map[string]any{
					"file": "chat-llm-call/.llm-records/run-llm-chat_001.json",
					"url":  "/api/chat/llm-trace?file=chat-llm-call%2F.llm-records%2Frun-llm-chat_001.json",
				},
				"status": "ok",
				"runSeq": 1,
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
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-llm-call"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one step line, got %#v", lines)
	}
	if _, ok := lines[0]["debug"]; ok {
		t.Fatalf("did not expect debug payload in chat jsonl, got %#v", lines[0])
	}
	systemRef, _ := lines[0]["systemRef"].(map[string]any)
	if systemRef["cacheKey"] != "react:main" || systemRef["fingerprint"] != "sha256:llm-call" {
		t.Fatalf("expected systemRef from debug.llmChat, got %#v", lines[0])
	}
	usage, _ := lines[0]["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["completionTokens"]) != 50 || toIntValue(usage["totalTokens"]) != 150 ||
		toIntValue(usage["llmChatCompletionCount"]) != 1 || toIntValue(usage["toolCallCount"]) != 2 {
		t.Fatalf("expected usage from debug.llmChat, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["currentSize"]) != 100 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected contextWindow from debug.llmChat, got %#v", lines[0])
	}

	detail, err := store.LoadChat("chat-llm-call")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "debug.llmChat" {
			t.Fatalf("did not expect debug.llmChat in chat history, got %#v", detail.Events)
		}
	}
	if detail.ReplayUsage.Chat.TotalTokens != 150 || detail.ContextWindow == nil {
		t.Fatalf("expected replay usage/context window, got usage=%#v contextWindow=%#v", detail.ReplayUsage, detail.ContextWindow)
	}
	if detail.ReplayUsage.Chat.FirstTokenLatencyTotalMs != 900 ||
		detail.ReplayUsage.Chat.FirstTokenLatencyCount != 1 ||
		detail.ReplayUsage.Chat.GenerationDurationMs != 2100 {
		t.Fatalf("expected replay timing usage, got %#v", detail.ReplayUsage)
	}
}

func TestStepWriterPersistsLLMChatMetadataWithoutDebugPayload(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-debug-disabled", "run-debug-disabled", "react")
	writer.OnEvent(stream.EventData{
		Type: "debug.llmChat",
		Payload: map[string]any{
			"data": map[string]any{
				"provider":  map[string]any{"key": "mock"},
				"systemRef": map[string]any{"cacheKey": "react:main"},
				"contextWindow": map[string]any{
					"maxSize":               128000,
					"currentSize":           100,
					"estimatedNextCallSize": 200,
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
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "hello",
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
		t.Fatalf("did not expect debug payload in chat jsonl, got %#v", lines[0])
	}
	if _, ok := lines[0]["systemRef"].(map[string]any); !ok {
		t.Fatalf("expected non-debug systemRef to remain, got %#v", lines[0])
	}
	usage, _ := lines[0]["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected usage to remain, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["currentSize"]) != 100 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected context window to remain, got %#v", lines[0])
	}
}

func TestStepWriterPersistsUsageSnapshotWhenDebugEventsDisabled(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-usage-snapshot", "run-usage-snapshot", "react")
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
			"model": map[string]any{
				"key":             "mock-model",
				"reasoningEffort": "HIGH",
			},
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
					"toolCallCount":          2,
					"timing": map[string]any{
						"firstTokenLatencyMs":   820,
						"generationDurationMs":  2380,
						"outputTokensPerSecond": 21.01,
					},
					"estimatedCost": map[string]any{
						"currency":       "CNY",
						"inputCacheHit":  0.01,
						"inputCacheMiss": 0.02,
						"output":         0.03,
						"total":          0.06,
					},
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
	if lines[0]["modelKey"] != "mock-model" {
		t.Fatalf("expected persisted usage snapshot top-level modelKey, got %#v", lines[0])
	}
	if lines[0]["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected persisted usage snapshot top-level reasoningEffort, got %#v", lines[0])
	}
	usage, _ := lines[0]["usage"].(map[string]any)
	promptDetails, _ := usage["promptTokensDetails"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["completionTokens"]) != 50 || toIntValue(usage["totalTokens"]) != 150 ||
		toIntValue(promptDetails["cacheHitTokens"]) != 64 || toIntValue(promptDetails["cacheMissTokens"]) != 36 {
		t.Fatalf("expected usage snapshot to persist, got %#v", lines[0])
	}
	if toIntValue(usage["llmChatCompletionCount"]) != 1 {
		t.Fatalf("expected persisted usage snapshot llmChatCompletionCount, got %#v", lines[0])
	}
	if toIntValue(usage["toolCallCount"]) != 2 {
		t.Fatalf("expected persisted usage snapshot toolCallCount, got %#v", lines[0])
	}
	timing, _ := usage["timing"].(map[string]any)
	if toIntValue(timing["firstTokenLatencyMs"]) != 820 || toIntValue(timing["generationDurationMs"]) != 2380 {
		t.Fatalf("expected persisted usage snapshot timing, got %#v", lines[0])
	}
	if _, ok := timing["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect derived tokens/s in persisted usage snapshot, got %#v", lines[0])
	}
	assertNoStepModelMetadata(t, usage, "usage")
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	if estimatedCost["currency"] != "CNY" || estimatedCost["total"] != 0.06 {
		t.Fatalf("expected persisted usage snapshot estimated cost, got %#v", lines[0])
	}
	contextWindow, _ := lines[0]["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["currentSize"]) != 100 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected context window to persist, got %#v", lines[0])
	}
	assertNoStepModelMetadata(t, contextWindow, "contextWindow")
}

func TestStepWriterPersistsPlanExecuteMetadataOnReactStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-plan-execute-model", "run-plan-execute-model", "PLAN_EXECUTE")
	writer.OnStageMarker("execute-task-1")
	writer.OnEvent(stream.EventData{
		Type: "content.snapshot",
		Payload: map[string]any{
			"contentId": "content-1",
			"text":      "execute result",
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
					"modelKey":         "plan-model",
					"reasoningEffort":  "MEDIUM",
				},
			},
		},
	})
	writer.OnEvent(stream.EventData{Type: "run.complete"})

	lines, err := readJSONLines(store.chatJSONLPath("chat-plan-execute-model"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected one react step line, got %#v", lines)
	}
	line := lines[0]
	if line["_type"] != StepLineTypeReact || line["stage"] != "execute" || toIntValue(line["seq"]) != 1 {
		t.Fatalf("expected execute react step, got %#v", line)
	}
	if line["modelKey"] != "plan-model" || line["reasoningEffort"] != "MEDIUM" {
		t.Fatalf("expected react top-level model metadata, got %#v", line)
	}
	usage, _ := line["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected react usage, got %#v", line)
	}
	assertNoStepModelMetadata(t, usage, "react usage")
	contextWindow, _ := line["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["currentSize"]) != 100 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected react contextWindow, got %#v", line)
	}
	assertNoStepModelMetadata(t, contextWindow, "react contextWindow")
}

func TestStepWriterIgnoresEmptyUsageSnapshot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-empty-usage-snapshot", "run-empty-usage-snapshot", "react")
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

func TestStepWriterPlanningDeltasAreLiveOnly(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-planning-live-only", "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, "chat-planning-live-only", "run-planning", "coder")
	emitPlanningLifecycleForTest(writer, "chat-planning-live-only")
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath("chat-planning-live-only"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("did not expect planning delta/end events to persist, got %#v", lines)
	}
	detail, err := store.LoadChat("chat-planning-live-only")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Planning != nil || detailHasEventType(detail.Events, "planning.delta") {
		t.Fatalf("did not expect replayed planning state/events, planning=%#v events=%#v", detail.Planning, detail.Events)
	}
}

func TestLoadChatPlanRemainsFromJSONLWhenPlanTaskSnapshotExists(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-plan-task-snapshot"
	runID := "run-jsonl"
	if _, _, err := store.EnsureChat(chatID, "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 1001,
		Messages:  []StoredMessage{},
		Plan: &PlanState{
			PlanID: "jsonl_plan",
			Tasks: []PlanTaskState{{
				TaskID:      "jsonl_task",
				Description: "JSONL task",
				Status:      "completed",
			}},
		},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	snapshotDir := filepath.Join(store.ChatDir(chatID), ToolRootDirName, ToolPlanTasksDirName)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	snapshotPath := filepath.Join(snapshotDir, runID+"_plan.json")
	if err := os.WriteFile(snapshotPath, []byte(`{"version":1,"chatId":"chat-plan-task-snapshot","runId":"run-jsonl","planId":"snapshot_plan","tasks":[{"taskId":"snapshot_task","description":"Snapshot task","status":"failed"}]}`), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Plan == nil || detail.Plan.PlanID != "jsonl_plan" || len(detail.Plan.Tasks) != 1 || detail.Plan.Tasks[0].TaskID != "jsonl_task" {
		t.Fatalf("expected replay plan from JSONL, got %#v", detail.Plan)
	}
}

func TestLoadChatRestoresPlanningFromReactAwaitingPlan(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-awaiting-plan"
	runID := "run-planning"
	if _, _, err := store.EnsureChat(chatID, "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	planningFile := filepath.Join(store.ChatDir(chatID), ToolRootDirName, ToolPlansDirName, "run-planning_planning_1.md")
	if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	if err := os.WriteFile(planningFile, []byte("# Awaiting Plan\n\nBody"), 0o644); err != nil {
		t.Fatalf("write planning file: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 1004,
		Messages:  []StoredMessage{},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"awaitingId": "run-planning_coder_plan_confirm_1",
				"mode":       "plan",
				"timeout":    0,
				"plan": map[string]any{
					"id":           "confirm",
					"planningId":   "run-planning_planning_1",
					"planningFile": planningFile,
				},
			},
		},
		Type: "react",
		Seq:  1,
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Planning == nil || detail.Planning.PlanningID != "run-planning_planning_1" ||
		detail.Planning.PlanningFile != planningFile || detail.Planning.Markdown != "# Awaiting Plan\n\nBody" {
		t.Fatalf("expected planning state from awaiting plan, got planning=%#v events=%#v", detail.Planning, detail.Events)
	}
	if detailEventTypeCount(detail.Events, "planning.snapshot") != 1 {
		t.Fatalf("expected awaiting plan to synthesize one planning.snapshot, got %#v", detail.Events)
	}
	if !detailHasEventType(detail.Events, "awaiting.ask") {
		t.Fatalf("expected awaiting.ask to replay, got %#v", detail.Events)
	}
	snapshot := detailEventByType(detail.Events, "planning.snapshot")
	if snapshot.String("planningId") != "run-planning_planning_1" ||
		snapshot.String("planningFile") != planningFile ||
		snapshot.String("text") != "# Awaiting Plan\n\nBody" {
		t.Fatalf("unexpected synthesized planning.snapshot: %#v", snapshot)
	}
	snapshotIndex := detailEventTypeIndex(detail.Events, "planning.snapshot")
	awaitingIndex := detailEventTypeIndex(detail.Events, "awaiting.ask")
	if snapshotIndex < 0 || awaitingIndex < 0 || snapshotIndex >= awaitingIndex {
		t.Fatalf("expected planning.snapshot before awaiting.ask, got %#v", detail.Events)
	}
}

func TestLoadChatSynthesizesPlanningSnapshotsForMultipleAwaitingPlans(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-awaiting-plan-replan"
	runID := "run-planning"
	if _, _, err := store.EnsureChat(chatID, "coder", "", "plan it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	firstFile := filepath.Join(store.ChatDir(chatID), ToolRootDirName, ToolPlansDirName, "run-planning_planning_1.md")
	secondFile := filepath.Join(store.ChatDir(chatID), ToolRootDirName, ToolPlansDirName, "run-planning_planning_2.md")
	if err := os.MkdirAll(filepath.Dir(firstFile), 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	if err := os.WriteFile(firstFile, []byte("# First\n\nBody"), 0o644); err != nil {
		t.Fatalf("write first planning file: %v", err)
	}
	if err := os.WriteFile(secondFile, []byte("# Second\n\nBody"), 0o644); err != nil {
		t.Fatalf("write second planning file: %v", err)
	}

	appendPlanAwaitingStepForTest(t, store, chatID, runID, 1004, "run-planning_coder_plan_confirm_1", "run-planning_planning_1", firstFile)
	appendPlanAwaitingStepForTest(t, store, chatID, runID, 1008, "run-planning_coder_plan_confirm_2", "run-planning_planning_2", secondFile)

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.Planning == nil || detail.Planning.PlanningID != "run-planning_planning_2" ||
		detail.Planning.PlanningFile != secondFile || detail.Planning.Markdown != "# Second\n\nBody" {
		t.Fatalf("expected latest planning state from second awaiting plan, got planning=%#v events=%#v", detail.Planning, detail.Events)
	}
	if detailEventTypeCount(detail.Events, "planning.snapshot") != 2 || detailEventTypeCount(detail.Events, "awaiting.ask") != 2 {
		t.Fatalf("expected two planning snapshots and two awaiting asks, got %#v", detail.Events)
	}

	firstSnapshotIndex := detailEventIndexByTypeAndString(detail.Events, "planning.snapshot", "planningId", "run-planning_planning_1")
	firstAwaitingIndex := detailEventIndexByTypeAndString(detail.Events, "awaiting.ask", "awaitingId", "run-planning_coder_plan_confirm_1")
	secondSnapshotIndex := detailEventIndexByTypeAndString(detail.Events, "planning.snapshot", "planningId", "run-planning_planning_2")
	secondAwaitingIndex := detailEventIndexByTypeAndString(detail.Events, "awaiting.ask", "awaitingId", "run-planning_coder_plan_confirm_2")
	if firstSnapshotIndex < 0 || firstAwaitingIndex < 0 || secondSnapshotIndex < 0 || secondAwaitingIndex < 0 ||
		firstSnapshotIndex >= firstAwaitingIndex || firstAwaitingIndex >= secondSnapshotIndex || secondSnapshotIndex >= secondAwaitingIndex {
		t.Fatalf("unexpected planning snapshot/awaiting ordering: %#v", detail.Events)
	}
}

func emitPlanningLifecycleForTest(writer *StepWriter, chatID string) {
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

func appendPlanAwaitingStepForTest(t *testing.T, store *FileStore, chatID string, runID string, updatedAt int64, awaitingID string, planningID string, planningFile string) {
	t.Helper()
	if err := store.AppendStepLine(chatID, StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: updatedAt,
		Messages:  []StoredMessage{},
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"awaitingId": awaitingID,
				"mode":       "plan",
				"timeout":    0,
				"plan": map[string]any{
					"id":           "confirm",
					"planningId":   planningID,
					"planningFile": planningFile,
				},
			},
		},
		Type: "react",
	}); err != nil {
		t.Fatalf("append step line: %v", err)
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

func detailEventTypeIndex(events []stream.EventData, eventType string) int {
	for idx, event := range events {
		if event.Type == eventType {
			return idx
		}
	}
	return -1
}

func detailEventIndexByTypeAndString(events []stream.EventData, eventType string, key string, value string) int {
	for idx, event := range events {
		if event.Type == eventType && event.String(key) == value {
			return idx
		}
	}
	return -1
}

func detailEventByType(events []stream.EventData, eventType string) stream.EventData {
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	return stream.EventData{}
}

func assertNoStepModelMetadata(t *testing.T, values map[string]any, label string) {
	t.Helper()
	for _, key := range []string{"modelKey", "reasoningEffort"} {
		if _, ok := values[key]; ok {
			t.Fatalf("did not expect %s in %s: %#v", key, label, values)
		}
	}
}

func TestStepWriterPersistsTaskScopedUsageAndSlimMetadataWithoutDebugPayload(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-task-debug", "run-task-debug", "react")
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
		Type: "debug.llmChat",
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
					"maxSize":               128000,
					"estimatedNextCallSize": 200,
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
		Type: "debug.llmChat",
		Payload: map[string]any{
			"taskId": "task_1",
			"data": map[string]any{
				"contextWindow": map[string]any{
					"maxSize":               128000,
					"estimatedNextCallSize": 200,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
						"modelKey":         "task-model",
						"reasoningEffort":  "LOW",
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
	if taskLine["modelKey"] != "task-model" || taskLine["reasoningEffort"] != "LOW" {
		t.Fatalf("expected task top-level model metadata, got %#v", taskLine)
	}
	usage, _ := taskLine["usage"].(map[string]any)
	if toIntValue(usage["promptTokens"]) != 100 || toIntValue(usage["totalTokens"]) != 150 {
		t.Fatalf("expected task usage, got %#v", taskLine)
	}
	assertNoStepModelMetadata(t, usage, "task usage")
	contextWindow, _ := taskLine["contextWindow"].(map[string]any)
	if toIntValue(contextWindow["maxSize"]) != 128000 || toIntValue(contextWindow["estimatedNextCallSize"]) != 200 {
		t.Fatalf("expected task contextWindow, got %#v", taskLine)
	}
	if _, ok := contextWindow["currentSize"]; ok {
		t.Fatalf("did not expect task usage promptTokens to become context currentSize, got %#v", contextWindow)
	}
	assertNoStepModelMetadata(t, contextWindow, "task contextWindow")
	if _, ok := lines[1]["debug"]; ok {
		t.Fatalf("did not expect child debug to pollute root step, got %#v", lines[1])
	}
}

func TestStepWriterSubTaskReactFlushOrder(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-task-order", "run-task-order", "react")
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

func TestRawMessagesIncludeReferenceContext(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-reference-context", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-reference-context", QueryLine{
		Type:      "query",
		ChatID:    "chat-reference-context",
		RunID:     "run-1",
		UpdatedAt: 2,
		Query: map[string]any{
			"role":    "user",
			"message": "分析 #{r01}",
			"references": []map[string]any{{
				"id":        "r01",
				"type":      "file",
				"name":      "sales.csv",
				"path":      "/workspace/sales.csv",
				"mimeType":  "text/csv",
				"sizeBytes": 537,
			}},
		},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	messages, err := store.LoadRawMessages("chat-reference-context", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("expected one user message, got %#v", messages)
	}
	content, _ := messages[0]["content"].(string)
	for _, expected := range []string{
		"[References]",
		"id: r01",
		"type: file",
		"name: sales.csv",
		"path: /workspace/sales.csv",
		"mimeType: text/csv",
		"sizeBytes: 537",
		"[User message]\n分析 #{r01}",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %q in content, got %q", expected, content)
		}
	}
}

func TestRawMessagesPreferQueryMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-query-messages", "agent", "", "raw"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-query-messages", QueryLine{
		Type:      "query",
		ChatID:    "chat-query-messages",
		RunID:     "run-1",
		UpdatedAt: 2,
		Query: map[string]any{
			"role":    "user",
			"message": "raw user text",
			"references": []map[string]any{{
				"id":   "r01",
				"name": "sales.csv",
			}},
		},
		Messages: []map[string]any{{
			"role":    "user",
			"content": "canonical model text",
		}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	messages, err := store.LoadRawMessages("chat-query-messages", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	if len(messages) != 1 || messages[0]["role"] != "user" || messages[0]["content"] != "canonical model text" {
		t.Fatalf("expected canonical query messages, got %#v", messages)
	}
	if strings.Contains(messages[0]["content"].(string), "[References]") {
		t.Fatalf("did not expect fallback reference formatting when query messages exist: %#v", messages)
	}
}

func TestLoadRawMessagesMapsAutomationAndSystemQueryRolesToUser(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-role-raw", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	for idx, item := range []struct {
		runID   string
		role    string
		message string
	}{
		{runID: "run-auto", role: "automation", message: "automation hello"},
		{runID: "run-system", role: "system", message: "system hello"},
	} {
		if err := store.AppendQueryLine("chat-role-raw", QueryLine{
			Type:      "query",
			ChatID:    "chat-role-raw",
			RunID:     item.runID,
			UpdatedAt: int64(idx + 1),
			Query: map[string]any{
				"role":    item.role,
				"message": item.message,
			},
		}); err != nil {
			t.Fatalf("append query: %v", err)
		}
	}
	messages, err := store.LoadRawMessages("chat-role-raw", 5)
	if err != nil {
		t.Fatalf("load raw messages: %v", err)
	}
	wants := []string{"[automation request]\nautomation hello", "[system request]\nsystem hello"}
	if len(messages) != len(wants) {
		t.Fatalf("expected raw messages, got %#v", messages)
	}
	for idx, want := range wants {
		if messages[idx]["role"] != "user" || messages[idx]["content"] != want {
			t.Fatalf("unexpected raw message %d: %#v", idx, messages[idx])
		}
	}
}

func TestStepWriterPersistsQueryMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-query-current-messages", "agent", "", "raw user text"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-query-current-messages", "run-1", "react")
	writer.SetPendingQueryMessages([]map[string]any{{
		"role":    "user",
		"content": "[References]\n- id: r01\n  name: sales.csv\n\n[User message]\nraw user text",
	}})
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  "chat-query-current-messages",
			"runId":   "run-1",
			"role":    "user",
			"message": "raw user text",
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath("chat-query-current-messages"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 || lines[0]["_type"] != "query" {
		t.Fatalf("expected one query line, got %#v", lines)
	}
	query, _ := lines[0]["query"].(map[string]any)
	if query["message"] != "raw user text" {
		t.Fatalf("expected raw query message to remain unchanged, got %#v", query)
	}
	if _, exists := query["messages"]; exists {
		t.Fatalf("did not expect model messages inside query payload, got %#v", query)
	}
	rawMessages, _ := lines[0]["messages"].([]any)
	if len(rawMessages) != 1 {
		t.Fatalf("expected top-level query messages, got %#v", lines[0])
	}
	message, _ := rawMessages[0].(map[string]any)
	if message["role"] != "user" || !strings.Contains(message["content"].(string), "[User message]\nraw user text") {
		t.Fatalf("unexpected query model message %#v", message)
	}
}

func TestStepWriterPersistsSyntheticQueryAfterInitialQuery(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-synthetic-query"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "raw user text"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, chatID, "run-1", "react")
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "raw user text"}})
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  chatID,
			"runId":   "run-1",
			"role":    "user",
			"message": "raw user text",
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       42,
		Type:      "request.query",
		Timestamp: 1002,
		Payload: map[string]any{
			"chatId":    chatID,
			"runId":     "run-1",
			"role":      "user",
			"message":   "执行计划",
			"synthetic": true,
			"stage":     "coder-execute",
			"source":    "coder-plan-approve",
			"messages": []any{map[string]any{
				"role":    "user",
				"content": "Execute the confirmed CODER plan.\n\nConfirmed plan:\n# Plan",
			}},
			"systems": []any{map[string]any{
				"cacheKey":      "coder:execute",
				"fingerprint":   "sha256:execute",
				"systemMessage": map[string]any{"role": "system", "content": "execute system"},
				"tools":         []any{},
				"model":         map[string]any{"key": "mock-model"},
			}},
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected initial and synthetic query lines, got %#v", lines)
	}
	synthetic := lines[1]
	if synthetic["_type"] != "query" || toIntValue(synthetic["liveSeq"]) != 42 {
		t.Fatalf("expected synthetic query line with liveSeq, got %#v", synthetic)
	}
	query, _ := synthetic["query"].(map[string]any)
	if query["message"] != "执行计划" || query["synthetic"] != true ||
		query["stage"] != "coder-execute" || query["source"] != "coder-plan-approve" {
		t.Fatalf("unexpected synthetic query payload %#v", query)
	}
	if _, ok := query["messages"]; ok {
		t.Fatalf("did not expect messages inside query payload, got %#v", query)
	}
	if _, ok := query["systems"]; ok {
		t.Fatalf("did not expect systems inside query payload, got %#v", query)
	}
	rawSystems, _ := synthetic["systems"].([]any)
	if len(rawSystems) != 1 {
		t.Fatalf("expected top-level synthetic query systems, got %#v", synthetic)
	}
	system, _ := rawSystems[0].(map[string]any)
	if system["cacheKey"] != "coder:execute" || system["fingerprint"] != "sha256:execute" {
		t.Fatalf("unexpected synthetic query system %#v", system)
	}
	rawMessages, _ := synthetic["messages"].([]any)
	if len(rawMessages) != 1 {
		t.Fatalf("expected top-level synthetic query messages, got %#v", synthetic)
	}
	message, _ := rawMessages[0].(map[string]any)
	if message["role"] != "user" || !strings.Contains(stringValue(message["content"]), "Confirmed plan:\n# Plan") {
		t.Fatalf("unexpected synthetic query model message %#v", message)
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

	writer := NewStepWriter(store, "chat-query-system-init", "run-1", "react")
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

func TestStepWriterPersistsQueryWithSystemInitsWithoutHiddenFlag(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-query-system-init-no-hidden", "agent", "", "system hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, "chat-query-system-init-no-hidden", "run-system", "react")
	writer.SetPendingSystemInits([]QueryLineSystemInit{{
		Fingerprint:   "sha256:system",
		CacheKey:      "react:main",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{map[string]any{"type": "function"}},
	}})
	writer.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: 1001,
		Payload: map[string]any{
			"chatId":  "chat-query-system-init-no-hidden",
			"runId":   "run-system",
			"role":    "system",
			"message": "system hello",
			"scene": map[string]any{
				"url":   "https://example.com/app",
				"title": "demo",
			},
		},
	})

	lines, err := readJSONLines(store.chatJSONLPath("chat-query-system-init-no-hidden"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	if len(lines) != 1 || lines[0]["_type"] != "query" {
		t.Fatalf("expected one query line, got %#v", lines)
	}
	if _, ok := lines[0]["hidden"]; ok {
		t.Fatalf("did not expect hidden on query line, got %#v", lines[0])
	}
	systems, _ := lines[0]["systems"].([]any)
	if len(systems) != 1 {
		t.Fatalf("expected query to keep inline systems, got %#v", lines[0])
	}
	system, _ := systems[0].(map[string]any)
	if system["cacheKey"] != "react:main" || system["fingerprint"] != "sha256:system" {
		t.Fatalf("unexpected inline system cache %#v", system)
	}

	detail, err := store.LoadChat("chat-query-system-init-no-hidden")
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
	if _, ok := queryEvent.Payload["hidden"]; ok {
		t.Fatalf("did not expect replayed request.query hidden, got %#v", queryEvent)
	}
	if queryEvent.String("role") != "system" {
		t.Fatalf("expected replayed request.query role=system, got %#v", queryEvent)
	}
	scene, _ := queryEvent.Value("scene").(map[string]any)
	if scene["url"] != "https://example.com/app" || scene["title"] != "demo" {
		t.Fatalf("expected replayed request.query scene, got %#v", queryEvent)
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

	writer := NewStepWriter(store, "chat-query-no-system-init", "run-1", "react")
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

	writer := NewStepWriter(store, "chat-awaiting-standalone", "run-awaiting-standalone", "react")
	writer.OnEvent(stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 3001,
		Payload: map[string]any{
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120,
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
		t.Fatalf("expected standalone awaiting step line, got %#v", lines)
	}
	if lines[0]["_type"] != "react" || toIntValue(lines[0]["updatedAt"]) != 3001 {
		t.Fatalf("expected awaiting step line updatedAt=3001, got %#v", lines[0])
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

func TestStepWriterFlushesAwaitingAskImmediatelyForAllModes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	tests := []struct {
		mode string
		key  string
		data any
	}{
		{
			mode: "question",
			key:  "questions",
			data: []any{map[string]any{"id": "q1", "question": "Need input", "type": "text"}},
		},
		{
			mode: "approval",
			key:  "approvals",
			data: []any{map[string]any{"id": "cmd-1", "command": "deploy"}},
		},
		{
			mode: "form",
			key:  "forms",
			data: []any{map[string]any{"id": "form-1", "title": "Review"}},
		},
		{
			mode: "plan",
			key:  "plan",
			data: map[string]any{"title": "Plan", "steps": []any{"one"}},
		},
	}

	for _, tc := range tests {
		chatID := "chat-awaiting-" + tc.mode
		runID := "run-awaiting-" + tc.mode
		writer := NewStepWriter(store, chatID, runID, "react")
		payload := map[string]any{
			"awaitingId": "await-" + tc.mode,
			"mode":       tc.mode,
			"timeout":    120,
			"runId":      runID,
			tc.key:       tc.data,
		}
		writer.OnEvent(stream.EventData{
			Type:      "awaiting.ask",
			Timestamp: 4000,
			Payload:   payload,
		})

		lines, err := readJSONLines(store.chatJSONLPath(chatID))
		if err != nil {
			t.Fatalf("read %s jsonl: %v", tc.mode, err)
		}
		if len(lines) != 1 || lines[0]["_type"] != "react" {
			t.Fatalf("expected one canonical step line for %s, got %#v", tc.mode, lines)
		}
		if _, ok := lines[0]["event"]; ok {
			t.Fatalf("did not expect event payload for %s, got %#v", tc.mode, lines[0])
		}
		awaiting, _ := lines[0]["awaiting"].([]any)
		if len(awaiting) != 1 {
			t.Fatalf("expected awaiting payload for %s, got %#v", tc.mode, lines[0])
		}
		item, _ := awaiting[0].(map[string]any)
		if item["type"] != "awaiting.ask" || item["mode"] != tc.mode || item[tc.key] == nil {
			t.Fatalf("unexpected awaiting item for %s: %#v", tc.mode, item)
		}
	}
}

func TestStepWriterPersistsInlineApprovalMessage(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	writer := NewStepWriter(store, "chat-approval-step", "run-approval-step", "react")
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
	if len(lines) != 2 {
		t.Fatalf("expected tool call and tool result step lines, got %#v", lines)
	}
	toolStep := lines[1]
	if toolStep["_type"] != StepLineTypeReactTool || toIntValue(toolStep["seq"]) != 1 {
		t.Fatalf("expected tool result step to reuse seq=1, got %#v", toolStep)
	}
	if _, ok := toolStep["approval"]; ok {
		t.Fatalf("did not expect top-level approval sidecar on step line, got %#v", toolStep)
	}
	messages, _ := toolStep["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected tool result and approval message, got %#v", toolStep)
	}
	approvalMessage, _ := messages[1].(map[string]any)
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
	writer := NewStepWriter(store, "chat-form-approval-step", "run-form-approval-step", "react")
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
	if len(lines) != 2 {
		t.Fatalf("expected tool call and tool result step lines, got %#v", lines)
	}
	toolStep := lines[1]
	if toolStep["_type"] != StepLineTypeReactTool || toIntValue(toolStep["seq"]) != 1 {
		t.Fatalf("expected tool result step to reuse seq=1, got %#v", toolStep)
	}
	if _, ok := toolStep["approval"]; ok {
		t.Fatalf("did not expect top-level approval sidecar on step line, got %#v", toolStep)
	}
	messages, _ := toolStep["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected inline approval message after tool result, got %#v", toolStep)
	}
	approvalMessage, _ := messages[1].(map[string]any)
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

func TestReactToolResultLinesReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lineType string
	}{
		{name: "react-tool", lineType: StepLineTypeReactTool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("new file store: %v", err)
			}
			chatID := "chat-" + tc.name
			runID := "run-" + tc.name
			if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
				t.Fatalf("ensure chat: %v", err)
			}

			assistantTs := int64(6101)
			resultTs := int64(6102)
			if err := store.AppendStepLine(chatID, StepLine{
				ChatID:    chatID,
				RunID:     runID,
				UpdatedAt: 6101,
				LiveSeq:   7,
				Type:      StepLineTypeReact,
				Seq:       1,
				Messages: []StoredMessage{{
					Role: "assistant",
					ToolCalls: []StoredToolCall{{
						ID:   "tool-1",
						Type: "function",
						Function: StoredFunction{
							Name:      "ask_user_question",
							Arguments: `{"questions":[]}`,
						},
					}},
					ToolID: "tool-1",
					MsgID:  "msg-1",
					Ts:     &assistantTs,
				}},
			}); err != nil {
				t.Fatalf("append assistant step: %v", err)
			}
			if err := store.AppendStepLine(chatID, StepLine{
				ChatID:    chatID,
				RunID:     runID,
				UpdatedAt: 6102,
				LiveSeq:   8,
				Type:      tc.lineType,
				Seq:       1,
				Messages: []StoredMessage{{
					Role:       "tool",
					Name:       "ask_user_question",
					ToolCallID: "tool-1",
					Content:    []ContentPart{{Type: "text", Text: `{"answers":[{"id":"q1","answer":"ok"}]}`}},
					ToolID:     "tool-1",
					Ts:         &resultTs,
				}},
			}); err != nil {
				t.Fatalf("append tool result step: %v", err)
			}

			rawMessages, err := store.LoadRawMessages(chatID, 10)
			if err != nil {
				t.Fatalf("load raw messages: %v", err)
			}
			if len(rawMessages) != 2 || rawMessages[0]["role"] != "assistant" || rawMessages[1]["role"] != "tool" {
				t.Fatalf("expected assistant -> tool raw messages, got %#v", rawMessages)
			}
			if rawMessages[1]["content"] != `{"answers":[{"id":"q1","answer":"ok"}]}` {
				t.Fatalf("unexpected tool raw message content %#v", rawMessages[1])
			}

			detail, err := store.LoadChat(chatID)
			if err != nil {
				t.Fatalf("load chat: %v", err)
			}
			foundSnapshot := false
			foundResult := false
			for _, event := range detail.Events {
				switch event.Type {
				case "tool.snapshot":
					if event.String("toolId") == "tool-1" && event.String("toolName") == "ask_user_question" {
						foundSnapshot = true
					}
				case "tool.result":
					if event.String("toolId") == "tool-1" && event.String("result") == `{"answers":[{"id":"q1","answer":"ok"}]}` {
						foundResult = true
					}
				}
			}
			if !foundSnapshot || !foundResult {
				t.Fatalf("expected tool snapshot/result replay, got %#v", detail.Events)
			}
		})
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

	writer := NewStepWriter(store, "chat-subagent-raw", "run-subagent-raw", "react")
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

	writer := NewStepWriter(store, "chat-task-upsert", "run-task-upsert", "react")
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

	writer := NewStepWriter(store, "chat-root-upsert", "run-root-upsert", "react")
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

	writer := NewStepWriter(store, "chat-subagent-no-infer", "run-subagent-no-infer", "react")
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

func TestLoadChatIgnoresQuestionAwaitingAskEventLines(t *testing.T) {
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
			"timeout":    120,
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

	if len(detail.Events) != 6 {
		t.Fatalf("expected 6 replayed events, got %d: %#v", len(detail.Events), detail.Events)
	}

	expectedTypes := []string{
		"chat.start",
		"request.query",
		"run.start",
		"request.submit",
		"awaiting.answer",
		"run.complete",
	}
	for i, eventType := range expectedTypes {
		if detail.Events[i].Type != eventType {
			t.Fatalf("expected event %d to be %s, got %#v", i, eventType, detail.Events[i])
		}
	}

	submit := detail.Events[3]
	submitParams, _ := submit.Value("params").([]any)
	if submit.String("awaitingId") != "tool-1" || len(submitParams) != 2 {
		t.Fatalf("unexpected request.submit replay %#v", submit)
	}
	answer := detail.Events[4]
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
			"durationMs": int64(88),
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
		"request.query",
		"run.start",
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
	if detail.Events[4].Value("durationMs") != float64(88) {
		t.Fatalf("expected awaiting.answer durationMs to replay, got %#v", detail.Events[4])
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
				"timeout":    120,
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
		"request.query",
		"run.start",
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

func TestLoadChatIgnoresEventLineAwaitingAsk(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-awaiting-event-ignored", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-awaiting-event-ignored", QueryLine{
		ChatID:    "chat-awaiting-event-ignored",
		RunID:     "run-awaiting-event-ignored",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-awaiting-event-ignored",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := store.AppendEventLine("chat-awaiting-event-ignored", EventLine{
		ChatID:    "chat-awaiting-event-ignored",
		RunID:     "run-awaiting-event-ignored",
		UpdatedAt: 1001,
		Type:      "event",
		Event: map[string]any{
			"type":       "awaiting.ask",
			"awaitingId": "tool-1",
			"mode":       "question",
			"timeout":    120,
			"runId":      "run-awaiting-event-ignored",
			"questions": []any{
				map[string]any{"id": "q1", "question": "How many?", "type": "number"},
			},
		},
	}); err != nil {
		t.Fatalf("append awaiting event line: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-event-ignored")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "awaiting.ask" {
			t.Fatalf("did not expect event-line awaiting.ask to replay, got %#v", detail.Events)
		}
	}
}

func TestLoadChatDoesNotSynthesizeRunCompleteForPendingAwaiting(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-awaiting-pending", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-awaiting-pending", QueryLine{
		ChatID:    "chat-awaiting-pending",
		RunID:     "run-awaiting-pending",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":  "chat-awaiting-pending",
			"runId":   "run-awaiting-pending",
			"message": "please ask me",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := store.AppendStepLine("chat-awaiting-pending", StepLine{
		ChatID:    "chat-awaiting-pending",
		RunID:     "run-awaiting-pending",
		UpdatedAt: 1001,
		Type:      "react",
		Awaiting: []map[string]any{{
			"type":       "awaiting.ask",
			"timestamp":  1001,
			"awaitingId": "tool-1",
			"runId":      "run-awaiting-pending",
			"mode":       "question",
			"timeout":    120,
			"questions": []any{
				map[string]any{"id": "q1", "question": "How many?", "type": "number"},
			},
		}},
	}); err != nil {
		t.Fatalf("append awaiting step: %v", err)
	}
	if err := store.SetPendingAwaiting("chat-awaiting-pending", PendingAwaiting{
		AwaitingID: "tool-1",
		RunID:      "run-awaiting-pending",
		Mode:       "question",
		CreatedAt:  1001,
	}); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}

	detail, err := store.LoadChat("chat-awaiting-pending")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	var sawAwaiting bool
	for _, event := range detail.Events {
		if event.Type == "awaiting.ask" {
			sawAwaiting = true
		}
		if event.Type == "run.complete" && event.String("runId") == "run-awaiting-pending" {
			t.Fatalf("did not expect synthesized run.complete while awaiting is pending, got %#v", detail.Events)
		}
	}
	if !sawAwaiting {
		t.Fatalf("expected replayed pending awaiting.ask, got %#v", detail.Events)
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
		"request.query",
		"run.start",
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
		ChatID:          "chat-step-usage",
		RunID:           "run-step-usage",
		UpdatedAt:       1003,
		ModelKey:        "line-model",
		ReasoningEffort: "HIGH",
		Type:            "react",
		Seq:             1,
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
			"toolCallCount":          2,
		},
		ContextWindow: map[string]any{
			"maxSize":               128000,
			"currentSize":           100,
			"estimatedNextCallSize": 200,
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
		"request.query",
		"run.start",
		"content.snapshot",
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

	if toIntValue(detail.ContextWindow["maxSize"]) != 128000 || toIntValue(detail.ContextWindow["currentSize"]) != 100 || toIntValue(detail.ContextWindow["estimatedNextCallSize"]) != 200 ||
		detail.ContextWindow["modelKey"] != "line-model" || detail.ContextWindow["reasoningEffort"] != "HIGH" {
		t.Fatalf("unexpected detail context window %#v", detail.ContextWindow)
	}

	if _, exists := detail.Events[4].Value("usage").(map[string]any); exists {
		t.Fatalf("did not expect synthesized run.complete usage %#v", detail.Events[4])
	}
	if detail.ReplayUsage.LastRunID != "run-step-usage" ||
		detail.ReplayUsage.LastRun.PromptTokens != 100 ||
		detail.ReplayUsage.LastRun.CompletionTokens != 50 ||
		detail.ReplayUsage.LastRun.TotalTokens != 150 ||
		detail.ReplayUsage.LastRun.LlmChatCompletionCount != 1 ||
		detail.ReplayUsage.LastRun.ToolCallCount != 2 {
		t.Fatalf("unexpected replay last run usage %#v", detail.ReplayUsage)
	}
	if detail.ReplayUsage.Chat.PromptTokens != 100 ||
		detail.ReplayUsage.Chat.CompletionTokens != 50 ||
		detail.ReplayUsage.Chat.TotalTokens != 150 ||
		detail.ReplayUsage.Chat.LlmChatCompletionCount != 1 ||
		detail.ReplayUsage.Chat.ToolCallCount != 2 {
		t.Fatalf("unexpected replay chat usage %#v", detail.ReplayUsage)
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
			"maxSize":               128000,
			"estimatedNextCallSize": 5703,
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
	if toIntValue(detail.ContextWindow["maxSize"]) != 128000 || toIntValue(detail.ContextWindow["estimatedNextCallSize"]) != 5703 {
		t.Fatalf("expected detail context window from empty step usage, got %#v", detail.ContextWindow)
	}
	if detail.ReplayUsage.LastRunID != "" || detail.ReplayUsage.Chat.TotalTokens != 0 || detail.ReplayUsage.Chat.LlmChatCompletionCount != 0 {
		t.Fatalf("did not expect replay usage from empty provider usage, got %#v", detail.ReplayUsage)
	}
}

func TestLoadChatReadsEstimatedCostFromStepLevel(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-step-cost", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-step-cost", QueryLine{
		ChatID:    "chat-step-cost",
		RunID:     "run-step-cost",
		UpdatedAt: 1000,
		Query:     map[string]any{"chatId": "chat-step-cost", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	if err := store.AppendStepLine("chat-step-cost", StepLine{
		ChatID:    "chat-step-cost",
		RunID:     "run-step-cost",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: textContent("answer"),
		}},
		Usage: map[string]any{
			"promptTokens":           100,
			"completionTokens":       50,
			"totalTokens":            150,
			"llmChatCompletionCount": 1,
			"estimatedCost": map[string]any{
				"currency":       "CNY",
				"inputCacheHit":  0.01,
				"inputCacheMiss": 0.02,
				"output":         0.03,
				"total":          0.06,
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-step-cost")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	if detail.ReplayUsage.LastRunID != "run-step-cost" {
		t.Fatalf("expected last run ID run-step-cost, got %q", detail.ReplayUsage.LastRunID)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "CNY" {
		t.Fatalf("expected last run cost currency CNY, got %q", detail.ReplayUsage.LastRun.EstimatedCostCurrency)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostTotal != 0.06 {
		t.Fatalf("expected last run cost total 0.06, got %f", detail.ReplayUsage.LastRun.EstimatedCostTotal)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostInputHit != 0.01 {
		t.Fatalf("expected last run cost input hit 0.01, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputHit)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostInputMiss != 0.02 {
		t.Fatalf("expected last run cost input miss 0.02, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputMiss)
	}
	if absFloat(detail.ReplayUsage.LastRun.EstimatedCostOutput-0.03) > 0.0001 {
		t.Fatalf("expected last run cost output ~0.03, got %f", detail.ReplayUsage.LastRun.EstimatedCostOutput)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "CNY" {
		t.Fatalf("expected chat cost currency CNY, got %q", detail.ReplayUsage.Chat.EstimatedCostCurrency)
	}
	if detail.ReplayUsage.Chat.EstimatedCostTotal != 0.06 {
		t.Fatalf("expected chat cost total 0.06, got %f", detail.ReplayUsage.Chat.EstimatedCostTotal)
	}
}

func TestLoadChatAccumulatesMultipleStepEstimatedCosts(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-multi-cost", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-multi-cost", QueryLine{
		ChatID:    "chat-multi-cost",
		RunID:     "run-multi-cost",
		UpdatedAt: 1000,
		Query:     map[string]any{"chatId": "chat-multi-cost", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	for i, cost := range []map[string]any{
		{"currency": "USD", "inputCacheHit": 0.005, "inputCacheMiss": 0.01, "output": 0.02, "total": 0.035},
		{"currency": "USD", "inputCacheHit": 0.003, "inputCacheMiss": 0.005, "output": 0.01, "total": 0.018},
	} {
		if err := store.AppendStepLine("chat-multi-cost", StepLine{
			ChatID:    "chat-multi-cost",
			RunID:     "run-multi-cost",
			UpdatedAt: int64(1004 + i),
			Type:      "react",
			Seq:       i + 1,
			Messages: []StoredMessage{{
				Role:    "assistant",
				Content: textContent("answer"),
			}},
			Usage: map[string]any{
				"promptTokens":           60,
				"completionTokens":       30,
				"totalTokens":            90,
				"llmChatCompletionCount": 1,
				"estimatedCost":          cost,
			},
		}); err != nil {
			t.Fatalf("append step line %d: %v", i, err)
		}
	}

	detail, err := store.LoadChat("chat-multi-cost")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "USD" {
		t.Fatalf("expected last run cost currency USD, got %q", detail.ReplayUsage.LastRun.EstimatedCostCurrency)
	}
	if absFloat(detail.ReplayUsage.LastRun.EstimatedCostTotal-0.053) > 0.0001 {
		t.Fatalf("expected last run cost total ~0.053, got %f", detail.ReplayUsage.LastRun.EstimatedCostTotal)
	}
	if absFloat(detail.ReplayUsage.LastRun.EstimatedCostInputHit-0.008) > 0.0001 {
		t.Fatalf("expected last run cost input hit ~0.008, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputHit)
	}
	if absFloat(detail.ReplayUsage.LastRun.EstimatedCostInputMiss-0.015) > 0.0001 {
		t.Fatalf("expected last run cost input miss ~0.015, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputMiss)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostOutput != 0.03 {
		t.Fatalf("expected last run cost output 0.03, got %f", detail.ReplayUsage.LastRun.EstimatedCostOutput)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "USD" {
		t.Fatalf("expected chat cost currency USD, got %q", detail.ReplayUsage.Chat.EstimatedCostCurrency)
	}
	if absFloat(detail.ReplayUsage.Chat.EstimatedCostTotal-0.053) > 0.0001 {
		t.Fatalf("expected chat cost total ~0.053, got %f", detail.ReplayUsage.Chat.EstimatedCostTotal)
	}
}

func TestLoadChatAccumulatesEstimatedCostWithoutTokens(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-cost-only", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	if err := store.AppendQueryLine("chat-cost-only", QueryLine{
		ChatID:    "chat-cost-only",
		RunID:     "run-cost-only",
		UpdatedAt: 1000,
		Query:     map[string]any{"chatId": "chat-cost-only", "message": "hello"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	// Step with ONLY estimatedCost (no provider tokens)
	if err := store.AppendStepLine("chat-cost-only", StepLine{
		ChatID:    "chat-cost-only",
		RunID:     "run-cost-only",
		UpdatedAt: 1003,
		Type:      "react",
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: textContent("answer"),
		}},
		Usage: map[string]any{
			"estimatedCost": map[string]any{
				"currency":       "CNY",
				"inputCacheHit":  0.005,
				"inputCacheMiss": 0.01,
				"output":         0.02,
				"total":          0.035,
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}

	detail, err := store.LoadChat("chat-cost-only")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "CNY" {
		t.Fatalf("expected cost currency CNY, got %q", detail.ReplayUsage.LastRun.EstimatedCostCurrency)
	}
	if absFloat(detail.ReplayUsage.LastRun.EstimatedCostTotal-0.035) > 0.0001 {
		t.Fatalf("expected last run cost total ~0.035, got %f", detail.ReplayUsage.LastRun.EstimatedCostTotal)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostInputHit != 0.005 {
		t.Fatalf("expected cost input hit 0.005, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputHit)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostInputMiss != 0.01 {
		t.Fatalf("expected cost input miss 0.01, got %f", detail.ReplayUsage.LastRun.EstimatedCostInputMiss)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostOutput != 0.02 {
		t.Fatalf("expected cost output 0.02, got %f", detail.ReplayUsage.LastRun.EstimatedCostOutput)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "CNY" {
		t.Fatalf("expected chat cost currency CNY, got %q", detail.ReplayUsage.Chat.EstimatedCostCurrency)
	}
	if absFloat(detail.ReplayUsage.Chat.EstimatedCostTotal-0.035) > 0.0001 {
		t.Fatalf("expected chat cost total ~0.035, got %f", detail.ReplayUsage.Chat.EstimatedCostTotal)
	}
}

func TestLoadChatIgnoresApprovalAwaitingAskEventLines(t *testing.T) {
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
			"timeout":    120,
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
	if foundAwaitAsk {
		t.Fatalf("did not expect approval awaiting.ask event-line replay, got %#v", detail.Events)
	}
	if !foundAwaitAnswer {
		t.Fatalf("expected approval awaiting.answer replay, got %#v", detail.Events)
	}
}

func TestLoadChatReplaysSourcePublishEvent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	if _, _, err := store.EnsureChat("chat-source-current", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	events := []stream.EventData{
		{
			Type:      "chat.start",
			Timestamp: 1000,
			Payload: map[string]any{
				"chatId":   "chat-source-current",
				"chatName": "hello",
			},
		},
		{
			Type:      "request.query",
			Timestamp: 1001,
			Payload: map[string]any{
				"chatId":  "chat-source-current",
				"runId":   "run-source-current",
				"message": "where is the policy?",
			},
		},
		{
			Type:      "run.start",
			Timestamp: 1002,
			Payload: map[string]any{
				"chatId": "chat-source-current",
				"runId":  "run-source-current",
			},
		},
		{
			Type:      "source.publish",
			Timestamp: 1003,
			Payload: map[string]any{
				"publishId":   "src-current",
				"runId":       "run-source-current",
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
				"contentId": "run-source-current_c_1",
				"runId":     "run-source-current",
				"text":      "answer",
			},
		},
		{
			Type:      "run.complete",
			Timestamp: 1005,
			Payload: map[string]any{
				"runId": "run-source-current",
			},
		},
	}

	for idx := range events {
		events[idx].Seq = int64(idx + 1)
		if err := store.AppendEvent("chat-source-current", events[idx]); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	detail, err := store.LoadChat("chat-source-current")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}

	foundSourcePublish := false
	foundContentSnapshot := false
	for _, event := range detail.Events {
		switch event.Type {
		case "source.publish":
			foundSourcePublish = true
			if event.String("publishId") != "src-current" || event.String("runId") != "run-source-current" {
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

func TestStepWriterPersistsSourcePublishOnReactToolStep(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}

	chatID := "chat-source-writer"
	runID := "run-source-writer"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	writer := NewStepWriter(store, chatID, runID, "REACT")
	writer.OnEvent(stream.EventData{
		Seq:       19,
		Type:      "tool.snapshot",
		Timestamp: 1001,
		Payload: map[string]any{
			"toolId":    "call_1",
			"toolName":  "kbase_search",
			"arguments": `{"query":"policy"}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       20,
		Type:      "tool.result",
		Timestamp: 1002,
		Payload: map[string]any{
			"toolId":     "call_1",
			"toolName":   "kbase_search",
			"durationMs": int64(5),
			"result":     `{"count":1}`,
		},
	})
	writer.OnEvent(stream.EventData{
		Seq:       21,
		Type:      "source.publish",
		Timestamp: 1003,
		Payload: map[string]any{
			"publishId":   "src-writer",
			"runId":       runID,
			"toolId":      "call_1",
			"kind":        "kbase",
			"query":       "policy",
			"sourceCount": 1,
			"chunkCount":  1,
			"sources": []map[string]any{
				{
					"id":           "kbase:docs/policy.md",
					"name":         "policy.md",
					"chunkIndexes": []int{1},
					"minIndex":     1,
					"chunks": []map[string]any{
						{
							"chunkId":   "chunk_1",
							"index":     1,
							"content":   "policy content",
							"startLine": 12,
							"endLine":   14,
						},
					},
				},
			},
		},
	})
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected react and react-tool lines, got %#v", lines)
	}
	if lines[0]["_type"] != StepLineTypeReact || toIntValue(lines[0]["seq"]) != 1 {
		t.Fatalf("expected assistant tool call react line, got %#v", lines[0])
	}
	if lines[1]["_type"] != StepLineTypeReactTool || toIntValue(lines[1]["seq"]) != 1 || int64FromAny(lines[1]["liveSeq"]) != 21 {
		t.Fatalf("expected react-tool line with source liveSeq, got %#v", lines[1])
	}
	sourcesState, _ := lines[1]["sources"].(map[string]any)
	items, _ := sourcesState["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one persisted source item, got %#v", lines[1])
	}
	item, _ := items[0].(map[string]any)
	if item["publishId"] != "src-writer" || item["toolId"] != "call_1" || int64FromAny(item["liveSeq"]) != 21 {
		t.Fatalf("unexpected persisted source item %#v", item)
	}
	for _, line := range lines {
		if line["_type"] == "event" {
			t.Fatalf("did not expect source.publish event line, got %#v", line)
		}
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	toolResultIndex := -1
	sourceIndex := -1
	for _, event := range detail.Events {
		switch event.Type {
		case "tool.result":
			if event.String("toolId") == "call_1" {
				toolResultIndex = int(event.Seq)
			}
		case "source.publish":
			sourceIndex = int(event.Seq)
			if event.String("publishId") != "src-writer" || event.String("query") != "policy" || event.String("toolId") != "call_1" {
				t.Fatalf("unexpected replayed source event %#v", event)
			}
			if int64FromAny(event.Value("liveSeq")) != 21 {
				t.Fatalf("expected replay liveSeq=21, got %#v", event)
			}
		}
	}
	if toolResultIndex <= 0 || sourceIndex <= toolResultIndex {
		t.Fatalf("expected source.publish after matching tool.result, tool=%d source=%d events=%#v", toolResultIndex, sourceIndex, detail.Events)
	}
}

func TestLoadChatReplaysMultipleStepSourcesAfterMatchingToolResults(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-source-multi"
	runID := "run-source-multi"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReactTool,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 100,
		LiveSeq:   12,
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:       "tool",
				Name:       "kbase_search",
				ToolCallID: "call_1",
				ToolID:     "call_1",
				Content:    textContent(`{"count":1}`),
			},
			{
				Role:       "tool",
				Name:       "kbase_search",
				ToolCallID: "call_2",
				ToolID:     "call_2",
				Content:    textContent(`{"count":1}`),
			},
		},
		Sources: &SourceState{Items: []map[string]any{
			{
				"publishId":   "src_1",
				"runId":       runID,
				"toolId":      "call_1",
				"kind":        "kbase",
				"query":       "alpha",
				"sourceCount": 1,
				"chunkCount":  1,
				"timestamp":   int64(101),
				"liveSeq":     int64(11),
				"sources": []map[string]any{{
					"id":   "kbase:alpha.md",
					"name": "alpha.md",
				}},
			},
			{
				"publishId":   "src_2",
				"runId":       runID,
				"toolId":      "call_2",
				"kind":        "kbase",
				"query":       "beta",
				"sourceCount": 1,
				"chunkCount":  1,
				"timestamp":   int64(102),
				"liveSeq":     int64(12),
				"sources": []map[string]any{{
					"id":   "kbase:beta.md",
					"name": "beta.md",
				}},
			},
		}},
	}); err != nil {
		t.Fatalf("append source step: %v", err)
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	var order []string
	for _, event := range detail.Events {
		switch event.Type {
		case "tool.result":
			order = append(order, "tool:"+event.String("toolId"))
		case "source.publish":
			order = append(order, "source:"+event.String("toolId")+":"+event.String("publishId"))
		}
	}
	want := []string{"tool:call_1", "source:call_1:src_1", "tool:call_2", "source:call_2:src_2"}
	if len(order) < len(want) {
		t.Fatalf("expected replayed tool/source order %#v, got %#v events=%#v", want, order, detail.Events)
	}
	for index, expected := range want {
		if order[index] != expected {
			t.Fatalf("unexpected replay order got %#v want prefix %#v events=%#v", order, want, detail.Events)
		}
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

	writer := NewStepWriter(store, "chat-artifact-batch", "run-artifact-batch", "REACT")
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

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
