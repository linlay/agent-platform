package engine

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

func TestRuntimeToolExecutorDateTimeSupportsTimezoneAndOffset(t *testing.T) {
	payload, err := buildDateTimePayload(map[string]any{
		"timezone": "UTC+8",
		"offset":   "+1D-3H+20m",
	}, time.Date(2026, 4, 10, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("build date time payload: %v", err)
	}

	if payload["timezone"] != "+08:00" {
		t.Fatalf("expected normalized timezone +08:00, got %#v", payload["timezone"])
	}
	if payload["timezoneOffset"] != "UTC+8" {
		t.Fatalf("expected timezoneOffset UTC+8, got %#v", payload["timezoneOffset"])
	}
	if payload["offset"] != "+1D-3H+20m" {
		t.Fatalf("expected normalized offset preserved, got %#v", payload["offset"])
	}
	if payload["date"] != "2026-04-11" {
		t.Fatalf("expected shifted date 2026-04-11, got %#v", payload["date"])
	}
	if payload["time"] != "06:22:03" {
		t.Fatalf("expected shifted time 06:22:03, got %#v", payload["time"])
	}
	if payload["weekday"] != "星期六" {
		t.Fatalf("expected weekday 星期六, got %#v", payload["weekday"])
	}
	if payload["iso"] != "2026-04-11T06:22:03+08:00" {
		t.Fatalf("expected iso with timezone offset, got %#v", payload["iso"])
	}
	if payload["source"] != "system-clock" {
		t.Fatalf("expected source system-clock, got %#v", payload["source"])
	}
	if lunar, _ := payload["lunarDate"].(string); lunar == "" {
		t.Fatalf("expected non-empty lunarDate, got %#v", payload["lunarDate"])
	}
}

func TestRuntimeToolExecutorDateTimeRejectsInvalidArgs(t *testing.T) {
	if _, err := buildDateTimePayload(map[string]any{"timezone": "Mars/Base"}, time.Now()); err == nil {
		t.Fatalf("expected invalid timezone error")
	}
	if _, err := buildDateTimePayload(map[string]any{"offset": "tomorrow"}, time.Now()); err == nil {
		t.Fatalf("expected invalid offset error")
	}
}

func TestRuntimeToolExecutorMemoryToolsRequireContextAndUseCanonicalNames(t *testing.T) {
	store, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	executor, err := NewRuntimeToolExecutor(config.Config{Memory: config.MemoryConfig{SearchDefaultLimit: 10}}, NewNoopSandboxClient(), store)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}

	result, err := executor.Invoke(context.Background(), "_memory_write_", map[string]any{"content": "hello"}, nil)
	if err != nil {
		t.Fatalf("invoke _memory_write_ without context: %v", err)
	}
	if result.Error != "memory_context_required" {
		t.Fatalf("expected memory_context_required, got %#v", result)
	}

	execCtx := &ExecutionContext{
		Request: api.QueryRequest{RequestID: "req-1", ChatID: "chat-1"},
		Session: QuerySession{AgentKey: "agent-a", ChatID: "chat-1", RequestID: "req-1"},
	}

	writeResult, err := executor.Invoke(context.Background(), "_memory_write_", map[string]any{
		"content":    "urgent release note",
		"category":   "Alerts",
		"importance": 9,
		"tags":       []any{"release", "urgent"},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke _memory_write_: %v", err)
	}
	if writeResult.Structured["status"] != "stored" {
		t.Fatalf("expected stored status, got %#v", writeResult.Structured)
	}
	if writeResult.Structured["subjectKey"] != "chat:chat-1" {
		t.Fatalf("expected subjectKey chat:chat-1, got %#v", writeResult.Structured)
	}
	if writeResult.Structured["category"] != "alerts" {
		t.Fatalf("expected normalized category alerts, got %#v", writeResult.Structured)
	}

	recordID, _ := writeResult.Structured["id"].(string)
	readResult, err := executor.Invoke(context.Background(), "_memory_read_", map[string]any{"id": recordID}, execCtx)
	if err != nil {
		t.Fatalf("invoke _memory_read_: %v", err)
	}
	if readResult.Structured["found"] != true {
		t.Fatalf("expected found=true, got %#v", readResult.Structured)
	}
	memoryNode, _ := readResult.Structured["memory"].(map[string]any)
	if memoryNode["content"] != "urgent release note" {
		t.Fatalf("expected content in memory payload, got %#v", memoryNode)
	}

	listResult, err := executor.Invoke(context.Background(), "_memory_read_", map[string]any{"sort": "importance"}, execCtx)
	if err != nil {
		t.Fatalf("invoke _memory_read_ list mode: %v", err)
	}
	results, _ := listResult.Structured["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("expected one listed memory, got %#v", listResult.Structured)
	}

	searchResult, err := executor.Invoke(context.Background(), "_memory_search_", map[string]any{"query": "release"}, execCtx)
	if err != nil {
		t.Fatalf("invoke _memory_search_: %v", err)
	}
	searchResults, _ := searchResult.Structured["results"].([]map[string]any)
	if len(searchResults) != 1 {
		t.Fatalf("expected one search result, got %#v", searchResult.Structured)
	}
	if searchResults[0]["matchType"] == "" {
		t.Fatalf("expected non-empty matchType, got %#v", searchResults[0])
	}

	legacyResult, err := executor.Invoke(context.Background(), "memory_read", map[string]any{"id": recordID}, execCtx)
	if err != nil {
		t.Fatalf("invoke legacy memory_read alias: %v", err)
	}
	if legacyResult.Structured["found"] != true {
		t.Fatalf("expected legacy alias to keep working, got %#v", legacyResult.Structured)
	}
}

func TestPublishArtifactsKeepsChatWorkspaceFilesInPlace(t *testing.T) {
	chatsRoot := t.TempDir()
	chatID := "chat_1"
	runID := "run_1"
	chatDir := filepath.Join(chatsRoot, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatDir, "joke.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatalf("write joke.md: %v", err)
	}

	published := publishArtifacts(chatsRoot, chatID, runID, []any{
		map[string]any{"path": "/workspace/joke.md"},
	})

	if len(published) != 1 {
		t.Fatalf("expected one published artifact, got %#v", published)
	}
	if published[0]["url"] != "/api/resource?file="+url.QueryEscape(chatID+"/joke.md") {
		t.Fatalf("expected chat-root resource url, got %#v", published[0]["url"])
	}
	if _, err := os.Stat(filepath.Join(chatDir, "artifacts", runID)); !os.IsNotExist(err) {
		t.Fatalf("expected artifacts dir to remain absent, err=%v", err)
	}
}

func TestPublishArtifactsMaterializesExternalFilesAndDeduplicates(t *testing.T) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	sourceRoot, err := os.MkdirTemp(workspaceRoot, "artifact-publish-*")
	if err != nil {
		t.Fatalf("mkdtemp in workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sourceRoot) })

	sameA := filepath.Join(sourceRoot, "same-a", "report.md")
	sameB := filepath.Join(sourceRoot, "same-b", "report.md")
	diffC := filepath.Join(sourceRoot, "diff-c", "report.md")
	writeTestFile(t, sameA, "same-content\n")
	writeTestFile(t, sameB, "same-content\n")
	writeTestFile(t, diffC, "different-content\n")

	chatsRoot := t.TempDir()
	chatID := "chat_1"
	runID := "run_1"
	chatDir := filepath.Join(chatsRoot, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}

	published := publishArtifacts(chatsRoot, chatID, runID, []any{
		map[string]any{"path": sameA},
		map[string]any{"path": sameB},
		map[string]any{"path": diffC},
	})

	if len(published) != 3 {
		t.Fatalf("expected three published artifacts, got %#v", published)
	}

	firstURL := "/api/resource?file=" + url.QueryEscape(chatID+"/artifacts/"+runID+"/report.md")
	thirdURL := "/api/resource?file=" + url.QueryEscape(chatID+"/artifacts/"+runID+"/report-1.md")
	if published[0]["url"] != firstURL {
		t.Fatalf("expected first artifact in artifacts dir, got %#v", published[0]["url"])
	}
	if published[1]["url"] != firstURL {
		t.Fatalf("expected same-content duplicate to reuse target path, got %#v", published[1]["url"])
	}
	if published[2]["url"] != thirdURL {
		t.Fatalf("expected different-content duplicate to use suffixed target path, got %#v", published[2]["url"])
	}

	entries, err := os.ReadDir(filepath.Join(chatDir, "artifacts", runID))
	if err != nil {
		t.Fatalf("read artifacts dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two materialized files, got %d", len(entries))
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
