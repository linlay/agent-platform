package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestCompactCommitMarksCoveredLinesAndProjectsSummaryTail(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact"
	ensureCompactTestChat(t, store, chatID)

	if err := store.AppendEventLine(chatID, EventLine{
		Type:      "event",
		ChatID:    chatID,
		RunID:     "r1",
		UpdatedAt: testEpochMillis(99),
		Event:     map[string]any{"type": "run.note", "message": "event r1", "timestamp": testEpochMillis(99)},
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r1", "user r1", "assistant r1")
	appendCompactTestRun(t, store, chatID, "r2", "user r2", "assistant r2")
	appendCompactTestRun(t, store, chatID, "r3", "user r3", "assistant r3")
	appendCompactTestRun(t, store, chatID, "r4", "user r4", "assistant r4")

	beforeBytes, err := os.ReadFile(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read before jsonl: %v", err)
	}
	beforeLines := compactTestJSONLLines(beforeBytes)

	snapshot, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("BuildCompactSnapshot: %v", err)
	}
	if snapshot.CoveredLineCount != 5 {
		t.Fatalf("covered line count = %d, want 5", snapshot.CoveredLineCount)
	}
	if snapshot.ProjectedMessageCount != 4 {
		t.Fatalf("projected message count = %d, want 4", snapshot.ProjectedMessageCount)
	}
	if err := store.CommitCompactCheckpoint(chatID, snapshot, CompactCheckpointLine{
		Type:                       CompactCheckpointLineType,
		ChatID:                     chatID,
		CompactID:                  "compact_1",
		UpdatedAt:                  testEpochMillis(123),
		Trigger:                    "manual",
		Summary:                    "summary one",
		SummarySource:              "model",
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: EstimateCompactPostTokens("summary one", snapshot.TailMessages),
		CompactionUsage:            map[string]any{},
	}); err != nil {
		t.Fatalf("CommitCompactCheckpoint: %v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read compacted jsonl: %v", err)
	}
	if len(lines) != len(beforeLines)+1 {
		t.Fatalf("line count = %d, want %d", len(lines), len(beforeLines)+1)
	}
	for i := 0; i < 5; i++ {
		if got := stringFromAny(lines[i]["_compact"]); got != "compact_1" {
			t.Fatalf("line %d _compact = %q, want compact_1", i, got)
		}
	}
	if got := stringFromAny(lines[5]["_type"]); got != CompactCheckpointLineType {
		t.Fatalf("inserted line type = %q, want %q", got, CompactCheckpointLineType)
	}
	if got := stringFromAny(lines[5]["summary"]); got != "summary one" {
		t.Fatalf("checkpoint summary = %q", got)
	}
	if _, ok := lines[6]["_compact"]; ok {
		t.Fatalf("tail line unexpectedly compacted: %#v", lines[6])
	}

	afterBytes, err := os.ReadFile(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read after jsonl: %v", err)
	}
	afterLines := compactTestJSONLLines(afterBytes)
	if afterLines[6] != beforeLines[5] {
		t.Fatalf("tail raw line changed:\nwant %s\ngot  %s", beforeLines[5], afterLines[6])
	}
	if _, err := os.Stat(store.ChatDir(chatID) + "/.compact-backups/compact_1.jsonl"); err != nil {
		t.Fatalf("expected compact backup: %v", err)
	}

	raw, err := store.LoadRawMessages(chatID, 1)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if len(raw) != 5 {
		t.Fatalf("raw message count = %d, want summary + r3/r4 messages", len(raw))
	}
	if content := stringFromAny(raw[0]["content"]); !strings.Contains(content, "summary one") || !strings.Contains(content, "替代所有已标记 _compact") {
		t.Fatalf("first raw message is not compact summary: %q", content)
	}
	for _, msg := range raw {
		content := stringFromAny(msg["content"])
		if strings.Contains(content, "r1") || strings.Contains(content, "r2") {
			t.Fatalf("compacted content leaked into raw messages: %#v", msg)
		}
	}

	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	if !compactReplayContains(detail.Events, "request.query", "user r1") {
		t.Fatalf("LoadChat replay should still show compacted historical query")
	}
	if !compactReplayContains(detail.Events, "context.compact.complete", "compact_1") {
		t.Fatalf("LoadChat replay should expose compact completion marker")
	}
}

func TestRawMessagesSkipCompactedLinesAndInactiveCheckpoints(t *testing.T) {
	lines := []map[string]any{
		{
			"_type":    "query",
			"_compact": "compact_1",
			"runId":    "r1",
			"query":    map[string]any{"role": "user", "message": "old user"},
		},
		{
			"_type":     CompactCheckpointLineType,
			"compactId": "compact_empty",
			"summary":   "",
		},
		{
			"_type":     CompactCheckpointLineType,
			"_compact":  "compact_2",
			"compactId": "compact_old",
			"summary":   "old summary",
		},
		{
			"_type":     CompactCheckpointLineType,
			"compactId": "compact_active",
			"summary":   "active summary",
		},
		{
			"_type": "query",
			"runId": "r2",
			"query": map[string]any{"role": "user", "message": "tail user"},
			"messages": []any{
				map[string]any{"role": "user", "content": "tail user"},
			},
		},
	}
	raw := rawMessagesFromJSONLLines(lines)
	if len(raw) != 2 {
		t.Fatalf("raw len = %d, want 2: %#v", len(raw), raw)
	}
	if content := stringFromAny(raw[0]["content"]); !strings.Contains(content, "active summary") {
		t.Fatalf("first message should be active summary, got %q", content)
	}
	if content := stringFromAny(raw[1]["content"]); content != "tail user" {
		t.Fatalf("tail query content = %q", content)
	}
}

func TestSecondCompactCoversPreviousCheckpoint(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact-second"
	ensureCompactTestChat(t, store, chatID)
	appendCompactTestRun(t, store, chatID, "r1", "user r1", "assistant r1")
	appendCompactTestRun(t, store, chatID, "r2", "user r2", "assistant r2")
	appendCompactTestRun(t, store, chatID, "r3", "user r3", "assistant r3")
	appendCompactTestRun(t, store, chatID, "r4", "user r4", "assistant r4")

	first, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("first BuildCompactSnapshot: %v", err)
	}
	if err := store.CommitCompactCheckpoint(chatID, first, CompactCheckpointLine{
		Type:            CompactCheckpointLineType,
		ChatID:          chatID,
		CompactID:       "compact_1",
		UpdatedAt:       testEpochMillis(123),
		Summary:         "summary one",
		SummarySource:   "model",
		CompactionUsage: map[string]any{},
	}); err != nil {
		t.Fatalf("first CommitCompactCheckpoint: %v", err)
	}

	appendCompactTestRun(t, store, chatID, "r5", "user r5", "assistant r5")
	second, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("second BuildCompactSnapshot: %v", err)
	}
	if err := store.CommitCompactCheckpoint(chatID, second, CompactCheckpointLine{
		Type:            CompactCheckpointLineType,
		ChatID:          chatID,
		CompactID:       "compact_2",
		UpdatedAt:       testEpochMillis(456),
		Summary:         "summary two",
		SummarySource:   "model",
		CompactionUsage: map[string]any{},
	}); err != nil {
		t.Fatalf("second CommitCompactCheckpoint: %v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	activeCheckpoints := 0
	oldCheckpointCovered := false
	for _, line := range lines {
		if stringFromAny(line["_type"]) != CompactCheckpointLineType {
			continue
		}
		if stringFromAny(line["compactId"]) == "compact_1" && stringFromAny(line["_compact"]) == "compact_2" {
			oldCheckpointCovered = true
		}
		if _, covered := line["_compact"]; !covered {
			activeCheckpoints++
		}
	}
	if !oldCheckpointCovered {
		t.Fatalf("old active checkpoint was not covered by second compact")
	}
	if activeCheckpoints != 1 {
		t.Fatalf("active checkpoint count = %d, want 1", activeCheckpoints)
	}

	raw, err := store.LoadRawMessages(chatID, 20)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if content := stringFromAny(raw[0]["content"]); !strings.Contains(content, "summary two") || strings.Contains(content, "summary one") {
		t.Fatalf("first raw message should be only second summary: %q", content)
	}
}

func TestCompactCommitDetectsHistoryChanged(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-compact-race"
	ensureCompactTestChat(t, store, chatID)
	appendCompactTestRun(t, store, chatID, "r1", "user r1", "assistant r1")
	appendCompactTestRun(t, store, chatID, "r2", "user r2", "assistant r2")
	appendCompactTestRun(t, store, chatID, "r3", "user r3", "assistant r3")

	snapshot, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("BuildCompactSnapshot: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r4", "user r4", "assistant r4")
	err = store.CommitCompactCheckpoint(chatID, snapshot, CompactCheckpointLine{
		Type:            CompactCheckpointLineType,
		ChatID:          chatID,
		CompactID:       "compact_1",
		UpdatedAt:       testEpochMillis(123),
		Summary:         "summary one",
		CompactionUsage: map[string]any{},
	})
	if !errors.Is(err, ErrCompactHistoryChanged) {
		t.Fatalf("CommitCompactCheckpoint err = %v, want ErrCompactHistoryChanged", err)
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	for i, line := range lines {
		if _, ok := line["_compact"]; ok {
			t.Fatalf("line %d unexpectedly compacted after history_changed: %#v", i, line)
		}
		if stringFromAny(line["_type"]) == CompactCheckpointLineType {
			t.Fatalf("checkpoint unexpectedly written after history_changed: %#v", line)
		}
	}
}

func TestToolCompactClearsOlderCompactableToolResults(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-tool-compact"
	ensureCompactTestChat(t, store, chatID)
	for i := 1; i <= 7; i++ {
		appendCompactTestToolResult(t, store, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "file_read", fmt.Sprintf("file result %d %s", i, strings.Repeat("x", 240)))
	}
	appendCompactTestToolResult(t, store, chatID, "r8", "tool-noncompact", "memory_search", "memory result should stay")

	snapshot, err := store.BuildToolCompactSnapshot(chatID, DefaultToolCompactKeepRecent)
	if err != nil {
		t.Fatalf("BuildToolCompactSnapshot: %v", err)
	}
	if snapshot.ToolsCleared != 2 || snapshot.ToolsKept != 5 || snapshot.TokensFreed <= 0 {
		t.Fatalf("unexpected tool compact snapshot: %#v", snapshot)
	}
	if err := store.CommitToolCompact(chatID, snapshot, ToolCompactLine{
		Type:                       ToolCompactLineType,
		ChatID:                     chatID,
		CompactID:                  "compact_tools_1",
		UpdatedAt:                  testEpochMillis(123),
		Trigger:                    "manual",
		Level:                      "l1_tools",
		ToolsCleared:               snapshot.ToolsCleared,
		ToolsKept:                  snapshot.ToolsKept,
		TokensFreed:                snapshot.TokensFreed,
		PreCompactEstimatedTokens:  snapshot.PreCompactEstimatedTokens,
		PostCompactEstimatedTokens: snapshot.PostCompactEstimatedTokens,
		CompressionRatio:           snapshot.CompressionRatio,
	}); err != nil {
		t.Fatalf("CommitToolCompact: %v", err)
	}
	if _, err := os.Stat(store.ChatDir(chatID) + "/.compact-backups/compact_tools_1.jsonl"); err != nil {
		t.Fatalf("expected tool compact backup: %v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if got := stringFromAny(lines[len(lines)-1]["_type"]); got != ToolCompactLineType {
		t.Fatalf("last line type = %q, want %q", got, ToolCompactLineType)
	}

	raw, err := store.LoadRawMessages(chatID, 20)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	toolContent := map[string]string{}
	for _, msg := range raw {
		if stringFromAny(msg["role"]) != "tool" {
			continue
		}
		toolContent[stringFromAny(msg["tool_call_id"])] = stringFromAny(msg["content"])
	}
	for _, toolID := range []string{"tool-1", "tool-2"} {
		if toolContent[toolID] != ToolCompactClearedMessage {
			t.Fatalf("%s content = %q, want cleared marker", toolID, toolContent[toolID])
		}
	}
	for i := 3; i <= 7; i++ {
		toolID := fmt.Sprintf("tool-%d", i)
		if strings.Contains(toolContent[toolID], ToolCompactClearedMessage) || !strings.Contains(toolContent[toolID], fmt.Sprintf("file result %d", i)) {
			t.Fatalf("%s should be kept, got %q", toolID, toolContent[toolID])
		}
	}
	if toolContent["tool-noncompact"] != "memory result should stay" {
		t.Fatalf("non compactable tool changed: %q", toolContent["tool-noncompact"])
	}

	second, err := store.BuildToolCompactSnapshot(chatID, DefaultToolCompactKeepRecent)
	if err != nil {
		t.Fatalf("second BuildToolCompactSnapshot: %v", err)
	}
	if second.ToolsCleared != 0 {
		t.Fatalf("second tool compact should be idempotent, got %#v", second)
	}
}

func TestToolCompactCommitDetectsHistoryChanged(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-tool-compact-race"
	ensureCompactTestChat(t, store, chatID)
	for i := 1; i <= 7; i++ {
		appendCompactTestToolResult(t, store, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "bash", fmt.Sprintf("bash result %d %s", i, strings.Repeat("x", 240)))
	}

	snapshot, err := store.BuildToolCompactSnapshot(chatID, DefaultToolCompactKeepRecent)
	if err != nil {
		t.Fatalf("BuildToolCompactSnapshot: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r8", "user r8", "assistant r8")
	err = store.CommitToolCompact(chatID, snapshot, ToolCompactLine{
		Type:      ToolCompactLineType,
		ChatID:    chatID,
		CompactID: "compact_tools_race",
		UpdatedAt: testEpochMillis(123),
		Level:     "l1_tools",
	})
	if !errors.Is(err, ErrCompactHistoryChanged) {
		t.Fatalf("CommitToolCompact err = %v, want ErrCompactHistoryChanged", err)
	}

	raw, err := store.LoadRawMessages(chatID, 20)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	for _, msg := range raw {
		if stringFromAny(msg["role"]) == "tool" && stringFromAny(msg["content"]) == ToolCompactClearedMessage {
			t.Fatalf("tool result unexpectedly compacted after history_changed: %#v", msg)
		}
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	for _, line := range lines {
		if stringFromAny(line["_type"]) == ToolCompactLineType {
			t.Fatalf("tool compact line unexpectedly written after history_changed: %#v", line)
		}
	}
}

func TestSummaryCompactCanCoverToolCompactMetadata(t *testing.T) {
	store := newCompactTestStore(t)
	chatID := "chat-tool-compact-then-summary"
	ensureCompactTestChat(t, store, chatID)
	for i := 1; i <= 7; i++ {
		appendCompactTestToolResult(t, store, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "file_grep", fmt.Sprintf("grep result %d %s", i, strings.Repeat("x", 240)))
	}
	toolSnapshot, err := store.BuildToolCompactSnapshot(chatID, DefaultToolCompactKeepRecent)
	if err != nil {
		t.Fatalf("BuildToolCompactSnapshot: %v", err)
	}
	if err := store.CommitToolCompact(chatID, toolSnapshot, ToolCompactLine{
		Type:      ToolCompactLineType,
		ChatID:    chatID,
		CompactID: "compact_tools_1",
		UpdatedAt: testEpochMillis(123),
		Level:     "l1_tools",
	}); err != nil {
		t.Fatalf("CommitToolCompact: %v", err)
	}
	appendCompactTestRun(t, store, chatID, "r8", "user r8", "assistant r8")
	appendCompactTestRun(t, store, chatID, "r9", "user r9", "assistant r9")

	summarySnapshot, err := store.BuildCompactSnapshot(chatID, 2)
	if err != nil {
		t.Fatalf("BuildCompactSnapshot: %v", err)
	}
	if err := store.CommitCompactCheckpoint(chatID, summarySnapshot, CompactCheckpointLine{
		Type:            CompactCheckpointLineType,
		ChatID:          chatID,
		CompactID:       "compact_summary_1",
		UpdatedAt:       testEpochMillis(456),
		Summary:         "summary after tool compact",
		SummarySource:   "model",
		CompactionUsage: map[string]any{},
	}); err != nil {
		t.Fatalf("CommitCompactCheckpoint: %v", err)
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	coveredToolMetadata := false
	for _, line := range lines {
		if stringFromAny(line["_type"]) == ToolCompactLineType && stringFromAny(line["_compact"]) == "compact_summary_1" {
			coveredToolMetadata = true
		}
	}
	if !coveredToolMetadata {
		t.Fatalf("summary compact did not cover tool compact metadata")
	}
}

func newCompactTestStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ensureCompactTestChat(t *testing.T, store *FileStore, chatID string) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello compact"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
}

func appendCompactTestRun(t *testing.T, store *FileStore, chatID string, runID string, userText string, assistantText string) {
	t.Helper()
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: testEpochMillis(100),
		Query:     map[string]any{"role": "user", "message": userText},
		Messages:  []map[string]any{{"role": "user", "content": userText, "ts": testEpochMillis(100)}},
	}); err != nil {
		t.Fatalf("AppendQueryLine(%s): %v", runID, err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: testEpochMillis(101),
		Messages: []StoredMessage{
			{
				Role:    "assistant",
				Content: []ContentPart{{Type: "text", Text: assistantText}},
				Ts:      int64Ptr(testEpochMillis(101)),
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
	if err := completeRunForTest(store, RunCompletion{ChatID: chatID, RunID: runID, InitialMessage: userText, AssistantText: assistantText, FinishReason: "complete", StartedAtMillis: testEpochMillis(100), UpdatedAtMillis: testEpochMillis(101)}); err != nil {
		t.Fatalf("complete %s: %v", runID, err)
	}
}

func appendCompactTestToolResult(t *testing.T, store *FileStore, chatID string, runID string, toolID string, toolName string, resultText string) {
	t.Helper()
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: testEpochMillis(101),
		Messages: []StoredMessage{
			{
				Role: "assistant",
				Ts:   int64Ptr(testEpochMillis(101)),
				ToolCalls: []StoredToolCall{{
					ID:   toolID,
					Type: "function",
					Function: StoredFunction{
						Name:      toolName,
						Arguments: "{}",
					},
				}},
			},
			{
				Role:       "tool",
				Name:       toolName,
				ToolCallID: toolID,
				Content:    []ContentPart{{Type: "text", Text: resultText}},
				Ts:         int64Ptr(testEpochMillis(101)),
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
	if err := completeRunForTest(store, RunCompletion{ChatID: chatID, RunID: runID, FinishReason: "complete", StartedAtMillis: testEpochMillis(100), UpdatedAtMillis: testEpochMillis(101)}); err != nil {
		t.Fatalf("complete %s: %v", runID, err)
	}
}

func compactTestJSONLLines(data []byte) []string {
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func compactReplayContains(events []stream.EventData, eventType string, needle string) bool {
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}
