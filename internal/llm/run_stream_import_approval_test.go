package llm

import (
	"context"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/toolinteraction"
	"agent-platform/internal/view"
)

type importApprovalExecutor struct {
	recordingToolExecutor
	reviews int
	policy  config.AccessPolicyConfig
}

func (x *importApprovalExecutor) PrepareToolApproval(_ context.Context, _ string, args map[string]any, e *ExecutionContext) (*ToolApproval, error) {
	x.reviews++
	path := args["args"].(map[string]any)["archivePath"].(string)
	access, err := filetools.BuildAccessPlanFromPolicy(x.policy, e.Session, filetools.ReadAccess, path)
	if err != nil {
		return nil, err
	}
	if !filetools.HasReadApproval(e, access) {
		panic("package reviewed before source access approval")
	}
	return &ToolApproval{Fingerprint: ToolApprovalFingerprint(e, "catalog_manage", "import", "archive"), View: view.Builtin("platform_control_review"), Form: map[string]any{"action": "import", "after": "package"}}, nil
}

func TestInputReadApprovalPrecedesPackageReviewAndCannotApproveImport(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			policy := config.DefaultAccessPolicyConfig()
			level := policy.Levels["default"]
			level.ReadRoots = []string{"@workspace"}
			policy.Levels["default"] = level
			executor := &importApprovalExecutor{policy: policy}
			ctx := context.Background()
			session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: AccessLevelDefault, WorkspaceRoot: t.TempDir()}
			stream := &llmRunStream{ctx: ctx, session: session, runControl: NewRunControl(ctx, "run"), engine: &LLMAgentEngine{cfg: config.Config{AccessPolicy: policy}, tools: executor, interactions: toolinteraction.NewDefaultRegistry()}, execCtx: &ExecutionContext{Session: session, Budget: Budget{Tool: RetryPolicy{Timeout: 1}}}}
			call := &preparedToolInvocation{toolID: "call", toolName: "catalog_manage", args: map[string]any{"action": "import", "args": map[string]any{"resourceType": "connector", "archivePath": filepath.Join(t.TempDir(), "source.zip")}}}
			request, ok := stream.approvalRequestForInvocation(call)
			if !ok || request.kind != approvalKindFileAccess || executor.reviews != 0 {
				t.Fatal("business review preceded input access")
			}
			if err := stream.emitApprovalRequestDeltas(request); err != nil {
				t.Fatal(err)
			}
			readAwaitingID := stream.hitlAwaitingID
			readAck := stream.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: readAwaitingID, Params: encodedSubmitParams(t, []map[string]any{{"id": "call", "decision": "approve"}})})
			if !readAck.Accepted {
				t.Fatalf("read approval rejected: %#v", readAck)
			}
			if err := stream.awaitHITLSubmitAndExecute(); err != nil {
				t.Fatal(err)
			}
			if executor.reviews != 1 || len(executor.invocations) != 0 || stream.hitlAwaitArgs["mode"] != "form" {
				t.Fatalf("read approval executed import: reviews=%d calls=%v ask=%v messages=%+v pending=%+v", executor.reviews, executor.invocations, stream.hitlAwaitArgs, stream.messages, stream.pending)
			}
			if stream.hitlAwaitingID == readAwaitingID {
				t.Fatal("input and package approvals reused an awaiting ID")
			}
			stale := stream.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: readAwaitingID, Param: api.SubmitParam{"decision": "approve"}})
			if stale.Accepted {
				t.Fatal("old read submit approved the import")
			}
			ack := stream.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: stream.hitlAwaitingID, Param: api.SubmitParam{"decision": decision, "data": map[string]any{"after": "untrusted reply"}}})
			if !ack.Accepted {
				t.Fatalf("import approval rejected: %#v", ack)
			}
			if err := stream.awaitHITLSubmitAndExecute(); err != nil {
				t.Fatal(err)
			}
			want := 0
			if decision == "approve" {
				want = 1
			}
			if len(executor.invocations) != want {
				t.Fatalf("import calls=%v messages=%+v pending=%+v", executor.invocations, stream.messages, stream.pending)
			}
		})
	}
}
