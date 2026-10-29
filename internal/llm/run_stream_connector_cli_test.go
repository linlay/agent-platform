package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	"agent-platform/internal/view"
)

// worklogViews declares one intercepted batch command reviewed in the
// worklog view's form, next to an ordinary read-only command.
func worklogViews(t *testing.T) map[string]view.Definition {
	t.Helper()
	var action view.Action
	raw := `{"description":"批量登记工时","adapter":"cli","intercept":true,
	  "args":["--no-input","issue","worklog","add-batch",{"opt":"--payload","param":"payload","json":true},"--json"],
	  "paramsSchema":{"type":"object","additionalProperties":false,"required":["payload"],"properties":{"payload":{"type":"object"}}}}`
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		t.Fatal(err)
	}
	return map[string]view.Definition{"worklog": {Usage: []string{"display", "form"}, Renderer: "html", Actions: map[string]view.Action{"worklog.batch": action}}}
}

// mountWorklogConnector mounts a jira connector whose CLI entry is a real
// file, so the Bash review recognizes direct calls of it.
func mountWorklogConnector(t *testing.T, s *llmRunStream) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "jira")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf ok"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("jira", root)
	if err != nil {
		t.Fatal(err)
	}
	mounts := []view.Mount{{ID: "jira", Version: "1.0.0", Dir: root, Views: worklogViews(t)}}
	for _, session := range []*QuerySession{&s.session, &s.execCtx.Session} {
		session.ConnectorDirs = map[string]string{"jira": root}
		session.ConnectorBinDirs = []string{bin}
		session.ConnectorCLIEntries = entries
		session.ViewMounts = mounts
	}
	return entry
}

// worklogIntercept is the review checkBashHITL produces for argv after the
// program, without a mounted connector.
func worklogIntercept(t *testing.T, argv ...string) hitl.InterceptResult {
	t.Helper()
	found := view.FindIntercept(worklogViews(t), argv)
	if found == nil || found.Err != nil {
		t.Fatalf("fixture call not intercepted: %+v", found)
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule:        hitl.FlatRule{Mode: "form", View: &view.Reference{ConnectorID: "jira", Key: "worklog"}, RuleKey: "view-intercept:jira/worklog/worklog.batch", Title: "批量登记工时"},
		Intercept:   &hitl.ViewIntercept{ConnectorID: "jira", Prefix: []string{"jira"}, Action: *found},
	}
}

func TestInterceptedConnectorCallWaitsForItsViewForm(t *testing.T) {
	s, _, _ := newApprovedBashStream(t, 1)
	entry := mountWorklogConnector(t, s)
	call := s.queuedToolCalls[0]
	call.args["command"] = entry + ` issue worklog add-batch --payload '{"entries":[{"key":"DEMO-1"}]}'`
	// A person always reviews the form, whatever the access level.
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.execCtx.AccessLevel = level
		s.execCtx.Session.AccessLevel = level
		got := s.checkBashHITL(call)
		if !got.Intercepted || got.Conflict != "" || got.Rule.EffectiveMode() != "form" || got.Rule.IsBuiltinApproval() ||
			got.Rule.View == nil || got.Rule.View.ConnectorID != "jira" || got.Rule.View.Key != "worklog" || got.Intercept == nil {
			t.Fatalf("%s: %+v", level, got)
		}
		want := map[string]any{"entries": []any{map[string]any{"key": "DEMO-1"}}}
		if !reflect.DeepEqual(got.Intercept.Action.Params["payload"], want) || !reflect.DeepEqual(got.Intercept.Prefix, []string{entry}) {
			t.Fatalf("%s: params %+v", level, got.Intercept)
		}
	}
	if s.usesHostBashAuthorization(call) {
		t.Fatal("a rebuilt form command entered the immutable host authorization")
	}
	call.args["command"] = entry + " issue list --json"
	if got := s.checkBashHITL(call); got.Intercepted {
		t.Fatalf("an undeclared command was reviewed: %+v", got)
	}
	call.args["command"] = entry + " issue worklog add-batch"
	if got := s.checkBashHITL(call); !got.Intercepted || got.Conflict != "" || got.Intercept == nil {
		t.Fatalf("a partial call did not open the review: %+v", got)
	}
	for _, command := range []string{
		entry + ` issue worklog add-batch --payload '{}' --force`,
		entry + ` issue worklog add-batch --payload '[1]'`,
		entry + " issue list | head -1",
		"X=1 " + entry + " issue list",
	} {
		call.args["command"] = command
		got := s.checkBashHITL(call)
		if !got.Intercepted || got.Conflict == "" {
			t.Fatalf("%s ran unreviewed: %+v", command, got)
		}
	}
}

