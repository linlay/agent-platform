package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func TestDesktopCDPParamsFileSendsParamsThroughExistingRequest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "params.json")
	chatDir := t.TempDir()
	tempDir := t.TempDir()
	for _, dir := range []string{root, chatDir, tempDir} {
		mustWriteFile(t, filepath.Join(dir, "params.json"), `{"expression":"document.title + '\\n标题'","returnByValue":"true","awaitPromise":false,"nested":{"pierce":"false"},"items":[1,null,"text"]}`)
	}
	for _, input := range []string{"params.json", "@workspace/params.json", "@chat/params.json", "@temp/params.json", path, "/workspace/params.json"} {
		t.Run(input, func(t *testing.T) {
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			execCtx.Session.RuntimeContext.LocalPaths.ChatDir = chatDir
			execCtx.Session.TempRoot = tempDir
			if input == "/workspace/params.json" {
				execCtx.Session.AgentHasRuntimeSandbox = true
				execCtx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
			}
			result, err := executor.Invoke(context.Background(), "desktop_cdp", map[string]any{
				"method": "Runtime.evaluate", "paramsFile": input,
				"requestId": "request-params", "targetId": "target-1", "sessionId": "session-1", "surfaceId": "surface-1",
			}, execCtx)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("paramsFile failed: result=%#v err=%v", result, err)
			}
			_, requests := invoker.snapshots()
			if len(requests) != 1 || requests[0].Type != desktopCDPRequestType || requests[0].ID != "request-params" {
				t.Fatalf("unexpected requests: %#v", requests)
			}
			payload := requests[0].Payload
			wantParams := map[string]any{
				"expression": "document.title + '\\n标题'", "returnByValue": true, "awaitPromise": false,
				"nested": map[string]any{"pierce": false}, "items": []any{float64(1), nil, "text"},
			}
			if !reflect.DeepEqual(payload["params"], wantParams) {
				t.Fatalf("params mismatch: %#v", payload["params"])
			}
			for key, want := range map[string]string{"method": "Runtime.evaluate", "targetId": "target-1", "sessionId": "session-1", "surfaceId": "surface-1"} {
				if payload[key] != want {
					t.Fatalf("%s = %#v, want %s", key, payload[key], want)
				}
			}
			if _, exists := payload["paramsFile"]; exists {
				t.Fatalf("paramsFile leaked to Desktop: %#v", payload)
			}
			source, _ := payload["source"].(map[string]any)
			if source["runId"] != execCtx.Session.RunID || source["chatId"] != execCtx.Session.ChatID || source["agentKey"] != execCtx.Session.AgentKey {
				t.Fatalf("unexpected source: %#v", source)
			}
		})
	}
}

func TestDesktopCDPParamsFileRejectsInvalidInputBeforeSending(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "valid.json"), `{}`)
	for name, data := range map[string]string{
		"empty": "", "malformed": `{"expression":`, "array": `[]`, "null": `null`,
		"string": `"text"`, "number": `12`, "boolean": `true`, "trailing": `{} {}`,
		"utf8": "{\"expression\":\"\xff\"}",
	} {
		mustWriteFile(t, filepath.Join(root, name+".json"), data)
	}
	type testCase struct {
		name string
		args map[string]any
		code string
	}
	tests := []testCase{
		{"mutually exclusive", map[string]any{"params": map[string]any{}, "paramsFile": "missing.json"}, "invalid_args"},
		{"null params conflict", map[string]any{"params": nil, "paramsFile": "valid.json"}, "invalid_args"},
		{"empty path", map[string]any{"paramsFile": "  "}, "invalid_args"},
		{"null path", map[string]any{"paramsFile": nil}, "invalid_args"},
		{"non-string path", map[string]any{"paramsFile": 1}, "invalid_args"},
		{"missing", map[string]any{"paramsFile": "missing.json"}, "desktop_cdp_params_file_read_failed"},
		{"directory", map[string]any{"paramsFile": "."}, "desktop_cdp_params_file_invalid_file"},
		{"workspace traversal", map[string]any{"paramsFile": "../params.json"}, "desktop_cdp_params_file_approval_required"},
		{"alias traversal", map[string]any{"paramsFile": "@workspace/../params.json"}, "desktop_cdp_params_file_invalid_path"},
	}
	for _, name := range []string{"empty", "malformed", "array", "null", "string", "number", "boolean", "trailing", "utf8"} {
		tests = append(tests, testCase{name, map[string]any{"paramsFile": name + ".json"}, "desktop_cdp_params_file_invalid_json"})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			test.args["method"] = "Runtime.evaluate"
			result, err := executor.invokeDesktopCDP(context.Background(), test.args, execCtx)
			if err != nil || result.ExitCode != -1 || result.Error != test.code {
				t.Fatalf("result=%#v err=%v, want %s", result, err, test.code)
			}
			_, requests := invoker.snapshots()
			if len(requests) != 0 {
				t.Fatalf("invalid params sent a request: %#v", requests)
			}
		})
	}
}

