package llm

import (
	"context"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// File inputs of the web-control tools follow the standard read approval:
// awcp_invoke.paramsFile. Scripts and raw CDP are not web-control tools.
func TestSurfaceFileInputAccessApproval(t *testing.T) {
	for _, tool := range []struct {
		name string
		args func(path string) map[string]any
	}{
		{"awcp_invoke", func(path string) map[string]any {
			return map[string]any{"surfaceId": "page:1", "paramsFile": path}
		}},
	} {
		for _, decision := range []string{"", "approve", "approve_rule_run", "reject"} {
			t.Run(tool.name+"/"+decision, func(t *testing.T) {
				testSurfaceFileInputAccessApproval(t, tool.name, tool.args, decision)
			})
		}
	}
}

func testSurfaceFileInputAccessApproval(t *testing.T, toolName string, toolArgs func(string) map[string]any, decision string) {
	{
		{
			root := t.TempDir()
			path := filepath.Join(t.TempDir(), "params.json")
			executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{backendToolDefinition(toolName)}}
			stream := &llmRunStream{
				ctx:     context.Background(),
				session: QuerySession{RunID: "run-cdp", WorkspaceRoot: root},
				engine: &LLMAgentEngine{
					cfg:   config.Config{RuntimeMode: config.RuntimeModeDesktop},
					tools: executor,
				},
				execCtx: &ExecutionContext{},
				activeToolCall: &preparedToolInvocation{
					toolID: "tool-cdp", toolName: toolName, approvalDecision: decision,
					args: toolArgs(path),
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
			if len(executor.invocations) != 1 || executor.invocations[0].name != toolName {
				t.Fatalf("approved CDP invocation missing: %#v", executor.invocations)
			}
			if decision == "approve" && len(stream.execCtx.FileReadApprovals) != 1 {
				t.Fatalf("exact read approval missing: %#v", stream.execCtx.FileReadApprovals)
			}
			if decision == "approve_rule_run" && len(stream.execCtx.FileReadRuleApprovals) != 1 {
				t.Fatalf("run read approval missing: %#v", stream.execCtx.FileReadRuleApprovals)
			}
		}
	}
}

func TestSurfaceFileInputSkipsUnnecessaryReadApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "params.json")
	for _, test := range []struct {
		name string
		mode config.RuntimeMode
		tool string
		args map[string]any
	}{
		{"inline", config.RuntimeModeDesktop, "awcp_invoke", map[string]any{"surfaceId": "page:1", "revision": "r1", "action": "a", "args": map[string]any{}}},
		{"empty path", config.RuntimeModeDesktop, "awcp_invoke", map[string]any{"surfaceId": "page:1", "paramsFile": " "}},
		{"removed raw CDP tool", config.RuntimeModeDesktop, "surface_cdp", map[string]any{"method": "Runtime.evaluate", "paramsFile": path}},
		{"removed script tool", config.RuntimeModeDesktop, "surface_evaluate", map[string]any{"surfaceId": "page:1", "expressionFile": path}},
		// The removed tool no longer plans a file read.
		{"removed tool", config.RuntimeModeDesktop, "desktop_cdp", map[string]any{"method": "Runtime.evaluate", "paramsFile": path}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &llmRunStream{
				session: QuerySession{WorkspaceRoot: t.TempDir()},
				engine:  &LLMAgentEngine{cfg: config.Config{RuntimeMode: test.mode}},
			}
			if plan, ok := stream.buildFileAccessPlan(&preparedToolInvocation{toolName: test.tool, args: test.args}); ok || plan != nil {
				t.Fatalf("unexpected read plan: %#v", plan)
			}
		})
	}
}
