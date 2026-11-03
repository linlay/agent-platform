package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
)

func TestHostExecutorRechecksConfigBytesBeforeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix Host fixture")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	cfg := filepath.Join(root, "curl.cfg")
	marker := filepath.Join(root, "result")
	if err := os.WriteFile(filepath.Join(root, "input"), []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("silent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	ctx := bashExecutionContext(root)
	ctx.Session.AccessLevel = AccessLevelAskForApproval
	args := map[string]any{"command": "curl -q -K curl.cfg -o result " + shellQuoteProbe("file://"+filepath.Join(root, "input"))}
	first := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	if !first.RequiresApproval() {
		t.Fatal(first)
	}
	shellplan.RegisterExactApproval(ctx, first.Fingerprint)
	info, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("silent\r"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	result, err := executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("changed config launched: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("changed config ran user source")
	}
	fresh := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	shellplan.RegisterExactApproval(ctx, fresh.Fingerprint)
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("fresh config approval failed: %+v %v", result, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "approved" {
		t.Fatalf("output %q %v", data, err)
	}
}

type configProbeSandbox struct {
	t          *testing.T
	content    string
	command    string
	executions int
}

func (*configProbeSandbox) OpenIfNeeded(_ context.Context, ctx *ExecutionContext) error {
	ctx.SandboxSession = &SandboxSession{SessionID: "test-config"}
	return nil
}
func (*configProbeSandbox) CloseQuietly(*ExecutionContext) {}

const configProbeUserCommand = "curl -q -K cfg -o marker https://example.invalid"

func (s *configProbeSandbox) Execute(_ context.Context, _ *ExecutionContext, command, cwd string, _ int64, _ map[string]string) (SandboxExecutionResult, error) {
	result := func(out string) (SandboxExecutionResult, error) {
		return SandboxExecutionResult{Stdout: out, Cwd: cwd}, nil
	}
	switch {
	case command == configProbeUserCommand || command == s.command:
		if s.command != "" && command != s.command {
			s.t.Fatal("executor rewrote original source")
		}
		s.executions++
		return result("approved")
	case strings.Contains(command, "before=$(/usr/bin/stat"):
		if strings.Contains(command, "target='/workspace/cfg';") {
			return result(fmt.Sprintf("file\x00/workspace/cfg\x00%x\x00", sha256.Sum256([]byte(s.content))))
		}
		return SandboxExecutionResult{}, fmt.Errorf("unexpected configuration target")
	case strings.HasPrefix(command, "test -d "):
		return result("/workspace\n")
	case strings.HasPrefix(command, "p=$(command -v"):
		return result("/usr/bin/curl\n")
	case strings.Contains(command, "/usr/bin/sha256sum"):
		return result(fmt.Sprintf("%x  /usr/bin/curl\n\x00", sha256.Sum256([]byte("binary"))))
	default:
		return SandboxExecutionResult{}, fmt.Errorf("unexpected probe: %s", command)
	}
}
func TestSandboxExecutorRechecksGuestConfigBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	client := &configProbeSandbox{t: t, content: "first"}
	executor.sandbox = client
	ctx := fileToolExecutionContext(root)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeEnvironmentID = "test"
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	args := map[string]any{"command": configProbeUserCommand}
	first := executor.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	if !first.RequiresApproval() {
		t.Fatal(first)
	}
	shellplan.RegisterExactApproval(ctx, first.Fingerprint)
	client.content = "other"
	result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" || client.executions != 0 {
		t.Fatalf("stale guest config grant: %+v %v executions=%d", result, err, client.executions)
	}
	fresh := executor.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	shellplan.RegisterExactApproval(ctx, fresh.Fingerprint)
	result, err = executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || client.executions != 1 {
		t.Fatalf("fresh guest config grant failed: %+v %v executions=%d", result, err, client.executions)
	}
}

func TestSandboxApprovalAndLaunchKeepOriginalWhitespace(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	command := " \n" + configProbeUserCommand + "\n "
	client := &configProbeSandbox{t: t, content: "first", command: command}
	executor.sandbox = client
	ctx := fileToolExecutionContext(root)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeEnvironmentID = "test"
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	args := map[string]any{"command": command}
	p := executor.ReviewBashAccess(context.Background(), args, ctx, config.AccessPolicyConfig{})
	if !p.RequiresApproval() {
		t.Fatal(p)
	}
	shellplan.RegisterExactApproval(ctx, p.Fingerprint)
	result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || client.executions != 1 {
		t.Fatalf("sandbox source changed before launch: %+v %v executions=%d", result, err, client.executions)
	}
}
