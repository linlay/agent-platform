package engine

import (
	"context"
	"strings"

	"agent-platform-runner-go/internal/api"
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
	defs := append([]api.ToolDetailResponse(nil), backend.Definitions()...)
	defs = append(defs, frontendToolDefinitions()...)
	defs = append(defs, extraDefs...)
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
		return r.backend.Invoke(ctx, toolName, args, execCtx)
	}

	kind, _ := def.Meta["kind"].(string)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "frontend":
		return r.frontend.Await(ctx, execCtx)
	case "mcp":
		serverKey, _ := def.Meta["serverKey"].(string)
		if strings.TrimSpace(serverKey) == "" {
			serverKey, _ = def.Meta["sourceKey"].(string)
		}
		payload, err := r.mcp.CallTool(ctx, serverKey, def.Name, args)
		if err != nil {
			return ToolExecutionResult{}, err
		}
		return structuredResult(payload), nil
	case "action":
		if r.action == nil {
			return ToolExecutionResult{Output: "action invoker not configured", Error: "action_not_configured", ExitCode: -1}, nil
		}
		return r.action.Invoke(ctx, def.Name, args, execCtx)
	case "backend":
		fallthrough
	default:
		return r.backend.Invoke(ctx, toolName, args, execCtx)
	}
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
