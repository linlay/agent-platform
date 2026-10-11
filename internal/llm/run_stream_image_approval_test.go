package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	runtimetools "agent-platform/internal/tools"
)

// The production parser/router participates; the final model request is replaced
// with an access-consuming backend so the test cannot upload any real image.
type imageApprovalExecutor struct {
	*runtimetools.ToolRouter
	t     *testing.T
	calls int
}

func (e *imageApprovalExecutor) Invoke(_ context.Context, name string, args map[string]any, ctx *ExecutionContext) (ToolExecutionResult, error) {
	plans, err := e.ReviewImageAccess(name, args, ctx)
	if err != nil {
		return ToolExecutionResult{Error: err.Error(), ExitCode: -1}, nil
	}
	for _, plan := range plans {
		if plan.AllowedByWhitelist || plan.AutoApproved {
			continue
		}
		sibling := *ctx
		sibling.CurrentToolID = "unapproved-sibling"
		if len(ctx.FileReadRuleApprovals) == 0 && filetools.HasReadApproval(&sibling, plan) {
			e.t.Fatal("image grant leaked to sibling")
		}
		if !filetools.ConsumeReadApproval(ctx, plan) {
			e.t.Fatal("approved source lacks read grant")
		}
	}
	e.calls++
	return ToolExecutionResult{Output: "ok"}, nil
}

func newImageApprovalStream(t *testing.T, tool string, args map[string]any) (*llmRunStream, *preparedToolInvocation, *imageApprovalExecutor) {
	t.Helper()
	cfg := config.Config{}
	runtime, err := runtimetools.NewRuntimeToolExecutor(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := runtimetools.NewToolRouter(runtime, nil, nil, stubInteractionSubmitter{})
	if err != nil {
		t.Fatal(err)
	}
	executor := &imageApprovalExecutor{ToolRouter: router, t: t}
	session := QuerySession{RunID: "images-run", WorkspaceRoot: t.TempDir(), AccessLevel: AccessLevelDefault}
	invocation := &preparedToolInvocation{toolID: "images-tool", toolName: tool, args: args}
	stream := &llmRunStream{ctx: context.Background(), session: session, engine: &LLMAgentEngine{cfg: cfg, tools: executor}, execCtx: &ExecutionContext{Session: session}, activeToolCall: invocation}
	return stream, invocation, executor
}

// The router requires an interaction handler for its unrelated question tool.
type stubInteractionSubmitter struct{}

func (stubInteractionSubmitter) Handles(name string) bool {
	return name == "ask_user_question" || name == "ask_user_form"
}
func (stubInteractionSubmitter) Await(context.Context, *ExecutionContext, map[string]any) (ToolExecutionResult, error) {
	return ToolExecutionResult{}, nil
}

func TestImageAccessCombinedApprovalAndMask(t *testing.T) {
	for _, tool := range []string{"vision_recognize", "image_generate"} {
		for _, decision := range []string{"approve", "approve_rule_run", "reject"} {
			t.Run(tool+"/"+decision, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "outside.png")
				source := map[string]any{"filePath": path}
				if tool == "image_generate" {
					source = map[string]any{"sourceType": "filePath", "value": path}
				}
				args := map[string]any{"images": []any{source, source}}
				count := 2
				if tool == "image_generate" {
					args["mask"] = map[string]any{"sourceType": "filePath", "value": path, "mode": "alpha"}
					count++
				}
				s, invocation, executor := newImageApprovalStream(t, tool, args)
				if err := s.invokeActiveToolCall(); err != nil {
					t.Fatal(err)
				}
				if executor.calls != 0 || invocation.shownApproval == nil {
					t.Fatal("image ran without approval")
				}
				item := s.buildApprovalAskItem(invocation)
				if len(item["requirements"].([]any)) != count {
					t.Fatalf("missing image or mask: %#v", item)
				}
				request := *invocation.shownApproval
				invocation.approvalDecision = decision
				if err := s.executeApprovedApprovalRequest(request); err != nil {
					t.Fatal(err)
				}
				want := 1
				if decision == "reject" {
					want = 0
				}
				if executor.calls != want {
					t.Fatalf("calls=%d want=%d", executor.calls, want)
				}
				if len(s.execCtx.FileReadApprovals) != 0 {
					t.Fatal("leftover one-shot image grants")
				}
			})
		}
	}
}

func TestImageAccessSymlinkChangedWhileAwaiting(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link.png")
	first := filepath.Join(t.TempDir(), "first.png")
	second := filepath.Join(t.TempDir(), "second.png")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, link); err != nil {
		t.Skip(err)
	}
	s, invocation, executor := newImageApprovalStream(t, "vision_recognize", map[string]any{"images": []any{map[string]any{"filePath": link}}})
	if err := s.invokeActiveToolCall(); err != nil {
		t.Fatal(err)
	}
	request := *invocation.shownApproval
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	invocation.approvalDecision = "approve"
	if err := s.executeApprovedApprovalRequest(request); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 0 || len(s.execCtx.FileReadApprovals) != 0 {
		t.Fatal("changed image target was approved")
	}
	last := s.pending[len(s.pending)-1].(DeltaToolResult)
	if last.Result.Error != "approval_requirements_changed" {
		t.Fatalf("wrong result: %+v", last.Result)
	}
}

func TestImageAccessAutoApprovalAndProtectedRoot(t *testing.T) {
	for _, protected := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "image.png")
		s, invocation, executor := newImageApprovalStream(t, "vision_recognize", map[string]any{"images": []any{map[string]any{"filePath": path}}})
		s.session.AccessLevel = AccessLevelAutoApprove
		if protected {
			s.session.ProtectedPaths = []string{filepath.Dir(path)}
		}
		s.execCtx.Session = s.session
		if err := s.invokeActiveToolCall(); err != nil {
			t.Fatal(err)
		}
		if invocation.shownApproval != nil {
			t.Fatal("auto or hard block unexpectedly asked for approval")
		}
		if protected {
			if executor.calls != 0 {
				t.Fatal("protected image source was read")
			}
		} else if executor.calls != 1 || invocation.hitlDecision == nil || invocation.hitlDecision.Decision != "auto_approved" {
			t.Fatal("missing automatic image approval audit")
		}
	}
}
