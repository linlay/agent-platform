package llm

import (
	"agent-platform/internal/view"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

// The executor is a test double: even rmdir is never passed to a shell.
func TestHostBashCombinedBuiltinApproval(t *testing.T) {
	for _, commandKind := range []string{"opaque", "security"} {
		for _, decision := range []string{"approve", "approve_rule_run", "reject"} {
			t.Run(commandKind+"/"+decision, func(t *testing.T) {
				s, e, _ := newApprovedBashStream(t, 1)
				call := s.queuedToolCalls[0]
				// mytool is an unmodeled program (opaque access approval); xargs adds a
				// shell-level runtime-wrapper approval on top of it.
				command := "mytool '" + filepath.ToSlash(filepath.Join(s.session.WorkspaceRoot, "empty")) + "'"
				hookCommand := "mytool"
				if commandKind == "security" {
					command = "printf x | xargs " + command
					hookCommand = "xargs"
				}
				call.args["command"] = command
				hooks := t.TempDir()
				if err := os.WriteFile(filepath.Join(hooks, "dangerous.yml"), []byte("key: dangerous-commands\ncommands:\n  - command: "+hookCommand+"\n    subcommands:\n      - match: \"\"\n        level: 1\n        timeout: 37\n"), 0600); err != nil {
					t.Fatal(err)
				}
				checker, err := hitl.NewSkillChecker([]string{hooks})
				if err != nil {
					t.Fatal(err)
				}
				s.checker = checker
				if !s.rawBashAccessReview(call).RequiresApproval() {
					t.Fatal("fixture must require access approval")
				}
				match := s.checkBashHITL(call)
				if !match.Intercepted {
					t.Fatal("fixture must require builtin HITL")
				}
				submitApprovedBashBatch(t, s, decision)
				shown := call.shownApproval
				if shown == nil || shown.kind == approvalKindHITL || shown.bashHITLReview == nil || shown.bashHITLReview.Rule.RuleKey != match.Rule.RuleKey || shown.ruleTimeout != 0 || shown.bashAccessReview == nil || !shown.bashAccessReview.RequiresApproval() {
					t.Fatalf("incomplete frozen request: %#v", shown)
				}
				entry, ok := s.buildHITLNoticeEntry(call)
				if !ok {
					t.Fatal("missing approval audit")
				}
				_, audit := buildHITLBatchSummaryAndApproval([]hitlNoticeEntry{entry})
				if audit == nil || len(audit.Decisions) != 1 || len(audit.Decisions[0].ReviewedRuleKeys) < 2 {
					t.Fatal("secondary review absent from audit")
				}
				runRules := audit.Decisions[0].RunRuleKeys
				if decision == "approve_rule_run" {
					if len(runRules) != 1 || runRules[0] != shown.result.Rule.RuleKey {
						t.Fatal("audit grants undisplayed run rules")
					}
				} else if len(runRules) != 0 {
					t.Fatal("unexpected run grant in audit")
				}
				if commandKind == "security" && (shown.bashSecurityReview == nil || shown.bashSecurityReview.Decision != bashsec.ReviewRequiresApproval) {
					t.Fatal("security requirement was not frozen")
				}
				if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
					t.Fatal(err)
				}
				if s.hitlPendingBatch != nil || s.hitlPendingCall != nil {
					t.Fatalf("approved call requeued an invisible second approval: %#v", s.hitlPendingBatch)
				}
				// Single calls are dispatched by the next scheduler phase.
				close(e.release[call.toolID])
				if s.activeToolCall != nil {
					if err := s.invokeActiveToolCall(); err != nil {
						t.Fatal(err)
					}
				}
				if decision == "reject" {
					if len(e.started) != 0 {
						t.Fatal("rejected call started")
					}
				} else {
					awaitApprovedBashStarts(t, e, 1)
					if s.isRuleWhitelisted(match.Rule.RuleKey) || s.isRuleWhitelisted(shown.result.Rule.RuleKey) != (decision == "approve_rule_run") {
						t.Fatal("unexpected authorization scope")
					}
					for _, rule := range audit.Decisions[0].ReviewedRuleKeys {
						if rule != shown.result.Rule.RuleKey && (s.isRuleWhitelisted(rule) || s.execCtx.AccessPolicyRuleApprovals[rule]) {
							t.Fatalf("secondary rule escaped into run grants: %s", rule)
						}
					}
					if decision == "approve_rule_run" && commandKind == "opaque" {
						// Different deletion operands share the opaque scope, but the
						// real dangerous-command checker still demands confirmation.
						later := &preparedToolInvocation{toolID: "later", toolName: "bash", args: map[string]any{
							"command": "mytool another-target", "cwd": s.session.WorkspaceRoot,
						}}
						request := s.prepareHostBashAuthorization(later)
						if request == nil || (request.result.Rule.RuleKey != match.Rule.RuleKey && (request.bashHITLReview == nil || request.bashHITLReview.Rule.RuleKey != match.Rule.RuleKey)) {
							t.Fatalf("different target must retain secondary hook: request=%#v result=%#v", request, later.queuedResult)
						}
						later = &preparedToolInvocation{toolID: "different-cwd", toolName: "bash", args: map[string]any{
							"command": "mytool another-target", "cwd": t.TempDir(),
						}}
						request = s.prepareHostBashAuthorization(later)
						if request == nil || request.kind != approvalKindBashAccess || request.result.Rule.RuleKey == shown.result.Rule.RuleKey {
							t.Fatal("opaque run grant leaked to another cwd")
						}
					}
				}
				results := 0
				for _, delta := range s.pending {
					if r, ok := delta.(DeltaToolResult); ok {
						results++
						want := ""
						if decision == "reject" {
							want = "user_rejected"
						}
						if r.Result.Error != want {
							t.Fatalf("result: %#v", r)
						}
					}
				}
				if results != 1 {
					t.Fatalf("want one result, got %d", results)
				}
			})
		}
	}
}

