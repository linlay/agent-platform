package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
	"agent-platform/internal/viewaction"
)

type viewActionInvoker struct {
	calls int
	scope connectorops.Scope
}

func (f *viewActionInvoker) Invoke(_ context.Context, scope connectorops.Scope, _ connectorops.Request) (connectorops.Result, error) {
	f.calls++
	f.scope = scope
	return connectorops.Result{Stdout: `{"data":{"applied":true}}`, InvocationID: "once"}, nil
}
func TestViewActionHTTPUsesLiveChatOwnershipAndPublishingRun(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-action"
	const runID = "run-action"
	if _, _, err := fixture.chats.EnsureChatWithSource(chatID, "mock-agent", "action", "query:alice"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{Type: "query", ChatID: chatID, RunID: runID, AgentKey: "mock-agent", UpdatedAt: testEpochMillis}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	os.MkdirAll(filepath.Join(dir, "views"), 0700)
	action := view.Action{Query: view.Query{Adapter: "cli", Args: []view.QueryArg{{Literal: "write"}, {Param: "key"}}, ParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"key":{"type":"string"}},"required":["key"]}`)}}
	definition := view.Definition{Description: "Demo", Renderer: "html", Usage: []string{"display"}, Entry: "views/card.html", Actions: map[string]view.Action{"update": action}}
	for name, value := range map[string]any{"connector.json": map[string]any{"id": "demo", "name": "Demo", "type": "cli", "version": "1.0.0", "authMode": "none"}, "cli.json": map[string]any{}, "view.json": view.Config{Views: map[string]view.Definition{"card": definition}}} {
		raw, _ := json.Marshal(value)
		os.WriteFile(filepath.Join(dir, name), raw, 0600)
	}
	os.WriteFile(filepath.Join(dir, "views/card.html"), []byte("<p>Demo</p>"), 0600)
	ref, err := view.SaveSnapshot(fixture.chats.ChatDir(chatID), view.Document{View: view.Reference{Source: "connector", ConnectorID: "demo", Key: "card", Renderer: "html"}, HTML: "<p>Demo</p>", Actions: definition.Actions})
	if err != nil {
		t.Fatal(err)
	}
	invoker := &viewActionInvoker{}
	fixture.server.deps.ViewActions = &viewaction.Service{Sources: connector.Sources{ExternalRoot: root}, Connectors: invoker, Authorize: func(_ context.Context, actor viewaction.Context, id string) error {
		if actor.RunID != runID || actor.AgentKey != "mock-agent" || id != "demo" {
			return viewaction.ErrDenied
		}
		return nil
	}}
	body := map[string]any{"chatId": chatID, "runId": runID, "view": map[string]any{"connectorId": "demo", "key": "card", "hash": ref.Hash}, "action": "update", "params": map[string]any{"key": "DEMO-1"}, "requestId": "request-0001"}
	post := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/view/invoke", bytes.NewReader(raw)).WithContext(WithPrincipal(context.Background(), &Principal{Subject: "alice"})))
		return rec
	}
	if rec := post(); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"invocationId":"once"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if invoker.calls != 1 || invoker.scope.Audit["runId"] != runID {
		t.Fatal(invoker.calls, invoker.scope)
	}
	body["runId"] = "foreign-run"
	if rec := post(); rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body["runId"] = runID
	body["params"] = map[string]any{"key": "DEMO-1", "url": "https://evil"}
	if rec := post(); rec.Code != 400 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if invoker.calls != 1 {
		t.Fatal("invalid request dispatched")
	}

	body["params"] = map[string]any{"key": "DEMO-1"}
	raw, _ := json.Marshal(body)
	var req ViewActionRequest
	json.Unmarshal(raw, &req)
	_, err = fixture.server.invokeView(WithPrincipal(context.Background(), &Principal{Subject: "bob"}), req)
	if err == nil {
		t.Fatal("foreign viewer allowed")
	}
	_, err = fixture.server.invokeView(context.Background(), req)
	if err == nil {
		t.Fatal("anonymous viewer allowed on owned chat")
	}
	if invoker.calls != 1 {
		t.Fatal("unauthorized write dispatched")
	}
}
