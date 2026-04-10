package schedule

import (
	"context"
	"testing"

	"agent-platform-runner-go/internal/api"
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
	})
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
	meta, ok := got.Params["__schedule"].(map[string]any)
	if !ok {
		t.Fatalf("expected __schedule metadata, got %#v", got.Params)
	}
	if meta["scheduleId"] != "daily" || meta["scheduleName"] != "Daily Summary" || meta["sourceFile"] != "/tmp/daily.yml" {
		t.Fatalf("unexpected __schedule metadata %#v", meta)
	}
}
