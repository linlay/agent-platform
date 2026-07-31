package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	contracts "agent-platform/internal/contracts"
)

func TestResolveHostShellInvocationDefaultsToPowerShellOnWindows(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{}, "Get-Process", "windows")

	if executable != "powershell.exe" {
		t.Fatalf("expected powershell.exe, got %q", executable)
	}
	wantCommand := "$OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false; [Console]::OutputEncoding = $OutputEncoding; Get-Process"
	wantArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", wantCommand}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationDefaultsToUTF8CmdOnWindows(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "cmd.exe",
	}, "dir", "windows")

	if executable != "cmd.exe" {
		t.Fatalf("expected cmd.exe, got %q", executable)
	}
	wantArgs := []string{"/d", "/s", "/c", "chcp 65001 >NUL & dir"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationDefaultsToBashOnUnix(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{}, "pwd", "linux")

	if executable != "bash" {
		t.Fatalf("expected bash, got %q", executable)
	}
	wantArgs := []string{"-o", "pipefail", "-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationLeavesNonBashUnixDefaultsUnchanged(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "sh",
	}, "pwd", "linux")

	if executable != "sh" {
		t.Fatalf("expected sh, got %q", executable)
	}
	wantArgs := []string{"-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationCustomBashArgsRemainAuthoritative(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "bash",
		ShellArgs:       []string{"-lc", "{{command}}"},
	}, "pwd", "linux")

	if executable != "bash" {
		t.Fatalf("expected bash, got %q", executable)
	}
	wantArgs := []string{"-lc", "pwd"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationSupportsCustomArgs(t *testing.T) {
	executable, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "cmd.exe",
		ShellArgs:       []string{"/d", "/s", "/c", "{{command}}"},
	}, "dir", "windows")

	if executable != "cmd.exe" {
		t.Fatalf("expected cmd.exe, got %q", executable)
	}
	wantArgs := []string{"/d", "/s", "/c", "dir"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestResolveHostShellInvocationAppendsCommandWithoutPlaceholder(t *testing.T) {
	_, args := resolveHostShellInvocation(config.BashConfig{
		ShellExecutable: "pwsh.exe",
		ShellArgs:       []string{"-NoProfile", "-Command"},
	}, "node --version", "windows")

	wantArgs := []string{"-NoProfile", "-Command", "node --version"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
}

func TestInvokeHostBashSuccessReturnsPlainStdout(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"echo"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "echo hello"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Output != "hello\n" {
		t.Fatalf("expected raw stdout, got %q", result.Output)
	}
	if result.Structured != nil {
		t.Fatalf("expected nil structured result, got %#v", result.Structured)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Error != "" {
		t.Fatalf("expected empty error, got %q", result.Error)
	}
}

func TestInvokeHostBashPipefailPreservesUpstreamFailure(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"false", "tail"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{
		"command": "false | tail -200",
	}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("expected pipefail to preserve upstream exit code 1, got %#v", result)
	}
	if result.Structured == nil || result.Structured["exitCode"] != 1 {
		t.Fatalf("expected structured pipeline failure, got %#v", result)
	}
}

func TestInvokeHostBashSuccessWithStderrReturnsStructuredJSON(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "emit.sh")
	if err := os.WriteFile(scriptPath, []byte("printf 'warn\\n' >&2\nprintf 'ok\\n'\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			AccessPolicy: config.AccessPolicyConfig{
				Levels: map[string]config.AccessPolicyLevelConfig{
					contracts.AccessLevelDefault: {
						Approvals: config.AccessPolicyApprovalConfig{
							BashOpaqueCommand: "allow",
						},
					},
				},
			},
			Bash: config.BashConfig{
				AllowedCommands: []string{"sh"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "sh " + scriptPath}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Structured == nil {
		t.Fatal("expected structured result when stderr is present")
	}
	if result.Structured["stdout"] != "ok\n" {
		t.Fatalf("expected stdout to stay separate, got %#v", result.Structured)
	}
	if result.Structured["stderr"] != "warn\n" {
		t.Fatalf("expected stderr to be preserved, got %#v", result.Structured)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if payload["stderr"] != "warn\n" {
		t.Fatalf("expected marshaled stderr to be preserved, got %#v", payload)
	}
}

func TestInvokeHostBashDoesNotWaitForBackgroundProcessOutput(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"echo", "sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	start := time.Now()
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "sleep 2 & echo done"}, bashExecutionContext(root))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("expected background command to return quickly, took %s", elapsed)
	}
	if result.Output != "done\n" {
		t.Fatalf("expected raw stdout from shell command, got %q", result.Output)
	}
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
}

func TestInvokeHostBashDefaultsTimeoutToToolBudget(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"sleep"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	start := time.Now()
	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{"command": "sleep 2"},
		&contracts.ExecutionContext{
			Session: contracts.QuerySession{WorkspaceRoot: root},
			Budget:  contracts.Budget{Tool: contracts.RetryPolicy{Timeout: 1}},
		},
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.ExitCode != -1 || !strings.Contains(result.Output, "Command timed out") {
		t.Fatalf("expected command timeout result, got %#v", result)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("expected budget timeout near 1s, took %s", elapsed)
	}
}

func TestResolveBashTimeoutCapsRequestedTimeoutAtToolBudget(t *testing.T) {
	executor := &RuntimeToolExecutor{}
	execCtx := &contracts.ExecutionContext{Budget: contracts.Budget{Tool: contracts.RetryPolicy{Timeout: 5}}}

	if got := executor.resolveBashTimeoutSeconds(map[string]any{}, execCtx); got != 5 {
		t.Fatalf("default bash timeout = %d, want tool budget 5", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{"timeout": 2}, execCtx); got != 2 {
		t.Fatalf("short requested bash timeout = %d, want 2", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{"timeout": 10}, execCtx); got != 5 {
		t.Fatalf("capped requested bash timeout = %d, want tool budget 5", got)
	}
	if got := executor.resolveBashTimeoutSeconds(map[string]any{}, nil); got != defaultBashTimeoutSeconds {
		t.Fatalf("fallback bash timeout = %d, want %d", got, defaultBashTimeoutSeconds)
	}
}

func TestInvokeHostBashDefaultsCwdToSessionWorkspace(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{WorkspaceRoot: root}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "pwd"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if strings.TrimSpace(result.Output) != want {
		t.Fatalf("expected pwd in workspace %q, got %q", want, result.Output)
	}
}

func TestInvokeHostBashWithoutWorkspaceRequiresExplicitCwd(t *testing.T) {
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{"command": "pwd"},
		&contracts.ExecutionContext{Session: contracts.QuerySession{}},
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "workspace_unavailable" ||
		!strings.Contains(result.Output, "pass cwd explicitly") ||
		!strings.Contains(result.Output, "@chat") {
		t.Fatalf("expected actionable workspace_unavailable result, got %#v", result)
	}
}

func TestInvokeHostBashWithoutWorkspaceAllowsExplicitChatAndAgentCwd(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(root, "chats", "chat-1")
	agentDir := filepath.Join(root, "agents", "bootstrap")
	for _, dir := range []string{chatDir, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"pwd"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: chatDir, AgentDir: agentDir},
		},
	}}

	for alias, want := range map[string]string{"@chat": chatDir, "@agent": agentDir} {
		result, err := executor.invokeHostBash(
			context.Background(),
			map[string]any{"command": "pwd", "cwd": alias},
			execCtx,
		)
		if err != nil {
			t.Fatalf("invokeHostBash(%s) returned error: %v", alias, err)
		}
		want, _ = filepath.EvalSymlinks(want)
		if result.Error != "" || strings.TrimSpace(result.Output) != want {
			t.Fatalf("expected pwd in %s %q, got %#v", alias, want, result)
		}
	}
}