func TestDesktopCDPParamsFileReadLimitAndWorkspaceRequired(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "params.json"), "{}\n")
	executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
	args := map[string]any{"method": "Runtime.evaluate", "paramsFile": "params.json"}
	executor.cfg.FileTools.MaxReadBytes = 2
	result, err := executor.invokeDesktopCDP(context.Background(), args, execCtx)
	if err != nil || result.Error != "desktop_cdp_params_file_too_large" {
		t.Fatalf("expected size failure: result=%#v err=%v", result, err)
	}
	execCtx.Session.WorkspaceRoot = ""
	result, err = executor.invokeDesktopCDP(context.Background(), args, execCtx)
	if err != nil || result.Error != "workspace_unavailable" {
		t.Fatalf("expected missing workspace failure: result=%#v err=%v", result, err)
	}
	_, requests := invoker.snapshots()
	if len(requests) != 0 {
		t.Fatalf("invalid file sent a request: %#v", requests)
	}
	execCtx.Session.WorkspaceRoot = root
	executor.cfg.FileTools.MaxReadBytes = 3
	result, err = executor.invokeDesktopCDP(context.Background(), args, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("expected exact size limit success: result=%#v err=%v", result, err)
	}
}

func TestDesktopCDPParamsFileReadPermissions(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "params.json")
	mustWriteFile(t, outside, `{"expression":"document.title"}`)
	for _, mode := range []string{"approval", "rule", "block", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			executor, execCtx, invoker := desktopCDPParamsTestRuntime(root)
			input := outside
			if mode == "symlink" {
				input = filepath.Join(root, "linked.json")
				if err := os.Symlink(outside, input); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			args := map[string]any{"method": "Runtime.evaluate", "paramsFile": input}
			result, err := executor.invokeDesktopCDP(context.Background(), args, execCtx)
			if err != nil || result.Error != "desktop_cdp_params_file_approval_required" {
				t.Fatalf("expected read approval: result=%#v err=%v", result, err)
			}
			canonical, err := filepath.EvalSymlinks(outside)
			if err != nil {
				t.Fatal(err)
			}
			if result.Structured["filePath"] != canonical {
				t.Fatalf("approval must identify canonical file: %#v", result.Structured)
			}
			_, requests := invoker.snapshots()
			if len(requests) != 0 {
				t.Fatalf("unapproved params sent: %#v", requests)
			}
			if mode == "symlink" {
				return
			}
			if mode == "rule" {
				filetools.RegisterRuleReadApproval(execCtx, result.Structured["ruleKey"].(string))
			} else {
				filetools.RegisterExactReadApproval(execCtx, result.Structured["fingerprint"].(string))
			}
			if mode == "block" {
				level := executor.cfg.AccessPolicy.Levels[AccessLevelDefault]
				level.Approvals.ReadOutsideRoots = "block"
				executor.cfg.AccessPolicy.Levels[AccessLevelDefault] = level
			}
			result, err = executor.invokeDesktopCDP(context.Background(), args, execCtx)
			if mode == "block" {
				_, requests = invoker.snapshots()
				if err != nil || result.Error != "desktop_cdp_params_file_path_blocked" || len(requests) != 0 {
					t.Fatalf("approval bypassed block: result=%#v err=%v requests=%#v", result, err, requests)
				}
				return
			}
			if err != nil || result.ExitCode != 0 || len(execCtx.FileReadApprovals) != 0 {
				t.Fatalf("approved read failed: result=%#v err=%v", result, err)
			}
			result, err = executor.invokeDesktopCDP(context.Background(), args, execCtx)
			if mode == "rule" {
				if err != nil || result.ExitCode != 0 {
					t.Fatalf("rule approval not reused: result=%#v err=%v", result, err)
				}
			} else if err != nil || result.Error != "desktop_cdp_params_file_approval_required" {
				t.Fatalf("one-shot approval reused: result=%#v err=%v", result, err)
			}
		})
	}
}

func desktopCDPParamsTestRuntime(root string) (*RuntimeToolExecutor, *ExecutionContext, *routingClientRequestInvoker) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			RuntimeMode: config.RuntimeModeDesktop,
			AccessPolicy: config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
				AccessLevelDefault: {ReadRoots: []string{"@workspace", "@chat"}, Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "hitl"}},
			}},
		},
		clientRequest: invoker,
		clientTargets: emptyRunClientTargetStore{},
	}
	execCtx := desktopActionTestExecutionContext()
	execCtx.Session.WorkspaceRoot = root
	return executor, execCtx, invoker
}
