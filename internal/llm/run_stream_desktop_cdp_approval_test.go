package llm

import (
	"context"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestDesktopCDPParamsFileAccessApproval(t *testing.T) {
	for _, decision := range []string{"", "approve", "approve_rule_run", "reject"} {
		t.Run(decision, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(t.TempDir(), "params.json")
			executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{backendToolDefinition("desktop_cdp")}}
			stream := &llmRunStream{
				ctx:     context.Background(),
				session: QuerySession{RunID: "run-cdp", WorkspaceRoot: root},
				engine: &LLMAgentEngine{
					cfg:   config.Config{RuntimeMode: config.RuntimeModeDesktop},
					tools: executor,
				},
				execCtx: &ExecutionContext{},
				activeToolCall: &preparedToolInvocation{
					toolID: "tool-cdp", toolName: "desktop_cdp", approvalDecision: decision,
					args: map[string]any{"method": "Runtime.evaluate", "paramsFile": path},
				},
			}
			if err := stream.invokeActiveToolCall(); err != nil {
				t.Fatal(err)
			}
			if decision == "" {
				if len(executor.invocations) != 0 || len(stream.pending) != 1 {
					t.Fatalf("tool executed before read approval: calls=%#v pending=%#v", executor.invocations, stream.pending)
				}
				ask, ok := stream.pending[0].(DeltaAwaitAsk)
				if !ok || ask.Mode != "approval" || len(ask.Approvals) != 1 {
					t.Fatalf("missing params file read approval: %#v", stream.pending)
				}
				return
			}
			if decision == "reject" {
				if len(executor.invocations) != 0 || len(stream.pending) != 1 {
					t.Fatalf("rejected tool executed: calls=%#v pending=%#v", executor.invocations, stream.pending)
				}
				result, ok := stream.pending[0].(DeltaToolResult)
				if !ok || result.Result.ExitCode != -1 {
					t.Fatalf("missing rejected result: %#v", stream.pending)
				}
				return
			}
			if len(executor.invocations) != 1 || executor.invocations[0].name != "desktop_cdp" {
				t.Fatalf("approved CDP invocation missing: %#v", executor.invocations)
			}
			if decision == "approve" && len(stream.execCtx.FileReadApprovals) != 1 {
				t.Fatalf("exact read approval missing: %#v", stream.execCtx.FileReadApprovals)
			}
			if decision == "approve_rule_run" && len(stream.execCtx.FileReadRuleApprovals) != 1 {
				t.Fatalf("run read approval missing: %#v", stream.execCtx.FileReadRuleApprovals)
			}
		})
	}
}

func TestDesktopCDPParamsFileSkipsUnnecessaryReadApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "params.json")
	for _, test := range []struct {
		name string
		mode config.RuntimeMode
		args map[string]any
	}{
		{"inline", config.RuntimeModeDesktop, map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": "document.title"}}},
		{"no params", config.RuntimeModeDesktop, map[string]any{"method": "Target.getTargets"}},
		{"conflict", config.RuntimeModeDesktop, map[string]any{"method": "Runtime.evaluate", "paramsFile": path, "params": nil}},
		{"empty path", config.RuntimeModeDesktop, map[string]any{"method": "Runtime.evaluate", "paramsFile": " "}},
		{"invalid path type", config.RuntimeModeDesktop, map[string]any{"method": "Runtime.evaluate", "paramsFile": 1}},
		{"missing method", config.RuntimeModeDesktop, map[string]any{"paramsFile": path}},
		{"standalone", config.RuntimeModeStandalone, map[string]any{"method": "Runtime.evaluate", "paramsFile": path}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &llmRunStream{
				session: QuerySession{WorkspaceRoot: t.TempDir()},
				engine:  &LLMAgentEngine{cfg: config.Config{RuntimeMode: test.mode}},
			}
			if plan, ok := stream.buildFileAccessPlan(&preparedToolInvocation{toolName: "desktop_cdp", args: test.args}); ok || plan != nil {
				t.Fatalf("unexpected read plan: %#v", plan)
			}
		})
	}
}
