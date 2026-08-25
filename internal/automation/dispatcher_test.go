package automation

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

type capturingExecutionRecorder struct {
	items []Execution
}

func (r *capturingExecutionRecorder) Submit(item Execution) {
	r.items = append(r.items, cloneExecution(item))
}

func successfulTestQuery(req api.QueryRequest, hooks QueryRunHooks) QueryRunResult {
	now := time.Now().UnixMilli()
	runID := "run-" + NewExecutionID()
	if hooks.OnRunStarted != nil {
		hooks.OnRunStarted(chat.RunStart{ChatID: req.ChatID, RunID: runID, StartedAtMillis: now})
	}
	return QueryRunResult{Completion: &chat.RunCompletion{
		ChatID:          req.ChatID,
		RunID:           runID,
		AssistantText:   "done",
		InitialMessage:  req.Message,
		FinishReason:    "complete",
		StartedAtMillis: now,
		UpdatedAtMillis: now + 1,
	}}
}

func TestDispatcherBuildsStructuredQueryRequest(t *testing.T) {
	def := Definition{
		ID:          "daily",
		Name:        "Daily Summary",
		Description: "Summarize the day",
		Enabled:     true,
		Cron:        "0 9 * * *",
		AgentKey:    "demo-agent",
		SourceFile:  "/tmp/daily.yml",
		Query: Query{
			RequestID: "req-1",
			ChatID:    "123e4567-e89b-12d3-a456-426614174000",
			Message:   "hello",
			Params:    map[string]any{"existing": "value"},
			References: []api.Reference{
				{ID: "ref-1", Type: "url", URL: "https://example.com"},
			},
			Scene: &api.Scene{URL: "https://example.com/app", Title: "demo"},
		},
	}

	var got api.QueryRequest
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
		got = req
		return successfulTestQuery(req, hooks), nil
	}, nil, nil)
	if err := dispatcher.Dispatch(context.Background(), def, "Asia/Shanghai"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got.RequestID != "req-1" || got.ChatID != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("unexpected ids %#v", got)
	}
	if got.AgentKey != "demo-agent" || got.TeamID != "" {
		t.Fatalf("unexpected target %#v", got)
	}
	if got.Role != "automation" || got.Message != "hello" {
		t.Fatalf("unexpected role/message %#v", got)
	}
	if got.Hidden == nil || !*got.Hidden {
		t.Fatalf("expected omitted automation query.hidden to execute as hidden, got %#v", got)
	}
	if len(got.References) != 1 || got.Scene == nil || got.Scene.Title != "demo" {
		t.Fatalf("unexpected refs/scene %#v", got)
	}
	if got.Params["existing"] != "value" {
		t.Fatalf("expected existing params, got %#v", got.Params)
	}
	if got.ChatSource != "automation:daily" {
		t.Fatalf("expected automation chat source, got %#v", got)
	}
	if _, ok := got.Params["__automation"]; ok {
		t.Fatalf("expected automation dispatch not to inject __automation metadata, got %#v", got.Params)
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
		SourceFile:  "/tmp/daily.yml",
		Query:       Query{Message: "hello"},
	}

	successLogs := captureDispatcherLogs(t, func() {
		dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
			return successfulTestQuery(req, hooks), nil
		}, nil, nil)
		if err := dispatcher.Dispatch(context.Background(), def, "Asia/Shanghai"); err != nil {
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
		dispatcher := NewDispatcher(func(_ context.Context, _ api.QueryRequest, _ QueryRunHooks) (QueryRunResult, error) {
			return QueryRunResult{}, errors.New("boom")
		}, nil, nil)
		err := dispatcher.Dispatch(context.Background(), def, "Asia/Shanghai")
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
		SourceFile:  "/tmp/daily.yml",
		Query:       Query{Message: "hello"},
	}

	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
		return successfulTestQuery(req, hooks), nil
	}, nil, store)
	if err := dispatcher.Dispatch(context.Background(), def, "Asia/Shanghai"); err != nil {
		t.Fatalf("dispatch success: %v", err)
	}
	items, total, err := store.ListByAutomation("daily", 10, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ZoneID != "Asia/Shanghai" || items[0].Status != "success" || items[0].DurationMs == nil {
		t.Fatalf("unexpected success execution total=%d items=%#v", total, items)
	}

	expectedErr := errors.New("boom")
	// Execution ordering is millisecond-granular; move the second execution into
	// a distinct timestamp so this test verifies its lifecycle rather than ID
	// tie-breaking.
	time.Sleep(time.Millisecond)
	dispatcher = NewDispatcher(func(_ context.Context, _ api.QueryRequest, _ QueryRunHooks) (QueryRunResult, error) {
		return QueryRunResult{}, expectedErr
	}, nil, store)
	if err := dispatcher.Dispatch(context.Background(), def, "UTC"); !errors.Is(err, expectedErr) {
		t.Fatalf("expected dispatch error, got %v", err)
	}
	last, err := store.LastExecution("daily")
	if err != nil {
		t.Fatalf("last execution: %v", err)
	}
	if last == nil || last.ZoneID != "UTC" || last.Status != "failed" || last.Error != "boom" {
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
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
		called = true
		return successfulTestQuery(req, hooks), nil
	}, nil, store)
	if err := dispatcher.Dispatch(context.Background(), Definition{
		ID:       "daily",
		Enabled:  true,
		AgentKey: "demo-agent",
		Query:    Query{Message: "hello"},
	}, "Asia/Shanghai"); err != nil {
		t.Fatalf("dispatch with closed store: %v", err)
	}
	if !called {
		t.Fatal("expected dispatch to continue after execution store failure")
	}
}

func TestDispatcherMapsPersistedErrorAndCancelCompletionsWithPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name         string
		finishReason string
		queryErr     error
		wantStatus   string
	}{
		{name: "error", finishReason: "error", queryErr: errors.New("model failed"), wantStatus: ExecutionStatusFailed},
		{name: "cancel", finishReason: "cancel", queryErr: context.Canceled, wantStatus: ExecutionStatusCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &capturingExecutionRecorder{}
			dispatchCalls := 0
			dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
				dispatchCalls++
				now := time.Now().UnixMilli()
				start := chat.RunStart{ChatID: "chat-partial", RunID: "run-partial", StartedAtMillis: now}
				hooks.OnRunStarted(start)
				return QueryRunResult{Completion: &chat.RunCompletion{
					ChatID:          start.ChatID,
					RunID:           start.RunID,
					AssistantText:   "已经产生的部分助手输出",
					InitialMessage:  req.Message,
					FinishReason:    tc.finishReason,
					StartedAtMillis: now,
					UpdatedAtMillis: now + 10,
				}}, tc.queryErr
			}, nil, recorder)
			err := dispatcher.Dispatch(context.Background(), Definition{ID: "daily", Name: "Daily", Enabled: true, AgentKey: "agent-a", Query: Query{Message: "hello"}}, "UTC")
			if !errors.Is(err, tc.queryErr) {
				t.Fatalf("dispatcher changed original query error: got %v want %v", err, tc.queryErr)
			}
			if dispatchCalls != 1 {
				t.Fatalf("query called %d times, want exactly once", dispatchCalls)
			}
			if len(recorder.items) != 3 {
				t.Fatalf("execution lifecycle snapshots=%d want 3: %#v", len(recorder.items), recorder.items)
			}
			terminal := recorder.items[len(recorder.items)-1]
			if terminal.Status != tc.wantStatus || terminal.FinishReason != tc.finishReason || terminal.ResultContent != "已经产生的部分助手输出" {
				t.Fatalf("unexpected terminal execution %#v", terminal)
			}
		})
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
