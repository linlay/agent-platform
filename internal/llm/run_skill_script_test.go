package llm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/skillsexec"
)

func TestRunSkillScriptDoesNotAwaitOrGrantInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix interpreter fixture")
	}
	root := t.TempDir()
	scripts := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "task.sh"), []byte("echo skill\n"), 0700); err != nil {
		t.Fatal(err)
	}
	session := QuerySession{AgentKey: "a", RunID: "r", ChatID: "c", WorkspaceRoot: root, AccessLevel: AccessLevelDefault}
	execCtx := &ExecutionContext{Session: session}
	session.SkillScripts = skillsexec.New(execCtx.ScriptOwner(), []skillsexec.Root{{Host: root}})
	execCtx.Session = session
	executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{bashToolDefinition()}}
	stream := &llmRunStream{ctx: context.Background(), session: session, execCtx: execCtx, engine: &LLMAgentEngine{cfg: config.Config{}, tools: executor}}
	approvals := 0
	stream.onApprovalSummary = func(chat.StepApproval) { approvals++ }
	invocation := &preparedToolInvocation{toolID: "script", toolName: "bash", args: map[string]any{"command": "sh scripts/task.sh"}}
	stream.activeToolCall = invocation
	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatal(err)
	}
	if stream.hitlPendingCall != nil || len(executor.invocations) != 1 || approvals != 0 {
		t.Fatalf("unexpected approval/execution: pending=%+v calls=%d approvals=%d", stream.hitlPendingCall, len(executor.invocations), approvals)
	}
	for _, delta := range stream.pending {
		if _, ok := delta.(DeltaAwaitAsk); ok {
			t.Fatal("skill script emitted awaiting")
		}
	}
	// Tool contexts retain the same in-memory snapshot; no interpreter rule is granted.
	next := stream.serialExecutionContext(&preparedToolInvocation{toolID: "next"})
	if next.Session.SkillScripts != session.SkillScripts {
		t.Fatal("tool context lost scope")
	}
	foreign := &preparedToolInvocation{toolID: "foreign", toolName: "bash", args: map[string]any{"command": "sh foreign.sh"}}
	if !stream.lookupBashAccessReview(foreign).RequiresApproval() {
		t.Fatal("skill execution approved other scripts")
	}
}