func TestInvokeHostBashFailureReturnsStructuredJSON(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"ls"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "ls missing"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Structured == nil {
		t.Fatal("expected structured failure result")
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code, got %#v", result)
	}
	if got, ok := result.Structured["exitCode"].(int); !ok || got != result.ExitCode {
		t.Fatalf("expected structured exit code %d, got %#v", result.ExitCode, result.Structured["exitCode"])
	}
	if got, _ := result.Structured["stderr"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("expected stderr metadata, got %#v", result.Structured)
	}
	if got, _ := result.Structured["stdout"].(string); got != "" {
		t.Fatalf("expected stdout to stay separate from stderr, got %#v", result.Structured)
	}
	if got, _ := result.Structured["stderr"].(string); !strings.Contains(got, "missing") {
		t.Fatalf("expected stderr to include command output, got %#v", result.Structured)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if _, ok := payload["stderr"]; !ok {
		t.Fatalf("expected stderr in marshaled output, got %#v", payload)
	}
}

func TestInvokeHostBashEarlyReturnStaysHumanReadable(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands: []string{"echo"},
				ShellExecutable: "bash",
				MaxCommandChars: 16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat secret.txt"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "command_not_allowed" {
		t.Fatalf("expected command_not_allowed, got %#v", result)
	}
	if result.Structured != nil {
		t.Fatalf("expected nil structured result for early return, got %#v", result.Structured)
	}
	if !strings.Contains(result.Output, "Command not allowed: cat") {
		t.Fatalf("expected human-readable rejection, got %q", result.Output)
	}
}

