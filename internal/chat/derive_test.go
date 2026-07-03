package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveChatCopiesHistoryThroughTargetRun(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-source", "agent-a", "team-a", "first user"); err != nil {
		t.Fatalf("ensure source chat: %v", err)
	}
	appendDeriveTestRun(t, store, "chat-source", "run-1", "first user", "first assistant", 1000)
	appendDeriveTestRun(t, store, "chat-source", "run-2", "second user", "second assistant", 2000)
	before, err := store.LoadJSONLContent("chat-source")
	if err != nil {
		t.Fatalf("load source jsonl: %v", err)
	}

	result, err := store.DeriveChat(DeriveChatRequest{
		SourceChatID: "chat-source",
		SourceRunID:  "run-1",
		ChatID:       "chat-derived",
	})
	if err != nil {
		t.Fatalf("derive chat: %v", err)
	}
	if result.CopiedRuns != 1 || result.LastRunID == "" || result.LastRunID == "run-1" {
		t.Fatalf("unexpected derive result: %#v", result)
	}
	if result.Summary.ChatID != "chat-derived" || result.Summary.AgentKey != "agent-a" || result.Summary.TeamID != "team-a" {
		t.Fatalf("unexpected derived summary: %#v", result.Summary)
	}

	after, err := store.LoadJSONLContent("chat-source")
	if err != nil {
		t.Fatalf("reload source jsonl: %v", err)
	}
	if after != before {
		t.Fatalf("source jsonl changed")
	}
	derivedJSONL, err := store.LoadJSONLContent("chat-derived")
	if err != nil {
		t.Fatalf("load derived jsonl: %v", err)
	}
	if strings.Contains(derivedJSONL, "run-1") || strings.Contains(derivedJSONL, "run-2") || strings.Contains(derivedJSONL, "second user") {
		t.Fatalf("derived jsonl contains old or out-of-scope data: %s", derivedJSONL)
	}
	if !strings.Contains(derivedJSONL, result.LastRunID) || !strings.Contains(derivedJSONL, "first assistant") {
		t.Fatalf("derived jsonl missing mapped run or copied content: %s", derivedJSONL)
	}

	runs, err := store.ListRuns("chat-derived")
	if err != nil {
		t.Fatalf("list derived runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != result.LastRunID || runs[0].InitialMessage != "first user" || runs[0].AssistantText != "first assistant" {
		t.Fatalf("unexpected derived runs: %#v", runs)
	}
	messages, err := store.LoadRawMessages("chat-derived", 5)
	if err != nil {
		t.Fatalf("load derived raw messages: %v", err)
	}
	if len(messages) != 2 || messages[0]["content"] != "first user" || messages[1]["content"] != "first assistant" {
		t.Fatalf("unexpected derived raw messages: %#v", messages)
	}
}

