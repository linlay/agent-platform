package llm

import (
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"context"
	"testing"
)

type exactApprovalExecutor struct{ recordingToolExecutor }

func (e *exactApprovalExecutor) PrepareToolApproval(_ context.Context, _ string, _ map[string]any, c *ExecutionContext) (*ToolApproval, error) {
	return &ToolApproval{Title: "catalog change", Fingerprint: ToolApprovalFingerprint(c, "catalog_manage", "apply", "candidate"), Details: map[string]any{"before": "old", "after": "new"}}, nil
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
			item := s.buildApprovalAskItem(call)
			if item["review"] == nil || item["fingerprint"] == nil {
				t.Fatal("missing concrete review")
			}
			options := item["options"].([]any)
			if len(options) != 1 || options[0].(map[string]any)["decision"] != "approve" {
				t.Fatal("review can grant broader permission")
			}
			ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: s.hitlAwaitingID, Params: encodedSubmitParams(t, []map[string]any{{"id": "call", "decision": "approve_rule_run"}})})
			if ack.Accepted {
				t.Fatal("broader approval accepted for exact review")
			}
		})
	}
}
