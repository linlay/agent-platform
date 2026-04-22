package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
	contracts "agent-platform-runner-go/internal/contracts"
)

func TestInvokeHostBashSuccessReturnsPlainStdout(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"echo"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "echo hello"}, nil)
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

func TestInvokeHostBashFailureReturnsStructuredJSON(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"ls"},
				PathCheckedCommands:     []string{"ls"},
				PathCheckBypassCommands: []string{},
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "ls missing"}, nil)
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
	if got, _ := result.Structured["stdout"].(string); !strings.Contains(got, "missing") {
		t.Fatalf("expected stdout to include command output, got %#v", result.Structured)
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
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				WorkingDirectory:        t.TempDir(),
				AllowedCommands:         []string{"echo"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "cat secret.txt"}, nil)
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
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"env"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellFeaturesEnabled:    true,
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "pwd",
			"cwd":     nested,
		},
		nil,
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
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"pwd", "cd"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellFeaturesEnabled:    true,
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "cd nested && pwd",
		},
		nil,
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

func TestInvokeHostBashIgnoresPerCallEnv(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			Bash: config.BashConfig{
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"bash"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellFeaturesEnabled:    true,
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "env",
			"env":     map[string]any{"TEST_HOST_ENV": "call-value"},
		},
		nil,
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
				WorkingDirectory:        root,
				AllowedPaths:            []string{root},
				AllowedCommands:         []string{"bash"},
				PathCheckedCommands:     []string{},
				PathCheckBypassCommands: []string{},
				ShellFeaturesEnabled:    true,
				ShellExecutable:         "bash",
				ShellTimeoutMs:          30000,
				MaxCommandChars:         16000,
			},
		},
	}

	result, err := executor.invokeHostBash(
		context.Background(),
		map[string]any{
			"command": "echo \"$TEST_HOST_ENV\"",
		},
		&contracts.ExecutionContext{
			SandboxEnvOverrides: map[string]string{"TEST_HOST_ENV": "agent-value"},
		},
	)
	if err != nil {
		t.Fatalf("invokeHostBash returned error: %v", err)
	}
	if strings.TrimSpace(result.Output) != "agent-value" {
		t.Fatalf("expected agent env override to apply, got %q", result.Output)
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
