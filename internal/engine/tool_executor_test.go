package engine

import (
	"context"
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
