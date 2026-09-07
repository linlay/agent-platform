package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/api"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

type approvedBashStart struct {
	id               string
	access, security map[string]int
}

type approvedBashExecutor struct {
	cfg     config.Config
	started chan approvedBashStart
	release map[string]chan struct{}
}

func (e *approvedBashExecutor) Definitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{bashToolDefinition()}
}

func (e *approvedBashExecutor) ReviewBashAccess(_ context.Context, args map[string]any, ctx *ExecutionContext, cfg config.AccessPolicyConfig) accesspolicy.BashPlan {
	return accesspolicy.ReviewBashCommand(cfg, ctx.Session, mapStringArg(args, "command"), mapStringArg(args, "cwd"), nil, ctx)
}

func (e *approvedBashExecutor) Invoke(ctx context.Context, _ string, args map[string]any, execution *ExecutionContext) (ToolExecutionResult, error) {
	start := approvedBashStart{execution.CurrentToolID, cloneIntMap(execution.AccessPolicyApprovals), cloneIntMap(execution.BashSecurityApprovals)}
	plan := e.ReviewBashAccess(ctx, args, execution, e.cfg.AccessPolicy)
	if plan.Blocked() || (plan.RequiresApproval() && !accesspolicy.ConsumeApproval(execution, plan)) {
		return ToolExecutionResult{Error: "bash_access_approval_required", ExitCode: -1}, nil
	}
	security := bashsec.ReviewBashSecurity(mapStringArg(args, "command"))
	if security.Decision == bashsec.ReviewRequiresApproval {
		if execution.BashSecurityApprovals[security.Fingerprint] != 1 {
			return ToolExecutionResult{Error: "bash_security_approval_required", ExitCode: -1}, nil
		}
		delete(execution.BashSecurityApprovals, security.Fingerprint)
	}
	select {
	case e.started <- start:
	case <-ctx.Done():
		return ToolExecutionResult{}, ctx.Err()
	}
	if execution.ToolOutputSink != nil {
		if err := execution.ToolOutputSink.EmitToolOutput(ctx, ToolOutput{Stream: ToolOutputStdout, Delta: start.id + " started\n"}); err != nil {
			return ToolExecutionResult{}, err
		}
	}
	select {
	case <-e.release[start.id]:
	case <-ctx.Done():
		return ToolExecutionResult{}, ctx.Err()
	}
	if execution.ToolOutputSink != nil {
		if err := execution.ToolOutputSink.EmitToolOutput(ctx, ToolOutput{Stream: ToolOutputStderr, Delta: start.id + " done\n"}); err != nil {
			return ToolExecutionResult{}, err
		}
	}
	return ToolExecutionResult{Output: start.id, ExitCode: 0}, nil
}

