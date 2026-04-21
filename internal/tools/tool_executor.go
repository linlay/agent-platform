package tools

import (
	"context"
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/skills"
)

type RuntimeToolExecutor struct {
	cfg             config.Config
	sandbox         SandboxClient
	chats           chat.Store
	memory          memory.Store
	skillCandidates skills.CandidateStore
	defs            []api.ToolDetailResponse
}

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient, chats chat.Store, memoryStore memory.Store, skillCandidates skills.CandidateStore) (*RuntimeToolExecutor, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if !cfg.ContainerHub.Enabled && strings.EqualFold(strings.TrimSpace(def.Name), "_bash_container_") {
			continue
		}
		filtered = append(filtered, def)
	}
	return &RuntimeToolExecutor{
		cfg:             cfg,
		sandbox:         sandbox,
		chats:           chats,
		memory:          memoryStore,
		skillCandidates: skillCandidates,
		defs:            filtered,
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
		if execCtx != nil && hasSandboxConfig(execCtx.Session) {
			if !t.cfg.ContainerHub.Enabled {
				return ToolExecutionResult{
					Output:   "sandbox execution is required by agent sandboxConfig but container-hub is unavailable",
					Error:    "sandbox_not_available",
					ExitCode: -1,
				}, nil
			}
			return t.invokeSandboxBash(ctx, args, execCtx)
		}
		return t.invokeHostBash(ctx, args, execCtx)
	case "_memory_search_", "memory_search":
		return t.invokeMemorySearch(toolName, args, execCtx)
	case "_memory_read_", "memory_read":
		return t.invokeMemoryRead(toolName, args, execCtx)
	case "_memory_write_", "memory_write":
		return t.invokeMemoryWrite(toolName, args, execCtx)
	case "_memory_update_", "memory_update":
		return t.invokeMemoryUpdate(toolName, args, execCtx)
	case "_memory_forget_", "memory_forget":
		return t.invokeMemoryForget(toolName, args, execCtx)
	case "_memory_timeline_", "memory_timeline":
		return t.invokeMemoryTimeline(toolName, args, execCtx)
	case "_memory_promote_", "memory_promote":
		return t.invokeMemoryPromote(toolName, args, execCtx)
	case "_memory_consolidate_", "memory_consolidate":
		return t.invokeMemoryConsolidate(toolName, args, execCtx)
	case "_session_search_", "session_search":
		return t.invokeSessionSearch(toolName, args, execCtx)
	case "_skill_candidate_write_", "skill_candidate_write":
		return t.invokeSkillCandidateWrite(toolName, args, execCtx)
	case "_skill_candidate_list_", "skill_candidate_list":
		return t.invokeSkillCandidateList(toolName, args, execCtx)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func hasSandboxConfig(session QuerySession) bool {
	return session.AgentHasSandboxConfig
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
