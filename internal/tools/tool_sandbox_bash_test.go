package tools

import (
	"context"
	"encoding/json"
	"testing"

	contracts "agent-platform-runner-go/internal/contracts"
)

type stubSandboxClient struct {
	result contracts.SandboxExecutionResult
	err    error
	env    map[string]string
}

func (s *stubSandboxClient) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *stubSandboxClient) Execute(_ context.Context, _ *contracts.ExecutionContext, _ string, _ string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	s.env = env
	return s.result, s.err
}

func (s *stubSandboxClient) CloseQuietly(_ *contracts.ExecutionContext) {}

func TestInvokeSandboxBashSuccessReturnsPlainStdout(t *testing.T) {
	executor := &RuntimeToolExecutor{
		sandbox: &stubSandboxClient{
			result: contracts.SandboxExecutionResult{
				ExitCode:         0,
				Stdout:           "alpha\nbeta\n",
				Stderr:           "",
				WorkingDirectory: "/workspace",
			},
		},
	}

	result, err := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "ls"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
	}
	if result.Output != "alpha\nbeta\n" {
		t.Fatalf("expected raw stdout, got %q", result.Output)
	}
	if result.Structured != nil {
		t.Fatalf("expected nil structured result, got %#v", result.Structured)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestInvokeSandboxBashFailureReturnsStructuredJSON(t *testing.T) {
	executor := &RuntimeToolExecutor{
		sandbox: &stubSandboxClient{
			result: contracts.SandboxExecutionResult{
				ExitCode:         2,
				Stdout:           "",
				Stderr:           "ls: cannot access missing: No such file or directory\n",
				WorkingDirectory: "/workspace",
			},
		},
	}

	result, err := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "ls missing"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
	}
	if result.Structured == nil {
		t.Fatal("expected structured failure result")
	}
	if result.ExitCode != 2 {
		t.Fatalf("expected exit code 2, got %#v", result)
	}
	if got, ok := result.Structured["exitCode"].(int); !ok || got != 2 {
		t.Fatalf("expected structured exit code 2, got %#v", result.Structured["exitCode"])
	}
	if result.Structured["stderr"] != "ls: cannot access missing: No such file or directory\n" {
		t.Fatalf("unexpected stderr payload %#v", result.Structured)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", result.Output, err)
	}
	if payload["stderr"] != "ls: cannot access missing: No such file or directory\n" {
		t.Fatalf("unexpected marshaled payload %#v", payload)
	}
}

func TestInvokeSandboxBashForwardsEnv(t *testing.T) {
	sandbox := &stubSandboxClient{
		result: contracts.SandboxExecutionResult{
			ExitCode:         0,
			Stdout:           "ok\n",
			WorkingDirectory: "/workspace",
		},
	}
	executor := &RuntimeToolExecutor{sandbox: sandbox}

	_, err := executor.invokeSandboxBash(context.Background(), map[string]any{
		"command": "echo ok",
		"env":     map[string]any{"FOO": "bar"},
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
	}
	if sandbox.env["FOO"] != "bar" {
		t.Fatalf("expected env to be forwarded, got %#v", sandbox.env)
	}
}