func TestInvokeHostBashSupportsPerCallCwd(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"env"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "pwd",
			"cwd":     nested,
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	resolvedNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	got := strings.TrimSpace(result.Output)
	if got != nested && got != resolvedNested {
		t.Fatalf("expected cwd line to match %q or %q, got %q", nested, resolvedNested, got)
	}
}

func TestInvokeHostBashAllowsShellSyntaxByDefault(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"pwd", "cd"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "cd nested && pwd",
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	resolvedNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	got := strings.TrimSpace(result.Output)
	if got != nested && got != resolvedNested {
		t.Fatalf("expected shell syntax command to resolve nested cwd, got %q", got)
	}
}

func TestInvokeHostBashAllowsExitStatusSpecialParameter(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"false", "echo"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": `false; echo "Exit code: $?"`,
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected command to execute, got %#v", result)
	}
	if strings.TrimSpace(result.Output) != "Exit code: 1" {
		t.Fatalf("expected shell to preserve real exit status, got %q", result.Output)
	}
}

func TestInvokeHostBashIgnoresPerCallEnv(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"bash"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "env",
			"env":     map[string]any{"TEST_HOST_ENV": "call-value"},
		},
		bashExecutionContext(root),
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if strings.Contains(result.Output, "TEST_HOST_ENV=call-value") {
		t.Fatalf("expected host per-call env to be ignored, got %q", result.Output)
	}
}

func TestInvokeHostBashAppliesAgentEnvOverrides(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"bash"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "echo \"$TEST_HOST_ENV\"",
		},
		&contracts.ExecutionContext{
			Session:             contracts.QuerySession{WorkspaceRoot: root},
			RuntimeEnvOverrides: map[string]string{"TEST_HOST_ENV": "agent-value"},
		},
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if strings.TrimSpace(result.Output) != "agent-value" {
		t.Fatalf("expected agent env override to apply, got %q", result.Output)
	}
}

func TestMergeCommandEnvInjectsReservedAgentAndChatContextAfterRuntimeOverrides(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents", "reader")
	chatDir := filepath.Join(root, "chats", "chat-1")
	t.Setenv("AP_AGENT_CONFIG_HOME", "/process-config")
	t.Setenv("AP_CHAT_DIR", "/process-chat")
	valuesFor := func(env []string) map[string]string {
		t.Helper()
		values := map[string]string{}
		for _, item := range env {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				values[key] = value
			}
		}
		return values
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			WorkspaceRoot: root,
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{AgentDir: agentDir, WorkspaceDir: root, ChatDir: chatDir},
			},
		},
	}
	if got, want := valuesFor(mergeCommandEnv(execCtx))["AP_AGENT_CONFIG_HOME"], filepath.Join(agentDir, ".config"); got != want {
		t.Fatalf("default AP_AGENT_CONFIG_HOME = %q, want %q", got, want)
	}
	if got := valuesFor(mergeCommandEnv(execCtx))["AP_CHAT_DIR"]; got != chatDir {
		t.Fatalf("default AP_CHAT_DIR = %q, want %q", got, chatDir)
	}
	if got := valuesFor(mergeCommandEnv(execCtx))["AP_WORKSPACE_DIR"]; got != root {
		t.Fatalf("default AP_WORKSPACE_DIR = %q, want %q", got, root)
	}
	execCtx.RuntimeEnvOverrides = map[string]string{
		"AP_AGENT_CONFIG_HOME": "/agent-custom",
		"AP_WORKSPACE_DIR":     "/wrong-workspace",
		"AP_CHAT_DIR":          "/wrong-chat",
	}
	got := valuesFor(mergeCommandEnv(execCtx))
	if want := filepath.Join(agentDir, ".config"); got["AP_AGENT_CONFIG_HOME"] != want {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want reserved value %q", got["AP_AGENT_CONFIG_HOME"], want)
	}
	if got["AP_CHAT_DIR"] != chatDir {
		t.Fatalf("AP_CHAT_DIR = %q, want reserved value %q", got["AP_CHAT_DIR"], chatDir)
	}
	if got["AP_WORKSPACE_DIR"] != root {
		t.Fatalf("AP_WORKSPACE_DIR = %q, want reserved value %q", got["AP_WORKSPACE_DIR"], root)
	}
}