func TestHostBashBuiltinRuleChangedAfterApproval(t *testing.T) {
	for _, decision := range []string{"approve", "approve_rule_run"} {
		for _, initiallyMatched := range []bool{false, true} {
			t.Run(decision+"/"+fmt.Sprint(initiallyMatched), func(t *testing.T) {
				s, e, _ := newApprovedBashStream(t, 1)
				call := s.queuedToolCalls[0]
				command := mapStringArg(call.args, "command")
				matches := map[string]hitl.InterceptResult{}
				if initiallyMatched {
					matches[command] = hitl.InterceptResult{Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "old-hook", Level: 1}}
				}
				s.checker = commandResultChecker{results: matches}
				submitApprovedBashBatch(t, s, decision)
				matches[command] = hitl.InterceptResult{Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "new-hook", Level: 1}}
				if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
					t.Fatal(err)
				}
				if s.hitlPendingBatch != nil || s.hitlPendingCall != nil {
					t.Fatal("changed rule opened another wait")
				}
				if err := s.invokeActiveToolCall(); err != nil {
					t.Fatal(err)
				}
				if len(e.started) != 0 || s.isRuleWhitelisted("new-hook") {
					t.Fatal("new rule authorized by stale decision")
				}
				results := 0
				for _, delta := range s.pending {
					if r, ok := delta.(DeltaToolResult); ok {
						results++
						if r.Result.Error != "bash_access_approval_required" {
							t.Fatalf("unexpected result: %#v", r)
						}
					}
				}
				if results != 1 || call.hitlDecision.Executed {
					t.Fatal("changed rule did not terminate as unexecuted")
				}
			})
		}
	}
}

func TestHostBashCombinedApprovalConcurrentIsolation(t *testing.T) {
	for _, decision := range []string{"approve", "approve_rule_run"} {
		t.Run(decision, func(t *testing.T) {
			s, e, _ := newApprovedBashStream(t, 3)
			command := mapStringArg(s.queuedToolCalls[0].args, "command")
			s.checker = commandResultChecker{results: map[string]hitl.InterceptResult{command: {Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "mock-hook", Level: 1}}}}
			submitApprovedBashBatch(t, s, decision, "reject", decision)
			if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
				t.Fatal(err)
			}
			starts := awaitApprovedBashStarts(t, e, 2)
			for _, start := range starts {
				if start.id == "tool_2" {
					t.Fatal("rejected sibling started")
				}
				close(e.release[start.id])
			}
			drainApprovedBashBatch(t, s)
			if s.hitlPendingBatch != nil || len(e.started) != 0 {
				t.Fatal("unexpected wait or execution")
			}
			for _, delta := range s.pending {
				if _, ok := delta.(DeltaAwaitAsk); ok {
					t.Fatal("second approval emitted")
				}
			}
			// A later sibling cannot borrow a one-shot grant; run approval covers both
			// the frozen access scope and hook, exactly as the displayed decision says.
			later := &preparedToolInvocation{toolID: "later", toolName: "bash"}
			later.args = map[string]any{"command": command, "cwd": s.session.WorkspaceRoot}
			request := s.prepareHostBashAuthorization(later)
			if request == nil {
				t.Fatal("secondary hook silently inherited run approval")
			}
			if decision == "approve_rule_run" && (request.kind != approvalKindHITL || request.result.Rule.RuleKey != "mock-hook") {
				t.Fatal("primary access rule was not reused")
			}
			if s.isRuleWhitelisted("mock-hook") {
				t.Fatal("secondary rule was whitelisted")
			}
		})
	}
}