func newApprovedBashStream(t *testing.T, count int) (*llmRunStream, *approvedBashExecutor, string) {
	t.Helper()
	root := t.TempDir()
	mock := filepath.Join(root, "mock")
	if err := os.WriteFile(mock, []byte("mock executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	session := QuerySession{RunID: "approved_batch", ChatID: "chat", WorkspaceRoot: root, AccessLevel: AccessLevelDefault}
	e := &approvedBashExecutor{started: make(chan approvedBashStart, 10), release: map[string]chan struct{}{}}
	s := &llmRunStream{ctx: ctx, session: session, engine: &LLMAgentEngine{cfg: e.cfg, tools: e}, runControl: NewRunControl(ctx, session.RunID),
		execCtx: &ExecutionContext{Session: session, StartedAt: time.Now(), Budget: Budget{Tool: RetryPolicy{MaxCalls: 100, Timeout: 60}}}}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("tool_%d", i+1)
		e.release[id] = make(chan struct{})
		s.queuedToolCalls = append(s.queuedToolCalls, &preparedToolInvocation{toolID: id, toolName: "bash", args: map[string]any{"command": mock + " work", "cwd": root}})
	}
	return s, e, mock
}

func submitApprovedBashBatch(t *testing.T, s *llmRunStream, decisions ...string) {
	t.Helper()
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	if s.hitlPendingBatch == nil || len(s.hitlPendingBatch.invocations) != len(decisions) {
		t.Fatalf("unexpected approval batch: %#v", s.hitlPendingBatch)
	}
	batch := s.hitlPendingBatch
	items := make([]map[string]any, len(decisions))
	for i, decision := range decisions {
		items[i] = map[string]any{"id": batch.invocations[i].toolID, "decision": decision}
	}
	ack := s.runControl.ResolveSubmit(api.SubmitRequest{RunID: s.session.RunID, AwaitingID: batch.awaitingID, Params: encodedSubmitParams(t, items)})
	if !ack.Accepted {
		t.Fatalf("submit rejected: %#v", ack)
	}
	if err := s.awaitHITLApprovalBatchAndContinue(); err != nil {
		t.Fatal(err)
	}
	s.pending = nil
}

func awaitApprovedBashStarts(t *testing.T, e *approvedBashExecutor, count int) []approvedBashStart {
	t.Helper()
	starts := make([]approvedBashStart, 0, count)
	for len(starts) < count {
		select {
		case start := <-e.started:
			starts = append(starts, start)
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d tools started before release; calls did not run concurrently", len(starts), count)
		}
	}
	return starts
}

func drainApprovedBashBatch(t *testing.T, s *llmRunStream) {
	t.Helper()
	for s.activeToolBatch != nil {
		if err := s.consumeActiveToolBatch(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostBashApprovalConcurrent(t *testing.T) {
	for _, mode := range []string{"approve", "approve_rule_run", "auto_approve", "security_and_access", "builtin_hitl"} {
		t.Run(mode, func(t *testing.T) {
			s, e, _ := newApprovedBashStream(t, 2)
			if mode == "security_and_access" {
				for _, call := range s.queuedToolCalls {
					call.args["command"] = mapStringArg(call.args, "command") + " > " + filepath.Join(s.session.WorkspaceRoot, call.toolID+".txt")
				}
			}
			if mode == "builtin_hitl" {
				command := mapStringArg(s.queuedToolCalls[0].args, "command")
				// Keep this test focused on the independent builtin HITL gate.
				s.execCtx.AccessLevel = AccessLevelFullAccess
				s.session.AccessLevel = AccessLevelFullAccess
				s.runControl.SetInitialAccessLevel(AccessLevelFullAccess)
				s.checker = commandResultChecker{results: map[string]hitl.InterceptResult{command: {Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "mock-login", ViewportType: "builtin", Level: 1}}}}
			}
			decision := mode
			if mode == "security_and_access" || mode == "builtin_hitl" {
				decision = "approve"
			}
			if mode == "auto_approve" {
				s.execCtx.AccessLevel = AccessLevelAutoApprove
				s.session.AccessLevel = AccessLevelAutoApprove
				s.runControl.SetInitialAccessLevel(AccessLevelAutoApprove)
			} else {
				submitApprovedBashBatch(t, s, decision, decision)
				if len(e.started) != 0 {
					t.Fatal("tool started before all approvals were resolved")
				}
			}
			var audits []chat.StepApproval
			s.onApprovalSummary = func(a chat.StepApproval) { audits = append(audits, a) }
			if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
				t.Fatal(err)
			}
			if s.activeToolBatch == nil {
				t.Fatalf("approved calls were serialized: active=%#v awaiting=%#v", s.activeToolCall, s.hitlPendingBatch)
			}
			starts := awaitApprovedBashStarts(t, e, 2)
			for _, start := range starts {
				if mode == "approve" || mode == "security_and_access" {
					if len(start.access) != 1 {
						t.Fatalf("exact approval not isolated: %#v", start)
					}
				}
				if mode == "security_and_access" && len(start.security) != 1 {
					t.Fatalf("missing security grant: %#v", start)
				}
			}
			if len(s.execCtx.AccessPolicyApprovals)+len(s.execCtx.BashSecurityApprovals) != 0 {
				t.Fatal("one-shot approvals leaked into run context")
			}
			close(e.release["tool_2"])
			for {
				if err := s.consumeActiveToolBatch(); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, delta := range s.pending {
					if r, ok := delta.(DeltaToolResult); ok {
						if r.ToolID != "tool_2" {
							t.Fatal("blocked first tool returned early")
						}
						found = true
					}
				}
				if found {
					break
				}
			}
			close(e.release["tool_1"])
			drainApprovedBashBatch(t, s)
			outputs := map[string]int{}
			results := map[string]int{}
			for _, delta := range s.pending {
				switch v := delta.(type) {
				case DeltaToolOutput:
					if results[v.ToolID] != 0 || v.ChunkIndex != outputs[v.ToolID] || !strings.HasPrefix(v.Delta, v.ToolID) {
						t.Fatalf("crossed output identity/order: %#v", v)
					}
					outputs[v.ToolID]++
				case DeltaToolResult:
					results[v.ToolID]++
					if v.Result.Error != "" {
						t.Fatalf("approved execution failed: %#v", v)
					}
				case DeltaAwaitAsk:
					t.Fatal("duplicate approval after submit")
				}
			}
			for _, id := range []string{"tool_1", "tool_2"} {
				if results[id] != 1 || outputs[id] != 2 {
					t.Fatalf("missing/duplicate events: %v %v", results, outputs)
				}
			}
			if len(s.messages) < 2 || s.messages[0].ToolCallID != "tool_1" || s.messages[1].ToolCallID != "tool_2" {
				t.Fatal("model result order changed")
			}
			if len(audits) != 1 || len(audits[0].Decisions) != 2 {
				t.Fatalf("approval summary duplicated/lost: %#v", audits)
			}
		})
	}
}

func TestHostBashApprovalConcurrentRuleReuseAndExactIsolation(t *testing.T) {
	for _, decision := range []string{"approve", "approve_rule_run"} {
		t.Run(decision, func(t *testing.T) {
			s, e, mock := newApprovedBashStream(t, 2)
			submitApprovedBashBatch(t, s, decision, decision)
			if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
				t.Fatal(err)
			}
			awaitApprovedBashStarts(t, e, 2)
			close(e.release["tool_1"])
			close(e.release["tool_2"])
			drainApprovedBashBatch(t, s)
			for _, id := range []string{"tool_3", "tool_4"} {
				e.release[id] = make(chan struct{})
				s.queuedToolCalls = append(s.queuedToolCalls, &preparedToolInvocation{toolID: id, toolName: "bash", args: map[string]any{"command": mock + " work", "cwd": s.session.WorkspaceRoot}})
			}
			s.pending = nil
			if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
				t.Fatal(err)
			}
			if decision == "approve" {
				if s.hitlPendingBatch == nil || len(e.started) != 0 {
					t.Fatal("exact grants authorized later calls")
				}
			} else {
				if s.hitlPendingBatch != nil || s.activeToolBatch == nil {
					t.Fatal("run-rule reuse did not restore concurrency")
				}
				awaitApprovedBashStarts(t, e, 2)
				close(e.release["tool_3"])
				close(e.release["tool_4"])
				drainApprovedBashBatch(t, s)
			}
		})
	}
}

