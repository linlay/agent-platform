package tools

import (
	"context"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/observability"
)

type toolCatalog interface {
	Definitions() []api.ToolDetailResponse
	Tool(name string) (api.ToolDetailResponse, bool)
}

type frontendSubmitter interface {
	Await(ctx context.Context, execCtx *ExecutionContext, args map[string]any) (ToolExecutionResult, error)
}

type ToolRouter struct {
	mu          sync.RWMutex
	backend     ToolExecutor
	mcp         McpClient
	mcpTools    toolCatalog
	frontend    frontendSubmitter
	action      ActionInvoker
	external    ExternalToolInvoker
	localDefs   []api.ToolDetailResponse
	localByName map[string]api.ToolDetailResponse
}

func NewToolRouter(backend ToolExecutor, mcp McpClient, mcpTools toolCatalog, frontend frontendSubmitter, action ActionInvoker, extraDefs ...api.ToolDetailResponse) *ToolRouter {
	localDefs, localByName := buildLocalToolDefinitions(backend.Definitions(), extraDefs)
	return &ToolRouter{
		backend:     backend,
		mcp:         mcp,
		mcpTools:    mcpTools,
		frontend:    frontend,
		action:      action,
		localDefs:   localDefs,
		localByName: localByName,
	}
}

func (r *ToolRouter) WithExternalInvoker(external ExternalToolInvoker) *ToolRouter {
	if r == nil {
		return r
	}
	r.external = external
	if external != nil {
		external.Configure(r.Definitions())
	}
	return r
}

func buildLocalToolDefinitions(base []api.ToolDetailResponse, extraDefs []api.ToolDetailResponse) ([]api.ToolDetailResponse, map[string]api.ToolDetailResponse) {
	baseDefs := append([]api.ToolDetailResponse(nil), base...)
	var runtimeDefs []api.ToolDetailResponse
	for _, def := range extraDefs {
		kind, _ := def.Meta["kind"].(string)
		if strings.EqualFold(kind, "mcp") {
			continue
		}
		runtimeDefs = append(runtimeDefs, def)
	}
	localDefs := MergeToolDefinitions(baseDefs, runtimeDefs, nil)
	localByName := make(map[string]api.ToolDetailResponse, len(localDefs)*2)
	for _, def := range localDefs {
		localByName[strings.ToLower(strings.TrimSpace(def.Name))] = def
		localByName[strings.ToLower(strings.TrimSpace(def.Key))] = def
	}
	return localDefs, localByName
}

func (r *ToolRouter) ReloadRuntimeToolDefinitions(root string) error {
	if r == nil {
		return nil
	}
	runtimeDefs, err := LoadRuntimeToolDefinitions(root)
	if err != nil {
		return err
	}
	baseDefs := r.backend.Definitions()
	localDefs, localByName := buildLocalToolDefinitions(baseDefs, runtimeDefs)
	r.mu.Lock()
	r.localDefs = localDefs
	r.localByName = localByName
	external := r.external
	r.mu.Unlock()
	if external != nil {
		external.Configure(localDefs)
	}
	return nil
}

func (r *ToolRouter) Definitions() []api.ToolDetailResponse {
	mcpDefs := []api.ToolDetailResponse(nil)
	if r.mcpTools != nil {
		mcpDefs = r.mcpTools.Definitions()
	}
	r.mu.RLock()
	localDefs := append([]api.ToolDetailResponse(nil), r.localDefs...)
	r.mu.RUnlock()
	return MergeToolDefinitions(localDefs, nil, mcpDefs)
}

func (r *ToolRouter) ReadFileHistory(chatID string, runID string, filePath string, version string) (string, error) {
	if r == nil {
		return "", ErrNotImplemented
	}
	reader, ok := r.backend.(FileHistoryReader)
	if !ok {
		return "", ErrNotImplemented
	}
	return reader.ReadFileHistory(chatID, runID, filePath, version)
}

