package engine

import (
	"context"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/observability"
)

type ToolRouter struct {
	backend   ToolExecutor
	mcp       McpClient
	frontend  *FrontendSubmitCoordinator
	action    ActionInvoker
	defs      []api.ToolDetailResponse
	defByName map[string]api.ToolDetailResponse
}

func NewToolRouter(backend ToolExecutor, mcp McpClient, frontend *FrontendSubmitCoordinator, action ActionInvoker, extraDefs ...api.ToolDetailResponse) *ToolRouter {
	baseDefs := append([]api.ToolDetailResponse(nil), backend.Definitions()...)
	baseDefs = append(baseDefs, frontendToolDefinitions()...)
	var runtimeDefs []api.ToolDetailResponse
	var mcpDefs []api.ToolDetailResponse
	for _, def := range extraDefs {
		kind, _ := def.Meta["kind"].(string)
		if strings.EqualFold(kind, "mcp") {
			mcpDefs = append(mcpDefs, def)
			continue
		}
		runtimeDefs = append(runtimeDefs, def)
	}
	defs := MergeToolDefinitions(baseDefs, runtimeDefs, mcpDefs)
	defByName := make(map[string]api.ToolDetailResponse, len(defs)*2)
	for _, def := range defs {
		defByName[strings.ToLower(strings.TrimSpace(def.Name))] = def
		defByName[strings.ToLower(strings.TrimSpace(def.Key))] = def
	}
	return &ToolRouter{
		backend:   backend,
		mcp:       mcp,
		frontend:  frontend,
		action:    action,
		defs:      defs,
		defByName: defByName,
	}
}

func (r *ToolRouter) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), r.defs...)
}

func (r *ToolRouter) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	def, ok := r.lookup(toolName)
	if !ok {
		return r.invokeWithPolicy(ctx, toolName, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
			return r.backend.Invoke(callCtx, toolName, args, execCtx)
		})
	}

	kind, _ := def.Meta["kind"].(string)
	return r.invokeWithPolicy(ctx, def.Name, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "frontend":
			return r.frontend.Await(callCtx, execCtx)
		case "mcp":
			serverKey, _ := def.Meta["serverKey"].(string)
			if strings.TrimSpace(serverKey) == "" {
				serverKey, _ = def.Meta["sourceKey"].(string)
			}
			payload, err := r.mcp.CallTool(callCtx, serverKey, def.Name, args)
			if err != nil {
				return ToolExecutionResult{}, err
			}
			return structuredResult(payload), nil
		case "action":
			if r.action == nil {
				return ToolExecutionResult{Output: "action invoker not configured", Error: "action_not_configured", ExitCode: -1}, nil
			}
			return r.action.Invoke(callCtx, def.Name, args, execCtx)
		case "backend":
			fallthrough
		default:
			return r.backend.Invoke(callCtx, toolName, args, execCtx)
		}
	})
}

func (r *ToolRouter) lookup(toolName string) (api.ToolDetailResponse, bool) {
	if r == nil {
		return api.ToolDetailResponse{}, false
	}
	def, ok := r.defByName[strings.ToLower(strings.TrimSpace(toolName))]
	return def, ok
}

func frontendToolDefinitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{
		{
			Key:         "confirm_dialog",
			Name:        "confirm_dialog",
			Label:       "确认对话框",
			Description: "展示确认对话框并等待用户提交",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string"},
				},
				"required": []string{"question"},
			},
			Meta: map[string]any{
				"kind":        "frontend",
				"toolType":    "html",
				"viewportKey": "confirm_dialog",
				"strict":      true,
				"sourceType":  "local",
				"sourceKey":   "confirm_dialog",
			},
		},
	}
}

func (r *ToolRouter) invokeWithPolicy(ctx context.Context, toolName string, execCtx *ExecutionContext, invoke func(context.Context) (ToolExecutionResult, error)) (ToolExecutionResult, error) {
	budget := Budget{}
	if execCtx != nil {
		budget = normalizeBudget(execCtx.Budget)
		if budget.Tool.MaxCalls > 0 && execCtx.ToolCalls > budget.Tool.MaxCalls {
			return ToolExecutionResult{
				Output: marshalJSON(NewErrorPayload(
					"tool_calls_exceeded",
					"tool call budget exceeded",
					ErrorScopeTool,
					ErrorCategoryTool,
					map[string]any{
						"toolCalls":  execCtx.ToolCalls,
						"limitValue": budget.Tool.MaxCalls,
						"limitName":  "tool.maxCalls",
						"toolName":   toolName,
					},
				)),
				Structured: NewErrorPayload(
					"tool_calls_exceeded",
					"tool call budget exceeded",
					ErrorScopeTool,
					ErrorCategoryTool,
					map[string]any{
						"toolCalls":  execCtx.ToolCalls,
						"limitValue": budget.Tool.MaxCalls,
						"limitName":  "tool.maxCalls",
						"toolName":   toolName,
					},
				),
				Error:    "tool_calls_exceeded",
				ExitCode: -1,
			}, nil
		}
	}
	retryCount := 0
	timeout := 30 * time.Second
	if budget.Tool.TimeoutMs > 0 {
		timeout = budget.Tool.Timeout()
	}
	if budget.Tool.RetryCount > 0 {
		retryCount = budget.Tool.RetryCount
	}
	var lastErr error
	for attempt := 0; attempt <= retryCount; attempt++ {
		callCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		result, err := invoke(callCtx)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			observability.LogToolInvocation(toolName, "ok", map[string]any{
				"attempt":  attempt + 1,
				"exitCode": result.ExitCode,
				"error":    result.Error,
			})
			return result, nil
		}
		observability.LogToolInvocation(toolName, "error", map[string]any{
			"attempt": attempt + 1,
			"error":   observability.SanitizeLog(err.Error()),
		})
		lastErr = err
	}
	return ToolExecutionResult{}, lastErr
}