func TestDeriveChatCopiesResourcesAndRewritesReferences(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	sourceChatID := "chat-source-res"
	targetChatID := "chat-derived-res"
	if _, _, err := store.EnsureChat(sourceChatID, "agent-a", "", "inspect upload"); err != nil {
		t.Fatalf("ensure source chat: %v", err)
	}
	sourceUpload := filepath.Join(store.ChatDir(sourceChatID), "notes.txt")
	if err := os.MkdirAll(filepath.Dir(sourceUpload), 0o755); err != nil {
		t.Fatalf("mkdir upload dir: %v", err)
	}
	if err := os.WriteFile(sourceUpload, []byte("notes"), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	mustWriteDeriveResource(t, store, sourceChatID, filepath.Join(ToolRootDirName, ToolResultsDirName, "call_1.json"), `{"stdout":"ok"}`)
	mustWriteDeriveResource(t, store, sourceChatID, filepath.Join(ToolRootDirName, ToolPlansDirName, "run-res_planning_1.md"), "# plan")
	mustWriteDeriveResource(t, store, sourceChatID, filepath.Join(ToolRootDirName, ToolPlanTasksDirName, "run-res_plan.json"), `{"chatId":"chat-source-res","runId":"run-res","tasks":[]}`)
	mustWriteDeriveResource(t, store, sourceChatID, filepath.Join(ToolRootDirName, ToolStateDirName, FileVersionsFileName), `{"state":"skip"}`)

	if err := store.AppendQueryLine(sourceChatID, QueryLine{
		Type:      "query",
		ChatID:    sourceChatID,
		RunID:     "run-res",
		UpdatedAt: 1000,
		Query: map[string]any{
			"chatId":    sourceChatID,
			"runId":     "run-res",
			"requestId": "req-res",
			"role":      "user",
			"message":   "inspect upload",
			"references": []map[string]any{{
				"id":       "ref-1",
				"type":     "file",
				"name":     "notes.txt",
				"path":     sourceUpload,
				"url":      "/api/resource?file=chat-source-res%2Fnotes.txt",
				"mimeType": "text/plain",
			}},
		},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(sourceChatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    sourceChatID,
		RunID:     "run-res",
		UpdatedAt: 1001,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "checked"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{ChatID: sourceChatID, RunID: "run-res", AgentKey: "agent-a", InitialMessage: "inspect upload", AssistantText: "checked", FinishReason: "complete", UpdatedAtMillis: 1002}); err != nil {
		t.Fatalf("complete source run: %v", err)
	}

	result, err := store.DeriveChat(DeriveChatRequest{SourceChatID: sourceChatID, ChatID: targetChatID})
	if err != nil {
		t.Fatalf("derive chat: %v", err)
	}
	if result.CopiedRuns != 1 || result.LastRunID == "run-res" {
		t.Fatalf("unexpected result: %#v", result)
	}

	for _, rel := range []string{
		"notes.txt",
		filepath.Join(ToolRootDirName, ToolResultsDirName, "call_1.json"),
		filepath.Join(ToolRootDirName, ToolPlansDirName, "run-res_planning_1.md"),
		filepath.Join(ToolRootDirName, ToolPlanTasksDirName, result.LastRunID+"_plan.json"),
	} {
		if _, err := os.Stat(filepath.Join(store.ChatDir(targetChatID), rel)); err != nil {
			t.Fatalf("expected copied resource %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.ChatDir(targetChatID), ToolRootDirName, ToolPlanTasksDirName, "run-res_plan.json")); !os.IsNotExist(err) {
		t.Fatalf("expected old plan task snapshot name removed, stat err=%v", err)
	}
	planTaskData, err := os.ReadFile(filepath.Join(store.ChatDir(targetChatID), ToolRootDirName, ToolPlanTasksDirName, result.LastRunID+"_plan.json"))
	if err != nil {
		t.Fatalf("read plan task snapshot: %v", err)
	}
	if text := string(planTaskData); !strings.Contains(text, `"chatId": "chat-derived-res"`) || !strings.Contains(text, `"runId": "`+result.LastRunID+`"`) {
		t.Fatalf("expected rewritten plan task snapshot, got %s", text)
	}
	if _, err := os.Stat(filepath.Join(store.ChatDir(targetChatID), ToolRootDirName, ToolStateDirName, FileVersionsFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected tool state not copied, stat err=%v", err)
	}

	lines, err := readJSONLines(store.chatJSONLPath(targetChatID))
	if err != nil {
		t.Fatalf("read derived jsonl: %v", err)
	}
	query := anyMap(lines[0]["query"])
	if query["chatId"] != targetChatID || query["runId"] != result.LastRunID || query["requestId"] != result.LastRunID {
		t.Fatalf("expected ids rewritten in query payload, got %#v", query)
	}
	refs := anySlice(query["references"])
	if len(refs) != 1 {
		t.Fatalf("expected one reference, got %#v", query["references"])
	}
	ref := anyMap(refs[0])
	if got := stringValue(ref["path"]); got != filepath.Join(store.ChatDir(targetChatID), "notes.txt") {
		t.Fatalf("reference path = %q", got)
	}
	if got := stringValue(ref["url"]); !strings.Contains(got, "file=chat-derived-res%2Fnotes.txt") {
		t.Fatalf("reference url = %q", got)
	}
}

func appendDeriveTestRun(t *testing.T, store *FileStore, chatID string, runID string, userText string, assistantText string, updatedAt int64) {
	t.Helper()
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: updatedAt,
		Query: map[string]any{
			"chatId":    chatID,
			"runId":     runID,
			"requestId": runID,
			"role":      "user",
			"message":   userText,
		},
		Messages: []map[string]any{{"role": "user", "content": userText}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: updatedAt + 1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: assistantText}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := store.OnRunCompleted(RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "agent-a",
		InitialMessage:  userText,
		AssistantText:   assistantText,
		FinishReason:    "complete",
		UpdatedAtMillis: updatedAt + 2,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
}

func mustWriteDeriveResource(t *testing.T, store *FileStore, chatID string, relPath string, content string) {
	t.Helper()
	path := filepath.Join(store.ChatDir(chatID), relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir resource dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write resource %s: %v", relPath, err)
	}
}