func (r *ToolRouter) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	def, ok := r.lookup(toolName)
	if !ok {
		return r.invokeWithPolicy(ctx, toolName, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
			return r.backend.Invoke(callCtx, toolName, args, execCtx)
		})
	}

	sourceType, _ := def.Meta["sourceType"].(string)
	kind, _ := def.Meta["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(sourceType), "mcp") && strings.EqualFold(strings.TrimSpace(kind), "frontend") {
		return r.invokeFrontendWithPolicy(ctx, def.Name, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
			if r.frontend == nil {
				return ToolExecutionResult{Output: "frontend submitter not configured", Error: "frontend_not_configured", ExitCode: -1}, nil
			}
			return r.frontend.Await(callCtx, execCtx, args)
		})
	}
	return r.invokeWithPolicy(ctx, def.Name, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
		if strings.EqualFold(strings.TrimSpace(sourceType), "mcp") {
			return r.invokeMCPTool(callCtx, def, args, execCtx), nil
		}
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "action":
			if r.action == nil {
				return ToolExecutionResult{Output: "action invoker not configured", Error: "action_not_configured", ExitCode: -1}, nil
			}
			return r.action.Invoke(callCtx, def.Name, args, execCtx)
		case "external":
			if r.external == nil {
				return ToolExecutionResult{Output: "external tool invoker not configured", Error: "external_not_configured", ExitCode: -1}, nil
			}
			return r.external.Invoke(callCtx, def, args, execCtx)
		case "backend":
			fallthrough
		default:
			return r.backend.Invoke(callCtx, toolName, args, execCtx)
		}
	})
}

func (r *ToolRouter) Tool(toolName string) (api.ToolDetailResponse, bool) {
	return r.lookup(toolName)
}

func (r *ToolRouter) lookup(toolName string) (api.ToolDetailResponse, bool) {
	if r == nil {
		return api.ToolDetailResponse{}, false
	}
	normalized := strings.ToLower(strings.TrimSpace(toolName))
	r.mu.RLock()
	if def, ok := r.localByName[normalized]; ok {
		r.mu.RUnlock()
		return def, true
	}
	r.mu.RUnlock()
	if r.mcpTools != nil {
		return r.mcpTools.Tool(toolName)
	}
	return api.ToolDetailResponse{}, false
}

func (r *ToolRouter) invokeMCPTool(ctx context.Context, def api.ToolDetailResponse, args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	if r.mcp == nil {
		return mcpErrorResult(def.Name, "mcp_disabled", "MCP is disabled")
	}
	serverKey, _ := def.Meta["serverKey"].(string)
	if strings.TrimSpace(serverKey) == "" {
		serverKey, _ = def.Meta["sourceKey"].(string)
	}
	if strings.TrimSpace(serverKey) == "" {
		return mcpErrorResult(def.Name, "mcp_source_key_missing", "MCP server key is missing")
	}
	payload, err := r.mcp.CallTool(ctx, serverKey, def.Name, args, buildMCPMeta(def.Name, execCtx))
	if err != nil {
		return mcpErrorResult(def.Name, "mcp_server_unavailable", err.Error())
	}
	return normalizeMCPResult(def.Name, payload)
}