func TestHostBashApprovalConcurrentRejectWins(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 3)
	submitApprovedBashBatch(t, s, "approve_rule_run", "reject", "approve")
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	starts := awaitApprovedBashStarts(t, e, 2)
	for _, start := range starts {
		if start.id == "tool_2" {
			t.Fatal("rejected call used sibling rule grant")
		}
		close(e.release[start.id])
	}
	drainApprovedBashBatch(t, s)
	rejected := 0
	for _, delta := range s.pending {
		if r, ok := delta.(DeltaToolResult); ok && r.ToolID == "tool_2" {
			rejected++
			if r.Result.Error != "user_rejected" {
				t.Fatalf("unexpected rejection: %#v", r)
			}
		}
	}
	if rejected != 1 || len(e.started) != 0 {
		t.Fatal("rejected command was executed or result duplicated")
	}
}

func TestHostBashApprovalConcurrentChangedRequirements(t *testing.T) {
	s, e, mock := newApprovedBashStream(t, 2)
	script := filepath.Join(s.session.WorkspaceRoot, "task.sh")
	if err := os.WriteFile(script, []byte("echo before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, call := range s.queuedToolCalls {
		call.args["command"] = "sh " + script
	}
	submitApprovedBashBatch(t, s, "approve", "approve")
	if err := os.WriteFile(script, []byte("echo after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	if len(e.started) != 0 {
		t.Fatal("changed script executed with stale approval")
	}
	for _, call := range append([]*preparedToolInvocation{s.activeToolCall}, s.queuedToolCalls...) {
		if call == nil || call.queuedResult == nil || call.queuedResult.Error != "bash_access_approval_required" {
			t.Fatalf("changed requirements not rejected: %#v (%s)", call, mock)
		}
	}
}

func TestHostBashApprovalConcurrentBudgetDiscardsUnusedGrants(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 2)
	calls := append([]*preparedToolInvocation(nil), s.queuedToolCalls...)
	submitApprovedBashBatch(t, s, "approve", "approve")
	s.execCtx.ToolCalls = 99
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	starts := awaitApprovedBashStarts(t, e, 1)
	if starts[0].id != "tool_1" {
		t.Fatal("budget changed provider dispatch order")
	}
	close(e.release["tool_1"])
	drainApprovedBashBatch(t, s)
	if len(e.started) != 0 {
		t.Fatal("over-budget tool executed")
	}
	a := calls[1].hostBashAuthorization
	if a == nil || a.dispatched || !a.retired || len(a.access) != 0 {
		t.Fatalf("unused grant survived budget rejection: %#v", a)
	}
	if len(s.execCtx.AccessPolicyApprovals) != 0 {
		t.Fatal("unused grant escaped to run")
	}
}

func TestHostBashApprovalConcurrentInterruptBeforeDispatch(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 2)
	calls := append([]*preparedToolInvocation(nil), s.queuedToolCalls...)
	submitApprovedBashBatch(t, s, "approve", "approve")
	// Resolve grants without launching; the normal loop processes interrupts first.
	for _, call := range calls {
		if request := s.prepareHostBashAuthorization(call); request != nil {
			t.Fatal("unexpected approval")
		}
	}
	s.runControl.Interrupt(InterruptInfo{Reason: InterruptReasonUserCancelled})
	if err := s.fillNextPendingSource(); err != nil {
		t.Fatal(err)
	}
	if len(e.started) != 0 {
		t.Fatal("interrupted command started")
	}
	for _, call := range calls {
		if a := call.hostBashAuthorization; !a.retired || a.dispatched || len(a.access) != 0 {
			t.Fatalf("interrupted grant survived: %#v", a)
		}
	}
}

func TestHostBashApprovalConcurrentControlBarrier(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 2)
	barrier := &preparedToolInvocation{toolID: "set_env", toolName: "platform_control", args: map[string]any{"operation": "run.env.set", "params": map[string]any{"key": "VALUE", "value": "new"}}}
	s.queuedToolCalls = append([]*preparedToolInvocation{barrier}, s.queuedToolCalls...)
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	if s.activeToolCall != barrier || s.hitlPendingBatch != nil || len(e.started) != 0 {
		t.Fatal("Bash preflight crossed environment barrier")
	}
	for _, call := range s.queuedToolCalls {
		if call.hostBashAuthorization != nil {
			t.Fatal("later environment-dependent grant prepared early")
		}
	}
}

func TestHostBashApprovalConcurrentHardBlockAfterApproval(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 2)
	submitApprovedBashBatch(t, s, "approve_rule_run", "approve_rule_run")
	s.queuedToolCalls[0].args["command"] = "eval 'echo blocked'"
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	if len(e.started) != 0 || s.activeToolCall == nil || s.activeToolCall.queuedResult == nil || s.activeToolCall.queuedResult.Error != "bash_security_blocked" {
		t.Fatal("hard block was relaxed by approval")
	}
}
