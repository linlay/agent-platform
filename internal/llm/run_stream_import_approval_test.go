package llm

import (
	"context"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/toolinput"
	"agent-platform/internal/toolinteraction"
	"agent-platform/internal/view"
)

type importApprovalExecutor struct {
	recordingToolExecutor
	reviews  int
	policy   config.AccessPolicyConfig
	inputErr error
}

func (x *importApprovalExecutor) ToolApprovalInputPath(_ string, args map[string]any, _ *ExecutionContext) (string, error) {
	if x.inputErr != nil {
		return "", x.inputErr
	}
	path, _ := args["inputPath"].(string)
	return path, nil
}

func TestInvalidReviewInputIsRejectedBeforeFileApproval(t *testing.T) {
	executor := &importApprovalExecutor{inputErr: toolinput.New("args.inputPath", "a nonempty file path", nil, false, "Choose one input file.")}
	s := &llmRunStream{ctx: t.Context(), engine: &LLMAgentEngine{tools: executor}, execCtx: &ExecutionContext{}}
	call := &preparedToolInvocation{toolID: "call", toolName: "package_apply", args: map[string]any{}}
	handled, err := s.handleToolApprovalBeforeInvoke(call)
	if err != nil || !handled || executor.reviews != 0 || len(executor.invocations) != 0 || s.hitlPendingCall != nil {
		t.Fatalf("invalid input reached review: handled=%v err=%v reviews=%d", handled, err, executor.reviews)
	}
	if len(s.pending) != 1 || s.pending[0].(DeltaToolResult).Result.Error != "tool_review_failed" {
		t.Fatalf("input diagnostic: %#v", s.pending)
	}
}

type ordinaryFileApprovalExecutor struct {
	recordingToolExecutor
	reviews int
}

func (x *ordinaryFileApprovalExecutor) PrepareToolApproval(context.Context, string, map[string]any, *ExecutionContext) (*ToolApproval, error) {
	x.reviews++
	return nil, nil
}

func TestApprovedWritePathPreservesExistingContentGrantWithoutReviewReentry(t *testing.T) {
	for _, name := range []string{"file_write", "file_edit"} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Config{FileTools: config.FileToolsConfig{RequireWriteApproval: true, MaxWriteBytes: 1024}}
			executor := &ordinaryFileApprovalExecutor{}
			session := QuerySession{WorkspaceRoot: t.TempDir()}
			s := &llmRunStream{ctx: t.Context(), session: session, engine: &LLMAgentEngine{cfg: cfg, tools: executor}, execCtx: &ExecutionContext{Session: session}}
			args := map[string]any{"filePath": filepath.Join(t.TempDir(), "owner.md"), "content": "new", "oldString": "old", "newString": "new"}
			call := &preparedToolInvocation{toolID: "call", toolName: name, args: args}
			write, err := s.buildFileWritePlan(call)
			if err != nil || !s.fileWritePlanNeedsApproval(write) {
				t.Fatalf("write fixture: %+v %v", write, err)
			}
			call.fileWritePlan = &write
			filetools.RegisterExactWriteApproval(s.execCtx, write.Fingerprint)
			access := s.lookupFileAccessPlan(call)
			if access == nil {
				t.Fatal("missing path plan")
			}
			filetools.RegisterExactAccessApproval(s.execCtx, access.Fingerprint)
			if err := s.executeAfterFileAccessApproval(call); err != nil {
				t.Fatal(err)
			}
			if executor.reviews != 0 || len(executor.invocations) != 1 || s.hitlPendingCall != nil || call.toolApprovalChecked {
				t.Fatalf("ordinary write reentered review: reviews=%d calls=%v pending=%v", executor.reviews, executor.invocations, s.hitlPendingCall)
			}
		})
	}
}

func (x *importApprovalExecutor) PrepareToolApproval(_ context.Context, _ string, args map[string]any, e *ExecutionContext) (*ToolApproval, error) {
	x.reviews++
	path := args["inputPath"].(string)
	access, err := filetools.BuildAccessPlanFromPolicy(x.policy, e.Session, filetools.ReadAccess, path)
	if err != nil {
		return nil, err
	}
	if !filetools.HasReadApproval(e, access) {
		panic("package reviewed before source access approval")
	}
	return &ToolApproval{Fingerprint: ToolApprovalFingerprint(e, "package_apply", "apply", "archive"), View: view.Builtin("platform_control_review"), Form: map[string]any{"action": "apply", "after": "package"}}, nil
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
			call := &preparedToolInvocation{toolID: "call", toolName: "package_apply", args: map[string]any{"inputPath": filepath.Join(t.TempDir(), "source.zip")}}
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

func TestRepeatedToolApprovalDoesNotResolveSiblingWithSuffixID(t *testing.T) {
	ctx := t.Context()
	s := &llmRunStream{ctx: ctx, session: QuerySession{RunID: "run"}, runControl: NewRunControl(ctx, "run")}
	call := &preparedToolInvocation{toolID: "call", toolName: "package_apply"}
	sibling := &preparedToolInvocation{toolID: "call_2", toolName: "package_apply"}
	seen := map[string]bool{}
	for _, invocation := range []*preparedToolInvocation{call, call, sibling} {
		request := approvalRequest{kind: approvalKindTool, invocation: invocation, toolApproval: &ToolApproval{View: view.Builtin("platform_control_review")}}
		if err := s.emitApprovalRequestDeltas(request); err != nil {
			t.Fatal(err)
		}
		id := s.hitlAwaitingID
		if seen[id] {
			t.Fatalf("approval of %q reused resolved awaiting ID %q", invocation.toolID, id)
		}
		seen[id] = true
		ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: id, Param: api.SubmitParam{"decision": "approve", "data": map[string]any{}}})
		if !ack.Accepted {
			t.Fatalf("approval of %q rejected: %+v", invocation.toolID, ack)
		}
	}
}
