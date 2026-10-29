package viewaction

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
)

type invoker struct {
	req    connectorops.Request
	scope  connectorops.Scope
	calls  int
	result connectorops.Result
	err    error
}

func (f *invoker) Invoke(_ context.Context, scope connectorops.Scope, req connectorops.Request) (connectorops.Result, error) {
	f.calls++
	f.req = req
	f.scope = scope
	return f.result, f.err
}
func setup(t *testing.T) (*Service, view.Document, *invoker, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "crm")
	os.MkdirAll(filepath.Join(dir, "views"), 0700)
	action := view.Action{Query: view.Query{Adapter: "cli", Args: []view.QueryArg{{Literal: "transition"}, {Param: "key"}, {Literal: "--fields-file"}, {Literal: "-"}}, ParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"key":{"type":"string","pattern":"^[A-Z]+-[1-9][0-9]*$"},"fields":{"type":"object"}},"required":["key","fields"]}`)}, StdinParam: "fields"}
	def := view.Definition{Description: "Orders", Renderer: "html", Usage: []string{"display"}, InputSchema: json.RawMessage(`{"type":"object"}`), Entry: "views/orders.html", Actions: map[string]view.Action{"transition": action}}
	for name, value := range map[string]any{"connector.json": map[string]any{"id": "crm", "name": "CRM", "version": "1.0.0", "type": "cli", "authMode": "none"}, "cli.json": map[string]any{}, "view.json": view.Config{Views: map[string]view.Definition{"orders": def}}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "views/orders.html"), []byte("<p>Orders</p>"), 0600)
	f := &invoker{result: connectorops.Result{Stdout: `{"data":{"applied":true}}`, InvocationID: "call-1"}}
	return &Service{Sources: connector.Sources{ExternalRoot: root}, Connectors: f, Authorize: func(context.Context, Context, string) error { return nil }}, view.Document{View: view.Reference{Source: "connector", ConnectorID: "crm", Key: "orders", Version: "1.0.0"}, Actions: def.Actions}, f, dir
}
func actor() Context {
	return Context{ChatID: "chat-1", RunID: "run-1", AgentKey: "agent-1", Subject: "alice"}
}
func TestActionExactScopeStdinAndAudit(t *testing.T) {
	s, doc, f, _ := setup(t)
	got, err := s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{"resolution":{"id":"1"}}}`), "request-001", actor())
	if err != nil {
		t.Fatal(err)
	}
	if got.InvocationID != "call-1" || got.ExecutedAt <= 0 || f.req.Stdin != `{"resolution":{"id":"1"}}` || f.req.IdempotencyKey != "request-001" || len(f.req.Args) != 4 {
		t.Fatalf("%+v %+v", got, f.req)
	}
	if f.scope.Subject != "view:alice" || f.scope.IdempotencyNamespace != "view:chat-1:orders:transition" || len(f.scope.Execution) != 1 || f.scope.Execution[0].ConnectorID != "crm" || f.scope.Audit["subject"] != "alice" || f.scope.Check() != nil {
		t.Fatalf("scope %+v", f.scope)
	}
	s.Authorize = func(context.Context, Context, string) error { return ErrDenied }
	if f.scope.Check() == nil {
		t.Fatal("revoked grant still valid")
	}
}
func TestActionRejectsUndeclaredInvalidAndRevokedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, params, id string
		want             error
	}{
		{"missing", `{}`, "request-001", view.ErrInvalid},
		{"transition", `{"key":"--exec","fields":{}}`, "request-001", view.ErrDataInvalid},
		{"transition", `{"key":"DEMO-1","fields":{},"url":"https://evil"}`, "request-001", view.ErrDataInvalid},
		{"transition", `{"key":"DEMO-1","fields":{}}`, "", view.ErrDataInvalid},
	} {
		s, doc, f, _ := setup(t)
		_, err := s.Execute(context.Background(), doc, tc.name, json.RawMessage(tc.params), tc.id, actor())
		if !errors.Is(err, tc.want) || f.calls != 0 {
			t.Fatalf("%s err %v calls %d", tc.name, err, f.calls)
		}
	}
	s, doc, f, dir := setup(t)
	os.WriteFile(filepath.Join(dir, "view.json"), []byte(`{"views":{"orders":{"description":"Orders","renderer":"html","usage":["display"],"inputSchema":{"type":"object"},"entry":"views/orders.html"}}}`), 0600)
	_, err := s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{}}`), "request-001", actor())
	if !errors.Is(err, ErrStale) || f.calls != 0 {
		t.Fatal(err, f.calls)
	}
}
func TestActionStaysWritableWhenOnlyInterceptChanges(t *testing.T) {
	s, doc, f, dir := setup(t)
	write := func(change func(*view.Action)) map[string]view.Action {
		var config view.Config
		raw, _ := os.ReadFile(filepath.Join(dir, "view.json"))
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		orders := config.Views["orders"]
		orders.Usage = []string{"display", "form"}
		action := orders.Actions["transition"]
		change(&action)
		orders.Actions["transition"] = action
		config.Views["orders"] = orders
		raw, _ = json.Marshal(config)
		if err := os.WriteFile(filepath.Join(dir, "view.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return orders.Actions
	}
	// A page rendered before the action was intercepted keeps writing.
	doc.Actions = write(func(a *view.Action) { a.StdinParam = "" })
	write(func(a *view.Action) { a.Intercept = true })
	if _, err := s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{}}`), "request-001", actor()); err != nil || f.calls != 1 {
		t.Fatalf("intercept made the page stale: %v calls %d", err, f.calls)
	}
	write(func(a *view.Action) { a.Args = append(a.Args, view.QueryArg{Literal: "--force"}) })
	if _, err := s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{}}`), "request-002", actor()); !errors.Is(err, ErrStale) || f.calls != 1 {
		t.Fatalf("a changed write contract stayed valid: %v calls %d", err, f.calls)
	}
}
func TestActionKeepsBusinessErrorAndTreatsNonJSONAsUnknown(t *testing.T) {
	s, doc, f, _ := setup(t)
	f.result = connectorops.Result{ExitCode: 6, Stdout: `{"error":{"outcome":"partial","message":"partial"}}`}
	result, err := s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{}}`), "request-001", actor())
	if err != nil || result.ExitCode != 6 {
		t.Fatal(result, err)
	}
	f.result.Stdout = "not JSON"
	_, err = s.Execute(context.Background(), doc, "transition", json.RawMessage(`{"key":"DEMO-1","fields":{}}`), "request-001", actor())
	if err == nil || err.Error() != "invocation_outcome_unknown" {
		t.Fatal(err)
	}
}