func TestHostBashCombinedApprovalExcludesSandboxAndForms(t *testing.T) {
	s, _, _ := newApprovedBashStream(t, 1)
	call := s.queuedToolCalls[0]
	s.session.AgentHasRuntimeSandbox = true
	if s.usesHostBashAuthorization(call) {
		t.Fatal("sandbox entered host authorization")
	}
	s.session.AgentHasRuntimeSandbox = false
	command := mapStringArg(call.args, "command")
	s.checker = commandResultChecker{results: map[string]hitl.InterceptResult{command: {Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{Mode: "form", View: view.Builtin("platform_control_review")}}}}
	if s.usesHostBashAuthorization(call) {
		t.Fatal("mutable form entered immutable builtin authorization")
	}
	request := s.bashAccessApprovalRequest(call, s.rawBashAccessReview(call))
	combined := s.hostBashApprovalNeeded(call, request, "bash_access_approval_required", "")
	if combined == nil || combined.kind != approvalKindBashAccess {
		t.Fatal("form was folded into builtin approval")
	}
}

func TestHostBashApprovedSnapshotCannotUseRunRuleToCoverChanges(t *testing.T) {
	for _, decision := range []string{"approve", "approve_rule_run"} {
		for _, change := range []string{"command", "cwd", "script"} {
			t.Run(decision+"/"+change, func(t *testing.T) {
				s, e, _ := newApprovedBashStream(t, 1)
				call := s.queuedToolCalls[0]
				script := filepath.Join(s.session.WorkspaceRoot, "task.sh")
				if err := os.WriteFile(script, []byte("echo before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if change == "script" {
					call.args["command"] = "sh '" + filepath.ToSlash(script) + "'"
				}
				submitApprovedBashBatch(t, s, decision)
				switch change {
				case "command":
					call.args["command"] = mapStringArg(call.args, "command") + " different-target"
				case "cwd":
					call.args["cwd"] = t.TempDir()
				case "script":
					if err := os.WriteFile(script, []byte("echo after\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
					t.Fatal(err)
				}
				if s.hitlPendingBatch != nil {
					t.Fatal("changed snapshot requeued approval")
				}
				if err := s.invokeActiveToolCall(); err != nil {
					t.Fatal(err)
				}
				if len(e.started) != 0 {
					t.Fatal("changed snapshot executed")
				}
				results := 0
				for _, delta := range s.pending {
					if r, ok := delta.(DeltaToolResult); ok {
						results++
						if r.Result.Error != "bash_access_approval_required" {
							t.Fatalf("wrong result: %#v", r)
						}
					}
				}
				if results != 1 {
					t.Fatalf("expected terminal result, got %d", results)
				}
			})
		}
	}
}

func TestHostBashCombinedApprovalAccessLevelDoesNotApproveHook(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 1)
	call := s.queuedToolCalls[0]
	command := mapStringArg(call.args, "command")
	s.checker = commandResultChecker{results: map[string]hitl.InterceptResult{command: {Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "mock-hook", Level: 1}}}}
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	batch := s.hitlPendingBatch
	if batch == nil {
		t.Fatal("missing combined approval")
	}
	s.runControl.UpdateAccessLevel(AccessLevelFullAccess)
	resolved, err := s.tryResolvePendingAccessLevelBatch(batch)
	if err != nil || resolved || len(e.started) != 0 {
		t.Fatal("access level answered the secondary hook")
	}
	resolved, err = s.tryResolvePendingAccessLevelApproval(call, batch.matches[0], batch.awaitingID)
	if err != nil || resolved || len(e.started) != 0 {
		t.Fatal("single access-level path answered the secondary hook")
	}
}
