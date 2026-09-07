package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type scriptProbeSandbox struct {
	t          *testing.T
	content    string
	changed    bool
	escapeTemp bool
	executions int
	probes     int
}

func (s *scriptProbeSandbox) OpenIfNeeded(_ context.Context, ctx *ExecutionContext) error {
	if ctx.SandboxSession == nil {
		ctx.SandboxSession = &SandboxSession{SessionID: "test-container"}
	}
	return nil
}
func (*scriptProbeSandbox) CloseQuietly(*ExecutionContext) {}
func (s *scriptProbeSandbox) Execute(_ context.Context, ctx *ExecutionContext, command, cwd string, _ int64, env map[string]string) (SandboxExecutionResult, error) {
	if err := agentconfig.ValidateUserEnvironment(env); err != nil {
		s.t.Fatal("reserved values sent as invocation overrides: ", err)
	}
	if ctx.SandboxSession == nil {
		s.t.Fatal("probe lost sandbox session")
	}
	if command == "sh task.sh" {
		s.executions++
		return SandboxExecutionResult{Stdout: "container-ok\n", ExitCode: 0, Cwd: cwd}, nil
	}
	s.probes++
	if strings.HasPrefix(command, "test -d ") {
		return SandboxExecutionResult{Stdout: "/workspace\n"}, nil
	}
	if strings.HasPrefix(command, "p=$(command -v") {
		if s.escapeTemp && strings.Contains(command, "'/tmp/escape'") {
			return SandboxExecutionResult{Stdout: "/tmp/escape\n/usr/bin/dash\n"}, nil
		}
		return SandboxExecutionResult{Stdout: "/usr/bin/dash\n"}, nil
	}
	if strings.Contains(command, "/usr/bin/sha256sum") {
		content := s.content
		if s.changed {
			content = "echo foreign"
		}
		return SandboxExecutionResult{Stdout: fmt.Sprintf("%x  /workspace/task.sh\n%s", sha256.Sum256([]byte(content)), content)}, nil
	}
	if strings.Contains(command, "/usr/bin/readlink -f") {
		if s.escapeTemp && strings.Contains(command, "'/tmp/escape.py'") {
			return SandboxExecutionResult{Stdout: "/etc/foreign.py\n"}, nil
		}
		return SandboxExecutionResult{Stdout: "/workspace/task.sh\n"}, nil
	}
	return SandboxExecutionResult{}, fmt.Errorf("unexpected probe %q", command)
}

func TestSandboxTemporarySymlinkEscapeCannotBeApproved(t *testing.T) {
	dir := t.TempDir()
	executor := fileToolExecutor(dir, true)
	executor.sandbox = &scriptProbeSandbox{t: t, escapeTemp: true}
	ctx := fileToolExecutionContext(dir)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		ctx.Session.AccessLevel = level
		ctx.AccessLevel = level
		for _, command := range []string{"python3 /tmp/escape.py", "/tmp/escape"} {
			plan := executor.ReviewBashAccess(context.Background(), map[string]any{"command": command}, ctx, config.AccessPolicyConfig{})
			if !plan.Blocked() {
				t.Fatalf("temporary escape at %s: %s: %+v", level, command, plan)
			}
		}
	}
}

func TestSandboxExecutorVerifiesMappedContentBeforeAuthoredAllow(t *testing.T) {
	dir := t.TempDir()
	content := "#!/bin/sh\necho container-ok\n"
	client := &scriptProbeSandbox{t: t, content: content}
	executor := fileToolExecutor(dir, true)
	executor.sandbox = client
	ctx := fileToolExecutionContext(dir)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeEnvironmentID = "test"
	ctx.Session.RunID = "run"
	ctx.Session.AgentKey = "ordinary"
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	write, err := executor.invokeWrite(context.Background(), map[string]any{"file_path": "task.sh", "content": content}, ctx)
	if err != nil || write.Error != "" {
		t.Fatalf("write %+v %v", write, err)
	}
	args := map[string]any{"command": "sh task.sh"}
	router, err := NewToolRouter(executor, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight := router.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	if !preflight.Allowed() {
		t.Fatalf("preflight: %+v", preflight)
	}
	result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.Output != "container-ok\n" || client.executions != 1 || client.probes == 0 {
		t.Fatalf("execute: %+v %v", result, err)
	}
	client.changed = true
	result, _ = executor.invokeSandboxBash(context.Background(), args, ctx)
	if result.Error != "bash_access_approval_required" || client.executions != 1 {
		t.Fatalf("changed container bytes were executed: %+v", result)
	}
	blocked, _ := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "echo ok > output; eval bad"}, ctx)
	if blocked.Error != "bash_security_blocked" || client.executions != 1 {
		t.Fatalf("sandbox hard block: %+v", blocked)
	}
}
