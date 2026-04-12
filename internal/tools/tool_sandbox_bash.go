package tools

import (
	"context"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeSandboxBash(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	timeoutMs := int64Arg(args, "timeout_ms")
	result, err := t.sandbox.Execute(ctx, execCtx, command, stringArg(args, "cwd"), timeoutMs)
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "sandbox_execute_failed", ExitCode: -1}, nil
	}
	if result.ExitCode == 0 && strings.TrimSpace(result.Stderr) == "" {
		return ToolExecutionResult{Output: result.Stdout, ExitCode: 0}, nil
	}
	payload := map[string]any{
		"exitCode":         result.ExitCode,
		"mode":             "sandbox",
		"workingDirectory": result.WorkingDirectory,
		"stdout":           result.Stdout,
		"stderr":           result.Stderr,
	}
	return structuredResultWithExit(payload, result.ExitCode), nil
}
