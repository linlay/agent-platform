package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/skillsexec"
)

func TestHostSkillScriptExecutionAndRevalidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "selected")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "scripts", "task.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nprintf 'skill\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executor := fileToolExecutor(dir, true)
	executor.cfg.Bash = config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true, ShellExecutable: "bash"}
	ctx := fileToolExecutionContext(dir)
	ctx.Session.AgentKey = "a"
	ctx.Session.RunID = "r"
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root}})
	args := map[string]any{"command": "sh selected/scripts/task.sh"}
	plan := executor.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	if !plan.Allowed() || plan.RuleKey != "bash-access:skill-script" {
		t.Fatalf("preflight: %+v", plan)
	}
	result, err := executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 7 || result.Structured["stdout"] != "skill\n" {
		t.Fatalf("execution: %+v %v", result, err)
	}
	metadata, _ := result.Structured["accessPolicy"].(map[string]any)
	if metadata["ruleKey"] != "bash-access:skill-script" || metadata["decision"] != "allow" {
		t.Fatalf("metadata: %+v", metadata)
	}
	if err := os.WriteFile(file, []byte("echo changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("stale script executed: %+v %v", result, err)
	}
}

// Reuse the existing sandbox probe fixture with an actual selected-skill mount.
type skillProbeSandbox struct{ *scriptProbeSandbox }

func (s *skillProbeSandbox) Execute(ctx context.Context, execCtx *contracts.ExecutionContext, command, cwd string, timeout int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	result, err := s.scriptProbeSandbox.Execute(ctx, execCtx, command, cwd, timeout, env)
	result.Stdout = strings.ReplaceAll(result.Stdout, "/workspace/task.sh", "/skills/selected/scripts/task.sh")
	if strings.HasPrefix(command, "test -d ") {
		result.Stdout = "/skills/selected/scripts\n"
	}
	return result, err
}
func TestSandboxSkillScriptExecutionAndRevalidation(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "selected")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	content := "#!/bin/sh\necho container-ok\n"
	if err := os.WriteFile(filepath.Join(root, "scripts", "task.sh"), []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	executor := fileToolExecutor(dir, true)
	client := &skillProbeSandbox{&scriptProbeSandbox{t: t, content: content}}
	executor.sandbox = client
	ctx := fileToolExecutionContext(dir)
	ctx.Session.AgentKey = "a"
	ctx.Session.RunID = "r"
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeEnvironmentID = "container"
	ctx.Session.RuntimeContext.LocalPaths.SkillsDir = dir
	ctx.Session.RuntimeContext.SandboxPaths.SkillsDir = "/skills"
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root, Guest: "/skills/selected"}})
	args := map[string]any{"command": "sh task.sh", "cwd": "/skills/selected/scripts"}
	preflight := executor.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	if !preflight.Allowed() || preflight.RuleKey != "bash-access:skill-script" {
		t.Fatalf("preflight: %+v", preflight)
	}
	result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || client.executions != 1 {
		t.Fatalf("container: %+v %v", result, err)
	}
	client.changed = true
	result, err = executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" || client.executions != 1 {
		t.Fatalf("changed container executed: %+v %v", result, err)
	}
}
