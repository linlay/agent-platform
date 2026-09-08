package llm

import (
	"context"
	"errors"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	"agent-platform/internal/view"
)

func TestViewFormDoesNotJoinBuiltinApprovalOrAutoApprove(t *testing.T) {
	rule := hitl.FlatRule{Mode: "form", View: &view.Reference{ConnectorID: "forms", Key: "edit"}, Level: 1}
	match := hitl.InterceptResult{Intercepted: true, Rule: rule}
	s := &llmRunStream{session: contracts.QuerySession{RunID: "run"}, execCtx: &contracts.ExecutionContext{AutoApproveLevels: map[int]bool{1: true}}}
	if rule.IsBuiltinApproval() || approvalRequestCanJoinBatch(approvalRequest{result: match}) || s.shouldAutoApproveHITL(match) {
		t.Fatal("connector form treated as builtin approval")
	}
	args := s.buildHITLArgs(&preparedToolInvocation{args: map[string]any{"command": `command --payload '{"name":"original"}'`}}, match)
	if args["mode"] != "form" || args["view"] == nil || args["viewportType"] != nil {
		t.Fatalf("args: %#v", args)
	}
	s.session.ResolveView = func(_ context.Context, ref view.Reference, usage string) (view.Reference, error) {
		if usage != "form" {
			t.Error(usage)
		}
		ref.Renderer = "qlc"
		ref.Version = "1.0.0"
		ref.Hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		return ref, nil
	}
	delta := s.buildHITLAwaitDelta("wait", args, 0)
	if delta.View == nil || delta.View.Renderer != "qlc" || delta.Mode != "form" {
		t.Fatalf("delta: %#v", delta)
	}
	s.session.ResolveView = func(context.Context, view.Reference, string) (view.Reference, error) {
		return view.Reference{}, errors.New("private endpoint and credential")
	}
	delta = s.buildHITLAwaitDelta("wait", args, 0)
	if delta.Mode != "form" || delta.ViewError != "view_unavailable" || delta.View == nil {
		t.Fatalf("failed view lost waiting: %#v", delta)
	}
}
