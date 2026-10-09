package llm

import (
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
	"agent-platform/internal/toolinteraction"
	"agent-platform/internal/view"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type exactApprovalExecutor struct {
	recordingToolExecutor
	plainApproval    bool
	allowAutoApprove bool
}

func (e *exactApprovalExecutor) PrepareToolApproval(_ context.Context, _ string, _ map[string]any, c *ExecutionContext) (*ToolApproval, error) {
	key := "platform_control_review"
	if e.plainApproval {
		key = ""
	}
	var ref *view.Reference
	if key != "" {
		ref = view.Builtin(key)
	}
	return &ToolApproval{AllowAutoApprove: e.allowAutoApprove, Title: "catalog change", Fingerprint: ToolApprovalFingerprint(c, "catalog_manage", "apply", "candidate"), View: ref, Form: map[string]any{"before": "old", "after": "new"}}, nil
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
			if s.hitlAwaitArgs["mode"] != "form" || s.hitlAwaitArgs["view"].(map[string]any)["key"] != "platform_control_review" {
				t.Fatalf("expected HTML form: %#v", s.hitlAwaitArgs)
			}
			if s.hitlAwaitArgs["approvals"] != nil {
				t.Fatal("form leaked approval schema")
			}
			item := s.hitlAwaitArgs["form"].(map[string]any)
			if item["id"] != nil || item["data"].(map[string]any)["after"] != "new" {
				t.Fatalf("missing frozen form: %#v", item)
			}
			for _, request := range []api.SubmitRequest{
				{Param: api.SubmitParam{"decision": "approve_rule_run"}},
				{Param: api.SubmitParam{"decision": "dismiss"}},
				{Params: encodedSubmitParams(t, []map[string]any{{"id": "call", "decision": "approve"}})},
				{Params: api.SubmitParams{}},
			} {
				request.RunID, request.AwaitingID = "run", s.hitlAwaitingID
				ack := s.runControl.ResolveSubmit(request)
				if ack.Accepted {
					t.Fatalf("invalid exact approval accepted: %#v", request)
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
			ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: "run", AwaitingID: s.hitlAwaitingID, Param: api.SubmitParam{"decision": decision, "reason": "adjust it", "data": map[string]any{"content": "tampered", "command": "do not execute"}}})
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

func TestReviewInputDiagnosticReachesModel(t *testing.T) {
	s := &llmRunStream{ctx: context.Background()}
	call := &preparedToolInvocation{toolID: "bad", toolName: "catalog_manage", toolApprovalChecked: true, toolApprovalErr: toolinput.New("args.content", "JSON string", true, true, `Use content:"text".`)}
	handled, err := s.handleToolApprovalBeforeInvoke(call)
	if err != nil || !handled || len(s.messages) != 1 || s.hitlPendingCall != nil {
		t.Fatalf("handled=%v error=%v messages=%v", handled, err, s.messages)
	}
	b, _ := json.Marshal(s.messages[0].Content)
	for _, fragment := range []string{"expected", "actual", "recovery", "not_started", "args.content"} {
		if !strings.Contains(string(b), fragment) {
			t.Fatal(string(b))
		}
	}
}

func TestExactToolApprovalWithoutFormStillRequiresApproval(t *testing.T) {
	for _, level := range []string{AccessLevelFullAccess, AccessLevelAutoApprove, AccessLevelDefault} {
		executor := &exactApprovalExecutor{plainApproval: true}
		ctx := context.Background()
		session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: level}
		s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{tools: executor}, runControl: NewRunControl(ctx, "run"), execCtx: &ExecutionContext{Session: session, AccessLevel: level}}
		call := &preparedToolInvocation{toolID: "call", toolName: "catalog_manage", args: map[string]any{"action": "apply"}}
		handled, err := s.handleToolApprovalBeforeInvoke(call)
		if err != nil || !handled || s.hitlAwaitArgs["mode"] != "approval" || len(executor.invocations) != 0 {
			t.Fatalf("missing form bypassed approval at %s: %v %v %#v", level, handled, err, s.hitlAwaitArgs)
		}
	}
}

func TestToolReviewOptInAutoApproval(t *testing.T) {
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		t.Run(level, func(t *testing.T) {
			executor := &exactApprovalExecutor{allowAutoApprove: true}
			ctx := context.Background()
			// Session stays default: policy must use the current execution access level.
			session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: AccessLevelDefault}
			s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{tools: executor}, runControl: NewRunControl(ctx, "run"), execCtx: &ExecutionContext{Session: session, AccessLevel: level}}
			call := &preparedToolInvocation{toolID: "call", toolName: "catalog_manage", args: map[string]any{"action": "apply"}}
			handled, err := s.handleToolApprovalBeforeInvoke(call)
			if err != nil || !handled {
				t.Fatalf("handled=%v error=%v", handled, err)
			}
			if level == AccessLevelDefault {
				if s.hitlPendingCall != call || len(executor.invocations) != 0 {
					t.Fatal("default skipped review")
				}
				return
			}
			if s.hitlPendingCall != nil || len(executor.invocations) != 1 || s.approvalAuto != 1 {
				t.Fatalf("auto approval: pending=%v calls=%d count=%d", s.hitlPendingCall, len(executor.invocations), s.approvalAuto)
			}
			if call.hitlDecision == nil || call.hitlDecision.Decision != "auto_approved" {
				t.Fatal("missing audit decision")
			}
			if len(s.execCtx.ToolApprovals) != 0 || len(s.hitlRuleWhitelist) != 0 {
				t.Fatal("one-shot approval leaked")
			}
		})
	}
}

// Raising the Run's access level while an exact tool review is displayed must
// never answer it; only a matching human submit can.
func TestExactToolReviewIgnoresAccessLevelChange(t *testing.T) {
	executor := &exactApprovalExecutor{}
	ctx := context.Background()
	session := QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", AccessLevel: AccessLevelAutoApprove}
	s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{tools: executor}, runControl: NewRunControl(ctx, "run"), execCtx: &ExecutionContext{Session: session, AccessLevel: AccessLevelAutoApprove}}
	call := &preparedToolInvocation{toolID: "call", toolName: "chat_start", args: map[string]any{"accessLevel": AccessLevelFullAccess}}
	if handled, err := s.handleToolApprovalBeforeInvoke(call); err != nil || !handled || s.hitlMatch == nil {
		t.Fatalf("review not shown: %v %v", handled, err)
	}
	s.runControl.UpdateAccessLevel(AccessLevelFullAccess)
	s.execCtx.AccessLevel = AccessLevelFullAccess
	resolved, err := s.tryResolvePendingAccessLevelApproval(call, *s.hitlMatch, s.hitlAwaitingID)
	if err != nil || resolved || len(executor.invocations) != 0 || call.approvalDecision != "" {
		t.Fatalf("access level change answered an exact review: resolved=%v err=%v", resolved, err)
	}
}
