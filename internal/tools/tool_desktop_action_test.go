package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "agent-platform-runner-go/internal/contracts"
)

func TestInvokeDesktopActionCallsBridge(t *testing.T) {
	var got desktopActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"action":"desktop.controlCenter.listServices","result":{"count":1}}`))
	}))
	defer server.Close()
	t.Setenv("DESKTOP_ACTION_BRIDGE_URL", server.URL)

	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.controlCenter.listServices",
		"args": map[string]any{
			"include": "all",
		},
	}, &ExecutionContext{Session: QuerySession{
		RunID:    "run-1",
		ChatID:   "chat-1",
		AgentKey: "desktopAssistant",
	}})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Action != "desktop.controlCenter.listServices" {
		t.Fatalf("unexpected action: %s", got.Action)
	}
	if got.Args["include"] != "all" {
		t.Fatalf("unexpected args: %#v", got.Args)
	}
	if got.Source.RunID != "run-1" || got.Source.ChatID != "chat-1" || got.Source.AgentKey != "desktopAssistant" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
}

func TestInvokeDesktopActionAllowsEmbeddedWebReadPageData(t *testing.T) {
	var got desktopActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"action":"desktop.embeddedWeb.readPageData","result":{"title":"Bing"}}`))
	}))
	defer server.Close()
	t.Setenv("DESKTOP_ACTION_BRIDGE_URL", server.URL)

	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.embeddedWeb.readPageData",
		"args": map[string]any{
			"include": []any{"links"},
		},
	}, &ExecutionContext{Session: QuerySession{RunID: "run-web", ChatID: "chat-web"}})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful exit code, got %d: %s", result.ExitCode, result.Output)
	}
	if got.Action != "desktop.embeddedWeb.readPageData" {
		t.Fatalf("unexpected action: %s", got.Action)
	}
	if got.Source.RunID != "run-web" || got.Source.ChatID != "chat-web" {
		t.Fatalf("unexpected source: %#v", got.Source)
	}
}

func TestInvokeDesktopActionRejectsUnknownAction(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).invokeDesktopAction(context.Background(), map[string]any{
		"action": "desktop.unlisted.anything",
	}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke desktop action: %v", err)
	}
	if result.ExitCode != -1 || result.Error != "unknown_action" {
		t.Fatalf("expected unknown_action failure, got exit=%d error=%q output=%s", result.ExitCode, result.Error, result.Output)
	}
}
