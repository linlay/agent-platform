package tools

import (
	"context"
	"encoding/json"
	"testing"

	contracts "agent-platform/internal/contracts"
)

type stubSandboxClient struct {
	result  contracts.SandboxExecutionResult
	err     error
	env     map[string]string
	timeout int64
}

func (s *stubSandboxClient) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *stubSandboxClient) Execute(_ context.Context, _ *contracts.ExecutionContext, _ string, _ string, timeout int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	s.timeout = timeout
	s.env = env
	return s.result, s.err
}

func (s *stubSandboxClient) CloseQuietly(_ *contracts.ExecutionContext) {}

func sandboxBashExecutionContext(t *testing.T) *contracts.ExecutionContext {
	t.Helper()
	workspace := t.TempDir()
	chatDir := t.TempDir()
	return &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot:          workspace,
		ChatRoot:               chatDir,
		AccessLevel:            contracts.AccessLevelFullAccess,
		AgentHasRuntimeSandbox: true,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: workspace, ChatDir: chatDir},
			SandboxPaths: contracts.SandboxPaths{
				WorkspaceDir: "/workspace",
				ChatDir:      "/chat",
			},
		},
	}}
}

func TestInvokeSandboxBashSuccessReturnsPlainStdout(t *testing.T) {
	executor := &RuntimeToolExecutor{
		sandbox: &stubSandboxClient{
			result: contracts.SandboxExecutionResult{
				ExitCode: 0,
				Stdout:   "alpha\nbeta\n",
				Stderr:   "",
				Cwd:      "/workspace",
			},
		},
	}

	result, err := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "ls"}, sandboxBashExecutionContext(t))
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

func TestInvokeSandboxBashSuccessWithStderrReturnsStructuredJSON(t *testing.T) {
	executor := &RuntimeToolExecutor{
		sandbox: &stubSandboxClient{
			result: contracts.SandboxExecutionResult{
				ExitCode: 0,
				Stdout:   "ok\n",
				Stderr:   "warn\n",
				Cwd:      "/workspace",
			},
		},
	}

	result, err := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "sample"}, sandboxBashExecutionContext(t))
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
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

func TestInvokeSandboxBashFailureReturnsStructuredJSON(t *testing.T) {
	executor := &RuntimeToolExecutor{
		sandbox: &stubSandboxClient{
			result: contracts.SandboxExecutionResult{
				ExitCode: 2,
				Stdout:   "",
				Stderr:   "ls: cannot access missing: No such file or directory\n",
				Cwd:      "/workspace",
			},
		},
	}

	result, err := executor.invokeSandboxBash(context.Background(), map[string]any{"command": "ls missing"}, sandboxBashExecutionContext(t))
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
			ExitCode: 0,
			Stdout:   "ok\n",
			Cwd:      "/workspace",
		},
	}
	executor := &RuntimeToolExecutor{sandbox: sandbox}

	_, err := executor.invokeSandboxBash(context.Background(), map[string]any{
		"command": "echo ok",
		"env": []any{
			map[string]any{"name": "FOO", "value": "bar"},
			map[string]any{"name": "EMPTY", "value": ""},
		},
	}, sandboxBashExecutionContext(t))
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
	}
	if sandbox.env["FOO"] != "bar" {
		t.Fatalf("expected env to be forwarded, got %#v", sandbox.env)
	}
	if value, exists := sandbox.env["EMPTY"]; !exists || value != "" {
		t.Fatalf("expected empty env value to be forwarded, got %#v", sandbox.env)
	}
}

func TestInvokeSandboxBashRejectsInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  any
	}{
		{name: "null", env: nil},
		{name: "legacy object", env: map[string]any{"FOO": "bar"}},
		{name: "non object item", env: []any{"FOO=bar"}},
		{name: "missing name", env: []any{map[string]any{"value": "bar"}}},
		{name: "missing value", env: []any{map[string]any{"name": "FOO"}}},
		{name: "non string value", env: []any{map[string]any{"name": "FOO", "value": true}}},
		{name: "invalid empty name", env: []any{map[string]any{"name": "", "value": "bar"}}},
		{name: "invalid leading digit", env: []any{map[string]any{"name": "1FOO", "value": "bar"}}},
		{name: "invalid punctuation", env: []any{map[string]any{"name": "FOO-BAR", "value": "bar"}}},
		{name: "duplicate name", env: []any{
			map[string]any{"name": "FOO", "value": "one"},
			map[string]any{"name": "FOO", "value": "two"},
		}},
		{name: "extra field", env: []any{map[string]any{"name": "FOO", "value": "bar", "secret": true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := &stubSandboxClient{}
			executor := &RuntimeToolExecutor{sandbox: sandbox}
			result, err := executor.invokeSandboxBash(context.Background(), map[string]any{
				"command": "echo ok",
				"env":     tc.env,
			}, sandboxBashExecutionContext(t))
			if err != nil {
				t.Fatalf("invokeSandboxBash returned error: %v", err)
			}
			if result.Error != "invalid_environment" || result.ExitCode != -1 {
				t.Fatalf("unexpected invalid environment result: %#v", result)
			}
			if sandbox.env != nil {
				t.Fatalf("sandbox execute received invalid environment: %#v", sandbox.env)
			}
		})
	}
}

func TestInvokeSandboxBashRejectsReservedEnvironment(t *testing.T) {
	for _, key := range []string{"AP_AGENT_CONFIG_HOME", "AP_WORKSPACE_DIR", "AP_CHAT_DIR", "AP_ACCESS_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			sandbox := &stubSandboxClient{}
			executor := &RuntimeToolExecutor{sandbox: sandbox}

			result, err := executor.invokeSandboxBash(context.Background(), map[string]any{
				"command": "echo ok",
				"env": []any{
					map[string]any{"name": key, "value": "/custom"},
				},
			}, &contracts.ExecutionContext{})
			if err != nil {
				t.Fatalf("invokeSandboxBash returned error: %v", err)
			}
			if result.Error != "reserved_environment_variable" || result.ExitCode != -1 {
				t.Fatalf("unexpected reserved environment result: %#v", result)
			}
			if sandbox.env != nil {
				t.Fatalf("sandbox execute received rejected environment: %#v", sandbox.env)
			}
		})
	}
}

func TestInvokeSandboxBashDefaultsTimeoutToToolBudget(t *testing.T) {
	sandbox := &stubSandboxClient{
		result: contracts.SandboxExecutionResult{
			ExitCode: 0,
			Stdout:   "ok\n",
			Cwd:      "/workspace",
		},
	}
	executor := &RuntimeToolExecutor{sandbox: sandbox}

	_, err := executor.invokeSandboxBash(
		context.Background(),
		map[string]any{"command": "echo ok", "timeout": 700},
		func() *contracts.ExecutionContext {
			execCtx := sandboxBashExecutionContext(t)
			execCtx.Budget = contracts.Budget{Tool: contracts.RetryPolicy{Timeout: 600}}
			return execCtx
		}(),
	)
	if err != nil {
		t.Fatalf("invokeSandboxBash returned error: %v", err)
	}
	if sandbox.timeout != 600 {
		t.Fatalf("expected sandbox timeout to be capped at 600, got %d", sandbox.timeout)
	}
}
