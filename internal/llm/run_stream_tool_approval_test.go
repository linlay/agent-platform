package llm

import (
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinteraction"
	"context"
	"testing"
)

type exactApprovalExecutor struct{ recordingToolExecutor }

func (e *exactApprovalExecutor) PrepareToolApproval(_ context.Context, _ string, _ map[string]any, c *ExecutionContext) (*ToolApproval, error) {
	return &ToolApproval{Title: "catalog change", Fingerprint: ToolApprovalFingerprint(c, "catalog_manage", "apply", "candidate"), ViewportKey: "platform_control_review", Form: map[string]any{"before": "old", "after": "new"}}, nil
}
func TestExactToolReviewCannotAutoApprove(t *testing.T) {
	for _, level := range []string{AccessLevelFullAccess, AccessLevelAutoApprove, AccessLevelDefault} {
		t.Run(level, func(t *testing.T) {
			executor := &exactApprovalExecutor{}
			ctx := context.Background()
			session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: level}
			s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{tools: executor}, runControl: NewRunControl(ctx, "run"), execCtx: &ExecutionContext{Session: session, AccessLevel: level}}
			call := &preparedToolInvocation{toolID: "call", toolName: "catalog_manage", args: map[string]any{"action": "apply"}}
			handled, err := s.handleToolApprovalBeforeInvoke(call)
			if err != nil || !handled || s.hitlPendingCall != call || len(executor.invocations) > 0 {
				t.Fatalf("mandatory approval bypassed: %v %v", handled, err)
			}
			if s.hitlAwaitArgs["mode"] != "form" || s.hitlAwaitArgs["viewportKey"] != "platform_control_review" {
				t.Fatalf("expected HTML form: %#v", s.hitlAwaitArgs)
			}
			if s.hitlAwaitArgs["approvals"] != nil {
				t.Fatal("form leaked approval schema")
			}
			item := s.hitlAwaitArgs["forms"].([]any)[0].(map[string]any)
			if item["id"] != "call" || item["form"].(map[string]any)["after"] != "new" {
				t.Fatalf("missing frozen form: %#v", item)
			}
			for _, params := range [][]map[string]any{
				{{"id": "call", "decision": "approve_rule_run"}},
				{{"id": "another-call", "decision": "approve", "form": map[string]any{}}},
				{},
			} {
				ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: s.hitlAwaitingID, Params: encodedSubmitParams(t, params)})
				if ack.Accepted {
					t.Fatalf("invalid exact approval accepted: %#v", params)
				}
			}
		})
	}
}

func TestExactToolFormExecutesOnlyFrozenArguments(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			executor := &exactApprovalExecutor{}
			ctx := context.Background()
			session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: AccessLevelFullAccess}
			s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{tools: executor, interactions: toolinteraction.NewDefaultRegistry()}, runControl: NewRunControl(ctx, "run"), execCtx: &ExecutionContext{Session: session, AccessLevel: AccessLevelFullAccess, Budget: Budget{Tool: RetryPolicy{Timeout: 1}}}}
			call := &preparedToolInvocation{toolID: "call", toolName: "catalog_manage", args: map[string]any{"action": "apply", "content": "frozen"}}
			if _, err := s.handleToolApprovalBeforeInvoke(call); err != nil {
				t.Fatal(err)
			}
			ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: s.hitlAwaitingID, Params: encodedSubmitParams(t, []map[string]any{{"id": "call", "decision": decision, "reason": "adjust it", "form": map[string]any{"content": "tampered", "command": "do not execute"}}})})
			if !ack.Accepted {
				t.Fatalf("submit rejected: %#v", ack)
			}
			if err := s.awaitHITLSubmitAndExecute(); err != nil {
				t.Fatal(err)
			}
			if decision == "reject" {
				if len(executor.invocations) != 0 {
					t.Fatal("rejected operation executed")
				}
				return
			}
			if len(executor.invocations) != 1 {
				t.Fatalf("invocations: %#v", executor.invocations)
			}
			args := executor.invocations[0].args
			if args["content"] != "frozen" || args["command"] != nil {
				t.Fatalf("form rewrote invocation: %#v", args)
			}
			if len(s.execCtx.ToolApprovals) != 0 || len(s.hitlRuleWhitelist) != 0 {
				t.Fatal("approval survived invocation")
			}
		})
	}
}
