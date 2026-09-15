package tools

import (
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopActionTransportDiagnostics(t *testing.T) {
	// Exact frame shape produced by Desktop Broker; no internal result/error envelope.
	raw := []byte(`{"frame":"error","type":"invalid_args","id":"action-invalid","code":400,"msg":"args.input must be an object.","data":{"action":"desktop.kanban.createIssue","details":{"issues":[{"path":"args.input","code":"required","expected":"object","actual":"missing"}],"recovery":"Read desktop-action/references/kanban.md"}}}`)
	var frame ClientResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{frame}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeStandalone}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	// Invoke the shared reverse transport directly to inspect this Desktop frame.
	result, err := executor.invokeDesktopClientRequest(context.Background(), "action-invalid", "desktop.kanban.createIssue", map[string]any{}, nil, "desktop_action", false,
		&ExecutionContext{Session: QuerySession{RunID: "run-1", ChatID: "chat-1", AgentKey: "agent-1"}})
	if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	details := result.Structured["details"].(map[string]any)
	if details["clientErrorType"] != "invalid_args" || details["recovery"] != "Read desktop-action/references/kanban.md" {
		t.Fatalf("missing metadata: %#v", details)
	}
	issues := details["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["path"] != "args.input" {
		t.Fatalf("missing field path: %#v", issues)
	}
}

func TestDesktopActionDiagnosticsAreBounded(t *testing.T) {
	raw := make([]any, 20)
	for i := range raw {
		raw[i] = map[string]any{"path": "args.input", "code": "required", "expected": "object", "actual": "secret-value", "value": "secret-value", "nested": map[string]any{"token": "secret"}}
	}
	raw[0].(map[string]any)["expected"] = strings.Repeat("x", 257)
	out := map[string]any{}
	appendDesktopActionIssues(out, map[string]any{"issues": raw})
	issues := out["issues"].([]any)
	if len(issues) != 16 {
		t.Fatalf("unbounded: %d", len(issues))
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), strings.Repeat("x", 257)) {
		t.Fatalf("unsafe diagnostics: %s", encoded)
	}
}

func TestDesktopActionWorkerDiagnosticsSurviveTransport(t *testing.T) {
	code := 500
	frame := ClientResponseFrame{Code: &code, Data: []byte(`{"details":{"category":"internal","stage":"internal","executionState":"not_started","diagnosticId":"diag-1","cause":{"code":"ENOENT","message":"missing /Users/example/private/worker.js token=secret-value"},"recovery":{"strategy":"repair_host","message":"Rebuild Desktop Main and Worker together."},"context":{"workerEntry":"dist-electron/main/webapp-tooling-worker.js","token":"secret-value"}}}`)}
	details := desktopClientRejectionDetails(frame)
	if details["stage"] != "internal" || details["executionState"] != "not_started" || details["diagnosticId"] != "diag-1" {
		t.Fatalf("lost diagnostics: %#v", details)
	}
	if details["cause"].(map[string]any)["code"] != "ENOENT" || details["recovery"].(map[string]any)["strategy"] != "repair_host" {
		t.Fatalf("lost root cause: %#v", details)
	}
	encoded, _ := json.Marshal(details)
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "/Users/") {
		t.Fatalf("unsafe diagnostics: %s", encoded)
	}
}

func TestDesktopActionSourceUsesFileToolsWorkspace(t *testing.T) {
	root := t.TempDir()
	chat := t.TempDir()
	session := QuerySession{RunID: "run-1", ChatID: "chat-1", RunOwner: AgentRunOwner("agent-1", "agent-1"), RuntimeContext: RuntimeRequestContext{LocalPaths: LocalPaths{WorkspaceDir: root, ChatDir: chat}}}
	source, err := buildDesktopActionSource(&ExecutionContext{Session: session})
	if err != nil || source.WorkspaceRoot != root {
		t.Fatalf("workspace mismatch: %#v %v", source, err)
	}
	session.RuntimeContext.LocalPaths.WorkspaceDir = ""
	source, err = buildDesktopActionSource(&ExecutionContext{Session: session})
	if err != nil || source.WorkspaceRoot != "" {
		t.Fatalf("must not infer workspace from Chat directory: %#v %v", source, err)
	}
}
