package tools

import (
	"context"
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
)

type RuntimeToolExecutor struct {
	cfg     config.Config
	sandbox SandboxClient
	memory  memory.Store
	defs    []api.ToolDetailResponse
}

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient, memoryStore memory.Store) (*RuntimeToolExecutor, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if !cfg.ContainerHub.Enabled && strings.EqualFold(strings.TrimSpace(def.Name), "_sandbox_bash_") {
			continue
		}
		filtered = append(filtered, def)
	}
	return &RuntimeToolExecutor{
		cfg:     cfg,
		sandbox: sandbox,
		memory:  memoryStore,
		defs:    filtered,
	}, nil
}

func (t *RuntimeToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), t.defs...)
}

func (t *RuntimeToolExecutor) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	switch strings.TrimSpace(toolName) {
	case "_datetime_":
		return t.invokeDateTime(args), nil
	case "_artifact_publish_":
		return t.invokeArtifactPublish(args, execCtx)
	case "_plan_add_tasks_":
		return t.invokePlanAddTasks(args, execCtx)
	case "_plan_get_tasks_":
		return t.invokePlanGetTasks(execCtx)
	case "_plan_update_task_":
		return t.invokePlanUpdateTask(args, execCtx)
	case "_bash_":
		return t.invokeHostBash(ctx, args)
	case "_sandbox_bash_":
		return t.invokeSandboxBash(ctx, args, execCtx)
	case "_memory_search_", "memory_search":
		return t.invokeMemorySearch(toolName, args, execCtx)
	case "_memory_read_", "memory_read":
		return t.invokeMemoryRead(toolName, args, execCtx)
	case "_memory_write_", "memory_write":
		return t.invokeMemoryWrite(toolName, args, execCtx)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func defaultStringArg(args map[string]any, key string, fallback string) string {
	if value := stringArg(args, key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	return structuredResultWithExit(payload, 0)
}

func bashResult(stdout, stderr, mode, workingDirectory string, exitCode int, hardError string) ToolExecutionResult {
	if exitCode == 0 && strings.TrimSpace(hardError) == "" {
		return ToolExecutionResult{
			Output:   stdout,
			ExitCode: 0,
		}
	}

	payload := map[string]any{
		"exitCode":         exitCode,
		"mode":             mode,
		"workingDirectory": workingDirectory,
		"stdout":           stdout,
		"stderr":           stderr,
	}
	if strings.TrimSpace(hardError) != "" {
		payload["error"] = hardError
	}
	result := structuredResultWithExit(payload, exitCode)
	result.Error = strings.TrimSpace(hardError)
	return result
}

func structuredResultWithExit(payload map[string]any, exitCode int) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   exitCode,
	}
}
