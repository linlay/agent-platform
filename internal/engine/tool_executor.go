package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

type RuntimeToolExecutor struct {
	cfg     config.Config
	sandbox SandboxClient
	defs    []api.ToolDetailResponse
}

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient) *RuntimeToolExecutor {
	defs := []api.ToolDetailResponse{
		bashToolDefinition(),
		dateTimeToolDefinition(),
		artifactPublishToolDefinition(),
	}
	if cfg.ContainerHub.Enabled {
		defs = append(defs, sandboxBashToolDefinition())
	}
	return &RuntimeToolExecutor{
		cfg:     cfg,
		sandbox: sandbox,
		defs:    defs,
	}
}

func (t *RuntimeToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), t.defs...)
}

func (t *RuntimeToolExecutor) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	switch strings.TrimSpace(toolName) {
	case "_datetime_":
		return t.invokeDateTime(), nil
	case "_artifact_publish_":
		return t.invokeArtifactPublish(args)
	case "_bash_":
		return t.invokeHostBash(ctx, args)
	case "_sandbox_bash_":
		return t.invokeSandboxBash(ctx, args, execCtx)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func (t *RuntimeToolExecutor) invokeDateTime() ToolExecutionResult {
	now := time.Now()
	payload := map[string]any{
		"iso8601": now.Format(time.RFC3339),
		"unixMs":  now.UnixMilli(),
		"zone":    now.Location().String(),
	}
	return structuredResult(payload)
}

func (t *RuntimeToolExecutor) invokeArtifactPublish(args map[string]any) (ToolExecutionResult, error) {
	artifacts, _ := args["artifacts"]
	payload := map[string]any{
		"status":    "published",
		"artifacts": artifacts,
	}
	return structuredResult(payload), nil
}

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
	payload := map[string]any{
		"exitCode":         result.ExitCode,
		"mode":             "sandbox",
		"workingDirectory": result.WorkingDirectory,
		"stdout":           result.Stdout,
		"stderr":           result.Stderr,
	}
	return structuredResultWithExit(payload, result.ExitCode), nil
}

func (t *RuntimeToolExecutor) invokeHostBash(ctx context.Context, args map[string]any) (ToolExecutionResult, error) {
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return ToolExecutionResult{Output: "Missing argument: command", Error: "missing_command", ExitCode: -1}, nil
	}
	if len(command) > maxInt(t.cfg.Bash.MaxCommandChars, 16000) {
		return ToolExecutionResult{Output: "Command is too long", Error: "command_too_long", ExitCode: -1}, nil
	}
	if len(t.cfg.Bash.AllowedCommands) == 0 {
		return ToolExecutionResult{Output: "Bash command whitelist is empty", Error: "command_whitelist_empty", ExitCode: -1}, nil
	}
	if !t.cfg.Bash.ShellFeaturesEnabled {
		if err := validateStrictCommand(command, t.cfg.Bash); err != nil {
			return ToolExecutionResult{Output: err.Error(), Error: "command_not_allowed", ExitCode: -1}, nil
		}
	}

	timeout := time.Duration(maxInt(t.cfg.Bash.ShellTimeoutMs, 30000)) * time.Millisecond
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellExecutable := strings.TrimSpace(t.cfg.Bash.ShellExecutable)
	if shellExecutable == "" {
		shellExecutable = "bash"
	}
	cmd := exec.CommandContext(runCtx, shellExecutable, "-lc", command)
	workingDir := t.cfg.Bash.WorkingDirectory
	if workingDir == "" {
		workingDir = "."
	}
	cmd.Dir = workingDir
	output, err := cmd.CombinedOutput()
	exitCode := 0
	stderr := ""
	stdout := string(output)
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		stderr = err.Error()
		if runCtx.Err() == context.DeadlineExceeded {
			stderr = "Command timed out"
		}
	}
	payload := map[string]any{
		"exitCode":         exitCode,
		"mode":             "host",
		"workingDirectory": workingDir,
		"stdout":           stdout,
		"stderr":           stderr,
	}
	return structuredResultWithExit(payload, exitCode), nil
}

func validateStrictCommand(command string, cfg config.BashConfig) error {
	if strings.ContainsAny(command, "\n;&|<>(){}") {
		return fmt.Errorf("Unsupported syntax for _bash_")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("Cannot parse command")
	}
	base := fields[0]
	if !containsString(cfg.AllowedCommands, base) {
		return fmt.Errorf("Command not allowed: %s", base)
	}
	if !containsString(cfg.PathCheckedCommands, base) || containsString(cfg.PathCheckBypassCommands, base) {
		return nil
	}
	workingDirectory := cfg.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		if !looksLikePathArg(field) {
			continue
		}
		resolved := field
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workingDirectory, resolved)
		}
		resolved = filepath.Clean(resolved)
		if !pathAllowed(resolved, cfg.AllowedPaths, workingDirectory) {
			return fmt.Errorf("Path not allowed: %s", field)
		}
	}
	return nil
}

func looksLikePathArg(arg string) bool {
	return strings.Contains(arg, "/") || strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "~")
}

func pathAllowed(resolved string, allowed []string, workingDirectory string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, root := range allowed {
		if root == "" {
			continue
		}
		checkRoot := root
		if !filepath.IsAbs(checkRoot) {
			checkRoot = filepath.Join(workingDirectory, checkRoot)
		}
		checkRoot = filepath.Clean(checkRoot)
		if resolved == checkRoot || strings.HasPrefix(resolved, checkRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	default:
		return 0
	}
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	return structuredResultWithExit(payload, 0)
}

func structuredResultWithExit(payload map[string]any, exitCode int) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   exitCode,
	}
}

func dateTimeToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_datetime_",
		Name:        "_datetime_",
		Label:       "日期时间",
		Description: "获取当前或偏移后的日期时间",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_datetime_"},
	}
}

func artifactPublishToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_artifact_publish_",
		Name:        "_artifact_publish_",
		Label:       "发布产物",
		Description: "发布当前运行中生成的产物",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifacts": map[string]any{"type": "array"},
			},
			"required": []string{"artifacts"},
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_artifact_publish_"},
	}
}

func bashToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_bash_",
		Name:        "_bash_",
		Label:       "执行命令（宿主机）",
		Description: "运行白名单 bash 命令（默认严格模式；可配置启用高级 shell 语法，如管道、重定向与 here-doc）",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_bash_"},
	}
}

func sandboxBashToolDefinition() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "_sandbox_bash_",
		Name:        "_sandbox_bash_",
		Label:       "执行命令",
		Description: "在沙箱容器中执行命令。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string"},
				"cwd":        map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Meta: map[string]any{"kind": "backend", "strict": true, "sourceType": "local", "sourceKey": "_sandbox_bash_"},
	}
}