func TestInvokeHostBashSoftSecurityRequiresApproval(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "printf ok > owner.md"}, bashExecutionContext(root))
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_approval_required" {
		t.Fatalf("expected bash_security_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashConsumesMatchingSoftSecurityApproval(t *testing.T) {
	root := t.TempDir()
	command := "printf ok > owner.md"
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint(command): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected approved command to execute, got %#v", result)
	}
	if _, ok := execCtx.BashSecurityApprovals[bashsec.ApprovalFingerprint(command)]; ok {
		t.Fatalf("expected approval to be consumed, got %#v", execCtx.BashSecurityApprovals)
	}
	data, err := os.ReadFile(filepath.Join(root, "owner.md"))
	if err != nil {
		t.Fatalf("read owner.md: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("expected written content, got %q", string(data))
	}
}

func TestInvokeHostBashRejectsMismatchedSoftSecurityApproval(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"printf"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{WorkspaceRoot: root},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint("printf ok > other.md"): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "printf ok > owner.md"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_approval_required" {
		t.Fatalf("expected bash_security_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashAccessPolicyRequiresApprovalForOutsidePath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"cat"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelDefault,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat " + secret}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_access_approval_required" {
		t.Fatalf("expected bash_access_approval_required, got %#v", result)
	}
}

func TestInvokeHostBashAutoApprovedAccessAddsMetadata(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"cat"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat " + secret}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected bash success, got %#v", result)
	}
	if result.Output != "secret\n" {
		t.Fatalf("expected stdout to stay plain, got %q", result.Output)
	}
	meta, _ := result.Structured["accessPolicy"].(map[string]any)
	if meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove {
		t.Fatalf("expected auto approval metadata, got %#v", result.Structured["accessPolicy"])
	}
}

func TestInvokeHostBashAutoApprovedReadWithDevNullRedirection(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	note := filepath.Join(outside, "note.md")
	if err := os.WriteFile(note, []byte("# note\n"), 0o644); err != nil {
		t.Fatalf("write outside note: %v", err)
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	command := "find " + outside + ` -maxdepth 1 -name "*.md" -type f 2>/dev/null`
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected bash success, got %#v", result)
	}
	if strings.TrimSpace(result.Output) != note {
		t.Fatalf("expected note path in stdout, got %q", result.Output)
	}
	meta, _ := result.Structured["accessPolicy"].(map[string]any)
	if meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove {
		t.Fatalf("expected auto approval metadata, got %#v", result.Structured["accessPolicy"])
	}
}

func TestInvokeHostBashRealOutsideRedirectionStillRequiresAccessApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	logPath := filepath.Join(outside, "out.log")
	command := "printf ok > " + logPath
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			AccessLevel:   contracts.AccessLevelAutoApprove,
			WorkspaceRoot: root,
		},
		BashSecurityApprovals: map[string]int{
			bashsec.ApprovalFingerprint(command): 1,
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_access_approval_required" {
		t.Fatalf("expected bash_access_approval_required, got %#v", result)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("did not expect redirected file to be written, stat err=%v", err)
	}
}

func TestInvokeHostBashFullAccessStillKeepsBashsecHardBlock(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				AllowedCommands:      []string{"*"},
				ShellFeaturesEnabled: true,
				ShellExecutable:      "bash",
				MaxCommandChars:      16000,
			},
		},
	}
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelFullAccess,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "eval echo hi"}, execCtx)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if result.Error != "bash_security_blocked" {
		t.Fatalf("expected bash_security_blocked, got %#v", result)
	}
}

func TestBashResultHardErrorReturnsStructuredJSON(t *testing.T) {
	result := bashResult("partial output", "runtime exploded", "host", "/tmp/work", 0, "sandbox_execute_failed")

	if result.Structured == nil {
		t.Fatal("expected structured failure result")
	}
	if result.Error != "sandbox_execute_failed" {
		t.Fatalf("expected helper to keep hard error, got %#v", result)
	}
	if result.Structured["error"] != "sandbox_execute_failed" {
		t.Fatalf("expected error in payload, got %#v", result.Structured)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if payload["error"] != "sandbox_execute_failed" {
		t.Fatalf("expected error in marshaled output, got %#v", payload)
	}
}

func bashExecutionContext(workspaceRoot string) *contracts.ExecutionContext {
	return &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			WorkspaceRoot: workspaceRoot,
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{WorkspaceDir: workspaceRoot},
			},
		},
	}
}
