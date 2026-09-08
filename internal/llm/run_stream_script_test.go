package llm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// Protocol fixtures without a filesystem model simulate a tool backend, not
// Host Bash. Fixtures that declare roots exercise the real access policy.
func (r *recordingToolExecutor) ReviewBashAccess(_ context.Context, args map[string]any, ctx *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	if ctx == nil || !hasLocalFileRoots(ctx.Session) {
		return accesspolicy.BashPlan{Decision: accesspolicy.DecisionAllow}
	}
	var environment *accesspolicy.BashEnvironment
	if ctx.Session.AgentHasRuntimeSandbox {
		environment = &accesspolicy.BashEnvironment{Resolve: func(name, cwd string, env map[string]string) (string, error) {
			if strings.Contains(name, "/") {
				return name, nil
			}
			return "/usr/bin/" + name, nil
		}}
	}
	return accesspolicy.ReviewBashCommandInEnvironment(cfg, ctx.Session, mapStringArg(args, "command"), mapStringArg(args, "cwd"), ctx.StaticRuntimeEnv, environment, ctx)
}

func (stubToolExecutor) ReviewBashAccess(ctx context.Context, args map[string]any, execution *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	return (&recordingToolExecutor{}).ReviewBashAccess(ctx, args, execution, cfg)
}

func (*streamingOutputToolExecutor) ReviewBashAccess(ctx context.Context, args map[string]any, execution *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	return (&recordingToolExecutor{}).ReviewBashAccess(ctx, args, execution, cfg)
}

func TestScriptBatchWriteBarrierPreservesOrder(t *testing.T) {
	executor := &recordingToolExecutor{}
	stream := &llmRunStream{ctx: context.Background(), engine: &LLMAgentEngine{tools: executor}, execCtx: &ExecutionContext{}}
	write := &preparedToolInvocation{toolID: "write", toolName: "file_write", args: map[string]any{"file_path": "task.sh", "content": "echo ok"}}
	bash := &preparedToolInvocation{toolID: "run", toolName: "bash", args: map[string]any{"command": "sh task.sh"}}
	stream.queuedToolCalls = []*preparedToolInvocation{write, bash}
	if stream.prepareQueuedBashApprovalBatch() {
		t.Fatal("execution preapproved before write")
	}
	if err := stream.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	if stream.activeToolCall != write || stream.hitlPendingBatch != nil {
		t.Fatal("write must be activated first")
	}
	first := stream.serialExecutionContext(write)
	second := stream.serialExecutionContext(bash)
	if first.AuthoredScripts == nil || first.AuthoredScripts != second.AuthoredScripts {
		t.Fatal("serial tools do not share run proof")
	}
}

func TestDisplayedScriptApprovalCannotGrantChangedContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "task.sh")
	if err := os.WriteFile(p, []byte("echo before"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &ExecutionContext{Session: QuerySession{WorkspaceRoot: dir, RunID: "run", AgentKey: "ordinary"}}
	stream := &llmRunStream{ctx: context.Background(), engine: &LLMAgentEngine{tools: &recordingToolExecutor{}}, session: ctx.Session, execCtx: ctx}
	invocation := &preparedToolInvocation{toolID: "bash", toolName: "bash", args: map[string]any{"command": "sh task.sh"}}
	shown := stream.bashAccessApprovalRequest(invocation, stream.lookupBashAccessReview(invocation))
	if err := stream.emitApprovalRequestDeltas(shown); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("echo after"), 0600); err != nil {
		t.Fatal(err)
	}
	invocation.approvalDecision = "approve"
	request, ok := stream.approvalRequestForInvocation(invocation)
	if !ok || request.bashAccessReview.Fingerprint != shown.bashAccessReview.Fingerprint {
		t.Fatal("shown snapshot replaced with new requirements")
	}
	stream.grantDisplayedBashAccess("approve", *request.bashAccessReview)
	if accesspolicy.HasApproval(ctx, stream.rawBashAccessReview(invocation)) {
		t.Fatal("old exact approval authorized changed script")
	}
}

func TestBashApprovalDescriptionDoesNotAppendPolicyReasons(t *testing.T) {
	for _, command := range []string{"sh ./self.sh", "sh task.sh > /outside-review/output"} {
		for _, description := range []string{"重试：sh ./self.sh", ""} {
			t.Run(command+"/"+description, func(t *testing.T) {
				dir := t.TempDir()
				ctx := &ExecutionContext{Session: QuerySession{WorkspaceRoot: dir}}
				stream := &llmRunStream{ctx: context.Background(), engine: &LLMAgentEngine{tools: &recordingToolExecutor{}}, session: ctx.Session, execCtx: ctx}
				args := map[string]any{"command": command, "cwd": dir}
				if description != "" {
					args["description"] = description
				}
				invocation := &preparedToolInvocation{toolID: "bash", toolName: "bash", args: args}
				request, ok := stream.approvalRequestForInvocation(invocation)
				if !ok || request.bashAccessReview == nil || !request.bashAccessReview.RequiresApproval() {
					t.Fatal("missing access approval")
				}
				if !strings.Contains(request.bashAccessReview.Reason, "internally") {
					t.Fatalf("missing internal script requirement: %#v", request.bashAccessReview)
				}
				if strings.Contains(command, ">") {
					if request.bashSecurityReview == nil || request.bashSecurityReview.Reason == "" || !strings.Contains(request.bashAccessReview.Reason, "outside-review") {
						t.Fatalf("missing aggregate requirements: %#v", request)
					}
				}
				invocation.shownApproval = &request
				wantDescription := description
				if wantDescription == "" {
					wantDescription = command
				}
				want := map[string]any{
					"id":            "bash",
					"command":       command,
					"description":   wantDescription,
					"options":       buildApprovalOptions(),
					"allowFreeText": true,
					"ruleKey":       request.result.Rule.RuleKey,
				}
				if item := stream.buildApprovalAskItem(invocation); !reflect.DeepEqual(item, want) {
					t.Fatalf("approval item changed command/description or added policy notes: got %#v, want %#v", item, want)
				}
				if accesspolicy.HasApproval(ctx, *request.bashAccessReview) {
					t.Fatal("building the approval item must not authorize execution")
				}
			})
		}
	}
}
