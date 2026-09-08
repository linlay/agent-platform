package llm

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/bashsec"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

type connectorRejectChecker struct{ calls int }

func (c *connectorRejectChecker) Check(command string, level int) hitl.InterceptResult {
	c.calls++
	return hitl.InterceptResult{Intercepted: true, OriginalCommand: command, Rule: hitl.FlatRule{RuleKey: "cli-approval", ViewportType: "builtin", Level: 1}}
}

func TestMountedConnectorLLMReviewKeepsResidualHITLAndOriginalCommand(t *testing.T) {
	s, _, entry := newApprovedBashStream(t, 1)
	// The fixture's executable is moved into the mounted package before freezing.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(bin, "wecom-cli")
	if err := os.Rename(entry, mounted); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("wecom", root)
	if err != nil {
		t.Fatal(err)
	}
	s.execCtx.Session.ConnectorDirs = map[string]string{"wecom": root}
	s.execCtx.Session.ConnectorCLIEntries = entries
	checker := &connectorRejectChecker{}
	s.checker = checker
	call := s.queuedToolCalls[0]
	command := "'" + mounted + "' send --json '{\n\"content\":\"中午12点会议 /proc/self/environ　\"\n}'"
	call.args["command"] = command
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.execCtx.AccessLevel = level
		s.execCtx.Session.AccessLevel = level
		if got := s.lookupBashSecurityReview(call); got.Decision != bashsec.ReviewAllow {
			t.Fatalf("%s security: %+v", level, got)
		}
		if got := s.lookupBashAccessReview(call); !got.ConnectorOnly || got.AutoApproved() || !got.Allowed() {
			t.Fatalf("%s access: %+v", level, got)
		}
		if got := s.checkBashHITL(call); got.Intercepted {
			t.Fatalf("%s HITL: %+v", level, got)
		}
	}
	if checker.calls != 0 {
		t.Fatal("CLI consulted the HITL checker")
	}
	call.args["command"] = command + " && python3 -c 'print(1)'"
	got := s.checkBashHITL(call)
	if !got.Intercepted || got.OriginalCommand != call.args["command"] || got.MatchedWhole || checker.calls != 1 {
		t.Fatalf("residual HITL lost source: %+v", got)
	}
}

func TestMountedConnectorHostBatchNeedsNoHITLOrAutoAudit(t *testing.T) {
	s, e, _ := newApprovedBashStream(t, 2)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "wecom-cli")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf ok"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := connector.SnapshotCLIEntries("wecom", root)
	if err != nil {
		t.Fatal(err)
	}
	s.execCtx.Session.ConnectorDirs = map[string]string{"wecom": root}
	s.execCtx.Session.ConnectorBinDirs = []string{bin}
	s.execCtx.Session.ConnectorCLIEntries = entries
	checker := &connectorRejectChecker{}
	s.checker = checker
	for _, call := range s.queuedToolCalls {
		call.args["command"] = "'" + entry + "' send --json '{\"content\":\"中午12点会议\"}'"
	}
	if err := s.invokeQueuedToolCallsAndPostHook(); err != nil {
		t.Fatal(err)
	}
	starts := awaitApprovedBashStarts(t, e, 2)
	if s.hitlPendingBatch != nil || checker.calls != 0 {
		t.Fatalf("mounted CLI reached HITL: batch=%+v calls=%d", s.hitlPendingBatch, checker.calls)
	}
	for _, start := range starts {
		if len(start.access) != 0 || len(start.security) != 0 {
			t.Fatalf("CLI manufactured approval grants: %+v", start)
		}
		close(e.release[start.id])
	}
	drainApprovedBashBatch(t, s)
	for _, delta := range s.pending {
		if _, ok := delta.(DeltaAwaitingAnswer); ok {
			t.Fatal("CLI emitted approval audit")
		}
	}
}
