package automation

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestDispatcherBuildsStructuredQueryRequest(t *testing.T) {
	hidden := true
	def := Definition{
		ID:          "daily",
		Name:        "Daily Summary",
		Description: "Summarize the day",
		Enabled:     true,
		Cron:        "0 9 * * *",
		AgentKey:    "demo-agent",
		TeamID:      "team-a",
		SourceFile:  "/tmp/daily.yml",
		Query: Query{
			RequestID: "req-1",
			ChatID:    "123e4567-e89b-12d3-a456-426614174000",
			Message:   "hello",
			Params:    map[string]any{"existing": "value"},
			References: []api.Reference{
				{ID: "ref-1", Type: "url", URL: "https://example.com"},
			},
			Scene:  &api.Scene{URL: "https://example.com/app", Title: "demo"},
			Hidden: &hidden,
		},
	}

	var got api.QueryRequest
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		got = req
		return nil
	}, nil, nil)
	if err := dispatcher.Dispatch(context.Background(), def); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got.RequestID != "req-1" || got.ChatID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("unexpected ids %#v", got)
	}
	if got.AgentKey != "demo-agent" || got.TeamID != "team-a" {
		t.Fatalf("unexpected target %#v", got)
	}
	if got.Role != "user" || got.Message != "hello" {
		t.Fatalf("unexpected role/message %#v", got)
	}
	if got.Hidden == nil || !*got.Hidden {
		t.Fatalf("expected hidden=true, got %#v", got.Hidden)
	}
	if len(got.References) != 1 || got.Scene == nil || got.Scene.Title != "demo" {
		t.Fatalf("unexpected refs/scene %#v", got)
	}
	if got.Params["existing"] != "value" {
		t.Fatalf("expected existing params, got %#v", got.Params)
	}
	meta, ok := got.Params["__automation"].(map[string]any)
	if !ok {
		t.Fatalf("expected __automation metadata, got %#v", got.Params)
	}
	if meta["automationId"] != "daily" || meta["automationName"] != "Daily Summary" || meta["sourceFile"] != "/tmp/daily.yml" {
		t.Fatalf("unexpected __automation metadata %#v", meta)
	}
}

func TestDispatcherLogsDispatchLifecycle(t *testing.T) {
	def := Definition{
		ID:          "daily",
		Name:        "Daily Summary",
		Description: "Summarize the day",
		Enabled:     true,
		Cron:        "0 9 * * *",
		AgentKey:    "demo-agent",
		TeamID:      "team-a",
		SourceFile:  "/tmp/daily.yml",
		Query:       Query{Message: "hello"},
	}

	successLogs := captureDispatcherLogs(t, func() {
		dispatcher := NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return nil }, nil, nil)
		if err := dispatcher.Dispatch(context.Background(), def); err != nil {
			t.Fatalf("dispatch success: %v", err)
		}
	})
	if !strings.Contains(successLogs, "[automation] dispatch start id=daily") {
		t.Fatalf("expected dispatch start log, got %s", successLogs)
	}
	if !strings.Contains(successLogs, "[automation] dispatch success id=daily") {
		t.Fatalf("expected dispatch success log, got %s", successLogs)
	}
	if !strings.Contains(successLogs, "source=/tmp/daily.yml") {
		t.Fatalf("expected automation source in logs, got %s", successLogs)
	}

	failureLogs := captureDispatcherLogs(t, func() {
		dispatcher := NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return errors.New("boom") }, nil, nil)
		err := dispatcher.Dispatch(context.Background(), def)
		if err == nil {
			t.Fatal("expected dispatch failure")
		}
	})
	if !strings.Contains(failureLogs, "[automation] dispatch failed id=daily") {
		t.Fatalf("expected dispatch failure log, got %s", failureLogs)
	}
	if !strings.Contains(failureLogs, "err=boom") {
		t.Fatalf("expected failure reason in logs, got %s", failureLogs)
	}
}

func TestDispatcherRecordsExecutionLifecycle(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	defer store.Close()

	def := Definition{
		ID:          "daily",
		Name:        "Daily Summary",
		Description: "Summarize the day",
		Enabled:     true,
		Cron:        "0 9 * * *",
		AgentKey:    "demo-agent",
		TeamID:      "team-a",
		SourceFile:  "/tmp/daily.yml",
		Query:       Query{Message: "hello"},
	}

	dispatcher := NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return nil }, nil, store)
	if err := dispatcher.Dispatch(context.Background(), def); err != nil {
		t.Fatalf("dispatch success: %v", err)
	}
	items, total, err := store.ListByAutomation("daily", 10, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Status != "success" || items[0].DurationMs == nil {
		t.Fatalf("unexpected success execution total=%d items=%#v", total, items)
	}

	expectedErr := errors.New("boom")
	dispatcher = NewDispatcher(func(_ context.Context, _ api.QueryRequest) error { return expectedErr }, nil, store)
	if err := dispatcher.Dispatch(context.Background(), def); !errors.Is(err, expectedErr) {
		t.Fatalf("expected dispatch error, got %v", err)
	}
	last, err := store.LastExecution("daily")
	if err != nil {
		t.Fatalf("last execution: %v", err)
	}
	if last == nil || last.Status != "failed" || last.Error != "boom" {
		t.Fatalf("unexpected failed execution %#v", last)
	}
}

func TestDispatcherDoesNotBlockWhenExecutionStoreFails(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	called := false
	dispatcher := NewDispatcher(func(_ context.Context, _ api.QueryRequest) error {
		called = true
		return nil
	}, nil, store)
	if err := dispatcher.Dispatch(context.Background(), Definition{
		ID:       "daily",
		Enabled:  true,
		AgentKey: "demo-agent",
		Query:    Query{Message: "hello"},
	}); err != nil {
		t.Fatalf("dispatch with closed store: %v", err)
	}
	if !called {
		t.Fatal("expected dispatch to continue after execution store failure")
	}
}

func captureDispatcherLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	}()
	fn()
	return buf.String()
}