func TestInterceptedConnectorCallRunsTheReviewedParams(t *testing.T) {
	s, _, _ := newApprovedBashStream(t, 1)
	entry := mountWorklogConnector(t, s)
	executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}}
	s.engine.tools = executor
	call := s.queuedToolCalls[0]
	call.args["command"] = entry + ` issue worklog add-batch --payload '{"entries":[{"key":"DEMO-1"}]}'`
	match := s.checkBashHITL(call)
	if err := s.emitApprovalRequestDeltas(hitlApprovalRequest(call, match)); err != nil {
		t.Fatal(err)
	}
	var ask DeltaAwaitAsk
	for _, delta := range s.pending {
		if typed, ok := delta.(DeltaAwaitAsk); ok {
			ask = typed
		}
	}
	data := AnyMapNode(ask.Form["data"])
	if ask.Mode != "form" || ask.Form["title"] != "批量登记工时" || ask.Form["command"] != nil || data["action"] != "worklog.batch" {
		t.Fatalf("form: %#v", ask)
	}
	submitted := map[string]any{"payload": map[string]any{"entries": []any{map[string]any{"key": "DEMO-2", "comment": "it's done"}}}}
	ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: s.session.RunID, AwaitingID: s.hitlAwaitingID, Param: api.SubmitParam{"decision": "approve", "data": map[string]any{"params": submitted}}})
	if !ack.Accepted {
		t.Fatalf("submit: %#v", ack)
	}
	if err := s.awaitHITLSubmitAndExecute(); err != nil {
		t.Fatal(err)
	}
	want := entry + ` --no-input issue worklog add-batch --payload '{"entries":[{"comment":"it'"'"'s done","key":"DEMO-2"}]}' --json`
	if len(executor.invocations) != 1 || executor.invocations[0].args["command"] != want {
		t.Fatalf("executed %#v, want %s", executor.invocations, want)
	}
}

func TestInterceptedFormRejectsParamsOutsideTheSchema(t *testing.T) {
	match := worklogIntercept(t, "issue", "worklog", "add-batch", "--payload", `{}`)
	for _, data := range []map[string]any{
		{},
		{"params": "text"},
		{"params": map[string]any{}},
		{"params": map[string]any{"payload": map[string]any{}, "force": true}},
	} {
		if _, err := rebuildInterceptedCommand(match, data); err == nil {
			t.Fatalf("%v rebuilt", data)
		}
	}
	got, err := rebuildInterceptedCommand(match, map[string]any{"params": map[string]any{"payload": map[string]any{"a": 1.0}}})
	if err != nil || got != `jira --no-input issue worklog add-batch --payload '{"a":1}' --json` {
		t.Fatal(got, err)
	}
	if _, err := rebuildInterceptedCommand(hitl.InterceptResult{Intercepted: true}, map[string]any{"params": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatal(err)
	}
}

func TestInterceptedFormIsNeverApprovedOnBehalfOfAPerson(t *testing.T) {
	s, _, _ := newApprovedBashStream(t, 1)
	entry := mountWorklogConnector(t, s)
	call := s.queuedToolCalls[0]
	call.args["command"] = entry + ` issue worklog add-batch --payload '{}'`
	s.execCtx.AccessLevel = AccessLevelFullAccess
	s.execCtx.Session.AccessLevel = AccessLevelFullAccess
	delegate := s.mailApprovalDelegator(call)
	if delegate == nil {
		t.Fatal("no delegator")
	}
	err := delegate(context.Background(), AccessLevelFullAccess, api.SubmitRequest{Param: api.SubmitParam{"decision": "approve"}})
	if err == nil {
		t.Fatal("a superior approved a view form review")
	}
}