func buildMCPMeta(toolName string, execCtx *ExecutionContext) map[string]any {
	if execCtx == nil {
		return nil
	}
	meta := map[string]any{}
	if value := strings.TrimSpace(execCtx.Request.ChatID); value != "" {
		meta["chatId"] = value
	}
	if value := strings.TrimSpace(execCtx.Request.RequestID); value != "" {
		meta["requestId"] = value
	}
	if value := strings.TrimSpace(execCtx.Request.RunID); value != "" {
		meta["runId"] = value
	}
	if value := strings.TrimSpace(toolName); value != "" {
		meta["toolName"] = value
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func normalizeMCPResult(toolName string, payload any) ToolExecutionResult {
	if payload == nil {
		return mcpErrorResult(toolName, "mcp_empty_result", "MCP result is empty")
	}
	if mapped, ok := payload.(map[string]any); ok {
		if isError, _ := mapped["isError"].(bool); isError {
			return mcpErrorResult(toolName, "mcp_tool_error", extractMCPErrorMessage(mapped))
		}
		if structured, ok := mapped["structuredContent"]; ok && structured != nil {
			return payloadToToolResult(structured)
		}
		if items, ok := mapped["content"].([]any); ok && len(items) > 0 {
			first := items[0]
			if contentMap, ok := first.(map[string]any); ok {
				if strings.EqualFold(strings.TrimSpace(AnyStringNode(contentMap["type"])), "text") {
					return ToolExecutionResult{Output: AnyStringNode(contentMap["text"]), ExitCode: 0}
				}
				return payloadToToolResult(contentMap)
			}
			return payloadToToolResult(first)
		}
		return payloadToToolResult(mapped)
	}
	return payloadToToolResult(payload)
}

func payloadToToolResult(payload any) ToolExecutionResult {
	switch value := payload.(type) {
	case map[string]any:
		return structuredResult(value)
	case string:
		return ToolExecutionResult{Output: value, ExitCode: 0}
	default:
		return ToolExecutionResult{Output: MarshalJSON(payload), ExitCode: 0}
	}
}

func extractMCPErrorMessage(payload map[string]any) string {
	if message := AnyStringNode(payload["error"]); strings.TrimSpace(message) != "" {
		return message
	}
	if items, ok := payload["content"].([]any); ok && len(items) > 0 {
		if contentMap, ok := items[0].(map[string]any); ok {
			if text := AnyStringNode(contentMap["text"]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return "MCP tool call failed"
}

func mcpErrorResult(toolName string, code string, message string) ToolExecutionResult {
	payload := map[string]any{
		"tool":  toolName,
		"ok":    false,
		"code":  code,
		"error": message,
	}
	return ToolExecutionResult{
		Output:     MarshalJSON(payload),
		Structured: payload,
		Error:      code,
		ExitCode:   -1,
	}
}

func (r *ToolRouter) invokeWithPolicy(ctx context.Context, toolName string, execCtx *ExecutionContext, invoke func(context.Context) (ToolExecutionResult, error)) (ToolExecutionResult, error) {
	budget := Budget{}
	if execCtx != nil {
		budget = NormalizeBudget(execCtx.Budget)
		if result, exceeded := toolCallsExceededResult(execCtx, budget, toolName); exceeded {
			return result, nil
		}
	}
	retryCount := 0
	timeout := 30 * time.Second
	if budget.Tool.Timeout > 0 {
		timeout = toolTimeout(budget.Tool)
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

func (r *ToolRouter) invokeFrontendWithPolicy(ctx context.Context, toolName string, execCtx *ExecutionContext, invoke func(context.Context) (ToolExecutionResult, error)) (ToolExecutionResult, error) {
	if execCtx != nil {
		budget := NormalizeBudget(execCtx.Budget)
		if result, exceeded := toolCallsExceededResult(execCtx, budget, toolName); exceeded {
			return result, nil
		}
	}
	result, err := invoke(ctx)
	if err == nil {
		observability.LogToolInvocation(toolName, "ok", map[string]any{
			"attempt":  1,
			"exitCode": result.ExitCode,
			"error":    result.Error,
		})
		return result, nil
	}
	observability.LogToolInvocation(toolName, "error", map[string]any{
		"attempt": 1,
		"error":   observability.SanitizeLog(err.Error()),
	})
	return ToolExecutionResult{}, err
}

func toolCallsExceededResult(execCtx *ExecutionContext, budget Budget, toolName string) (ToolExecutionResult, bool) {
	if execCtx == nil || budget.Tool.MaxCalls <= 0 || execCtx.ToolCalls <= budget.Tool.MaxCalls {
		return ToolExecutionResult{}, false
	}
	payload := NewErrorPayload(
		"tool_calls_exceeded",
		"tool call budget exceeded",
		ErrorScopeTool,
		ErrorCategoryTool,
		map[string]any{
			"toolCalls":  execCtx.ToolCalls,
			"limitValue": budget.Tool.MaxCalls,
			"limitName":  "budget.tool.maxCalls",
			"toolName":   toolName,
		},
	)
	return ToolExecutionResult{
		Output:     MarshalJSON(payload),
		Structured: payload,
		Error:      "tool_calls_exceeded",
		ExitCode:   -1,
	}, true
}
