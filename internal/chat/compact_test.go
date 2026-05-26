package chat

import (
	"strings"
	"testing"
	"time"
)

func TestCompactCheckpointProjectsSummaryAndTail(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	seedCompactRun(t, store, "chat-compact", "run-1", "读取日志", "bash", strings.Repeat("line\n", 1200), now)
	seedCompactRun(t, store, "chat-compact", "run-2", "继续分析", "", "第二轮结果", now+1)
	seedCompactRun(t, store, "chat-compact", "run-3", "收尾", "", "第三轮结果", now+2)

	snapshot, err := store.BuildCompactSnapshot("chat-compact", 2)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BoundaryRunID != "run-2" {
		t.Fatalf("expected boundary run-2, got %q", snapshot.BoundaryRunID)
	}
	if snapshot.PreCompactTokens <= 0 || snapshot.PostCompactTokens <= 0 || snapshot.CompressionRatio <= 0 {
		t.Fatalf("expected compact metrics, got %#v", snapshot)
	}
	if len(snapshot.ToolDigests) != 1 {
		t.Fatalf("expected one tool digest, got %#v", snapshot.ToolDigests)
	}
	if len(snapshot.DigestedRunIDs) != 1 || snapshot.DigestedRunIDs[0] != "run-1" {
		t.Fatalf("expected digested run id, got %#v", snapshot.DigestedRunIDs)
	}
	if !strings.Contains(snapshot.Prompt, "[工具结果已压缩]") {
		t.Fatalf("expected compact prompt to include digested tool result")
	}

	if err := store.AppendCompactLine("chat-compact", CompactLine{
		ChatID:        "chat-compact",
		RunID:         snapshot.BoundaryRunID,
		CompactID:     "compact-1",
		UpdatedAt:     now + 3,
		BoundaryRunID: snapshot.BoundaryRunID,
		BoundarySeq:   snapshot.BoundarySeq,
		Summary:       "用户要求分析日志，已经读取第一轮日志并得到关键结论。",
		SummarySource: "model",
		KeptRunCount:  2,
		ToolDigests:   snapshot.ToolDigests,
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.LoadRawMessages("chat-compact", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Fatal("expected projected messages")
	}
	if messages[0]["role"] != "user" || !strings.Contains(messages[0]["content"].(string), "上下文压缩摘要") {
		t.Fatalf("expected compact summary user context message first, got %#v", messages[0])
	}
	rendered := compactTestRender(messages)
	if strings.Contains(rendered, "读取日志") || strings.Contains(rendered, strings.Repeat("line\n", 20)) {
		t.Fatalf("expected checkpoint-before raw history to be removed, got %s", rendered)
	}
	if !strings.Contains(rendered, "继续分析") || !strings.Contains(rendered, "第三轮结果") {
		t.Fatalf("expected kept tail runs, got %s", rendered)
	}

	seedCompactRun(t, store, "chat-compact", "run-4", "再分析", "", "第四轮结果", now+4)
	nextSnapshot, err := store.BuildCompactSnapshot("chat-compact", 2)
	if err != nil {
		t.Fatal(err)
	}
	if nextSnapshot.Generation != 2 {
		t.Fatalf("expected second compact generation, got %d", nextSnapshot.Generation)
	}
	if strings.Contains(nextSnapshot.Prompt, "用户要求分析日志") {
		t.Fatalf("expected second compact to rebuild from raw history, not previous summary")
	}
}

func TestCompactProjectionDigestsHugeRecentToolResults(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	seedCompactRun(t, store, "chat-recent-tool", "run-1", "第一轮", "", "第一轮结果", now)
	seedCompactRun(t, store, "chat-recent-tool", "run-2", "第二轮", "bash", strings.Repeat("recent output\n", 900), now+1)
	if err := store.AppendCompactLine("chat-recent-tool", CompactLine{
		ChatID:        "chat-recent-tool",
		RunID:         "run-1",
		CompactID:     "compact-recent",
		UpdatedAt:     now + 2,
		BoundaryRunID: "run-2",
		BoundarySeq:   2,
		Summary:       "第一轮摘要",
		SummarySource: "model",
		KeptRunCount:  2,
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.LoadRawMessages("chat-recent-tool", 20)
	if err != nil {
		t.Fatal(err)
	}
	rendered := compactTestRender(messages)
	if !strings.Contains(rendered, "[工具结果已压缩]") {
		t.Fatalf("expected huge recent tool result to be digested, got %s", rendered)
	}
	if strings.Contains(rendered, strings.Repeat("recent output\n", 100)) {
		t.Fatalf("expected recent tool output to be truncated, got %s", rendered)
	}
}

func seedCompactRun(t *testing.T, store *FileStore, chatID string, runID string, userText string, toolName string, result string, ts int64) {
	t.Helper()
	if err := store.AppendQueryLine(chatID, QueryLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: ts,
		Query:     map[string]any{"role": "user", "message": userText},
		Type:      "query",
	}); err != nil {
		t.Fatal(err)
	}
	messages := []StoredMessage{{
		Role:    "assistant",
		Content: []ContentPart{{Type: "text", Text: result}},
	}}
	if toolName != "" {
		messages = []StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []StoredToolCall{{
					ID:   "tool-" + runID,
					Type: "function",
					Function: StoredFunction{
						Name:      toolName,
						Arguments: `{"command":"printf logs"}`,
					},
				}},
			},
			{
				Role:       "tool",
				Name:       toolName,
				ToolCallID: "tool-" + runID,
				Content:    []ContentPart{{Type: "text", Text: result}},
			},
		}
	}
	if err := store.AppendStepLine(chatID, StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: ts,
		Type:      "react",
		Messages:  messages,
	}); err != nil {
		t.Fatal(err)
	}
}

func compactTestRender(messages []map[string]any) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(strings.TrimSpace(stringValue(message["content"])))
		b.WriteByte('\n')
	}
	return b.String()
}
