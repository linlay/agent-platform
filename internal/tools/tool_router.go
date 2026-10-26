package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/observability"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/toolargs"
	"agent-platform/internal/toolpolicy"
)

type toolCatalog interface {
	Definitions() []api.ToolDetailResponse
	Tool(name string) (api.ToolDetailResponse, bool)
}

type mcpServerToolCatalog interface {
	ToolNamesForServers(serverKeys []string) []string
}

type interactionSubmitter interface {
	Handles(toolName string) bool
	Await(ctx context.Context, execCtx *ExecutionContext, args map[string]any) (ToolExecutionResult, error)
}

type ToolRouter struct {
	mu          sync.RWMutex
	runtime     ToolExecutor
	mcp         McpClient
	mcpTools    toolCatalog
	interaction interactionSubmitter
	localDefs   []api.ToolDetailResponse
	localByName map[string]api.ToolDetailResponse
	handlers    map[string]NamedToolHandler
}

func NewToolRouter(runtime ToolExecutor, mcp McpClient, mcpTools toolCatalog, interaction interactionSubmitter, extraDefs ...api.ToolDetailResponse) (*ToolRouter, error) {
	localDefs, localByName, err := buildLocalToolDefinitions(runtime.Definitions(), extraDefs)
	if err != nil {
		return nil, err
	}
	if err := validateRequiredInteractionHandler(localDefs, interaction); err != nil {
		return nil, err
	}
	return &ToolRouter{
		runtime:     runtime,
		mcp:         mcp,
		mcpTools:    mcpTools,
		interaction: interaction,
		localDefs:   localDefs,
		localByName: localByName,
		handlers:    map[string]NamedToolHandler{},
	}, nil
}

// RegisterHandler atomically assigns every normalized tool name advertised by
// handler. Definitions remain owned by the catalog; handlers only replace the
// implementation for already-defined local tools.
func (r *ToolRouter) RegisterHandler(handler NamedToolHandler) error {
	if r == nil {
		return fmt.Errorf("register named tool handler: router is nil")
	}
	if handler == nil {
		return fmt.Errorf("register named tool handler: handler is nil")
	}
	names := handler.ToolNames()
	if len(names) == 0 {
		return fmt.Errorf("register named tool handler: tool names must not be empty")
	}

	normalizedNames := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		requestedName := normalizeNamedToolName(name)
		if requestedName == "" {
			return fmt.Errorf("register named tool handler: tool name must not be blank")
		}
		def, exists := r.localByName[requestedName]
		if !exists {
			return fmt.Errorf("register named tool handler: tool %q is not defined", requestedName)
		}
		canonicalName := normalizeNamedToolName(def.Name)
		if canonicalName == "" {
			canonicalName = requestedName
		}
		if _, exists := seen[canonicalName]; exists {
			return fmt.Errorf("register named tool handler: duplicate tool name %q", canonicalName)
		}
		seen[canonicalName] = struct{}{}
		if _, exists := r.handlers[canonicalName]; exists {
			return fmt.Errorf("register named tool handler: tool %q already has a handler", canonicalName)
		}
		normalizedNames = append(normalizedNames, canonicalName)
	}
	for _, name := range normalizedNames {
		r.handlers[name] = handler
	}
	return nil
}

func buildLocalToolDefinitions(base []api.ToolDetailResponse, extraDefs []api.ToolDetailResponse) ([]api.ToolDetailResponse, map[string]api.ToolDetailResponse, error) {
	baseDefs := append([]api.ToolDetailResponse(nil), base...)
	implemented := make(map[string]struct{}, len(baseDefs)*2)
	for _, def := range baseDefs {
		if name := normalizeToolName(def); name != "" {
			implemented[name] = struct{}{}
		}
	}
	runtimeDefs := make([]api.ToolDetailResponse, 0, len(extraDefs))
	for _, def := range extraDefs {
		if strings.EqualFold(strings.TrimSpace(AnyStringNode(def.Meta["sourceType"])), "mcp") {
			continue
		}
		name := normalizeToolName(def)
		if _, ok := implemented[name]; !ok {
			return nil, nil, fmt.Errorf("runtime tool %q has no registered implementation; add a Go handler or expose it through MCP", def.Name)
		}
		runtimeDefs = append(runtimeDefs, def)
	}
	localDefs := MergeToolDefinitions(baseDefs, runtimeDefs, nil)
	localByName := make(map[string]api.ToolDetailResponse, len(localDefs)*2)
	for _, def := range localDefs {
		localByName[strings.ToLower(strings.TrimSpace(def.Name))] = def
		localByName[strings.ToLower(strings.TrimSpace(def.Key))] = def
	}
	return localDefs, localByName, nil
}

func (r *ToolRouter) ReloadRuntimeToolDefinitions(root string) error {
	if r == nil {
		return nil
	}
	runtimeDefs, err := LoadRuntimeToolDefinitions(root)
	if err != nil {
		return err
	}
	baseDefs := r.runtime.Definitions()
	localDefs, localByName, err := buildLocalToolDefinitions(baseDefs, runtimeDefs)
	if err != nil {
		return err
	}
	if err := validateRequiredInteractionHandler(localDefs, r.interaction); err != nil {
		return err
	}
	r.mu.Lock()
	r.localDefs = localDefs
	r.localByName = localByName
	r.mu.Unlock()
	return nil
}

func validateRequiredInteractionHandler(defs []api.ToolDetailResponse, interaction interactionSubmitter) error {
	for _, def := range defs {
		if name := strings.ToLower(strings.TrimSpace(def.Name)); name != "ask_user_question" && name != "ask_user_form" {
			continue
		}
		if interaction == nil || !interaction.Handles(def.Name) {
			return fmt.Errorf("tool %s requires a registered interaction handler", def.Name)
		}
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

// MCPToolNamesForServers resolves an Agent's explicit MCP server allowlist to
// synchronized tool names without allowing MCP/local name collisions to grant
// an unrelated local tool.
func (r *ToolRouter) MCPToolNamesForServers(serverKeys []string) []string {
	if r == nil || r.mcpTools == nil || len(serverKeys) == 0 {
		return nil
	}
	catalog, ok := r.mcpTools.(mcpServerToolCatalog)
	if !ok {
		return nil
	}
	names := catalog.ToolNamesForServers(serverKeys)
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, conflictsWithLocal := r.localByName[strings.ToLower(name)]; conflictsWithLocal {
			continue
		}
		result = append(result, name)
	}
	return result
}

func (r *ToolRouter) ReadFileHistory(chatID string, runID string, filePath string, version string) (string, error) {
	if r == nil {
		return "", ErrNotImplemented
	}
	reader, ok := r.runtime.(FileHistoryReader)
	if !ok {
		return "", ErrNotImplemented
	}
	return reader.ReadFileHistory(chatID, runID, filePath, version)
}

func (r *ToolRouter) ListFileHistory(chatID string, runID string) ([]FileHistoryRecord, error) {
	if r == nil || r.runtime == nil {
		return nil, ErrNotImplemented
	}
	reader, ok := r.runtime.(ProjectFileHistoryReader)
	if !ok {
		return nil, ErrNotImplemented
	}
	return reader.ListFileHistory(chatID, runID)
}

func (r *ToolRouter) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	def, ok := r.lookup(toolName)
	if owner, native := connector.NativeToolConnector(toolName); native {
		if execCtx == nil || execCtx.Session.NativeConnectorTools[toolName] != owner || execCtx.Session.ConnectorDirs[owner] == "" {
			return ToolExecutionResult{Error: "connector_not_mounted", Output: "tool requires mounted " + owner, ExitCode: -1}, nil
		}
	}
	if execCtx != nil && (!execCtx.Session.AllowsTool(toolName) || (ok && !execCtx.Session.AllowsTool(def.Name))) {
		return ToolNotMountedResult(toolName), nil
	}
	if ok && !strings.EqualFold(AnyStringNode(def.Meta["sourceType"]), "mcp") {
		if err := toolargs.RejectLegacy(def.Name, args); err != nil {
			return ToolExecutionResult{Error: "invalid_tool_arguments", Output: err.Error(), ExitCode: -1}, nil
		}
	}
	if execCtx != nil && IsReadOnlyToolExecutionPolicy(execCtx.ToolExecutionPolicy) && !r.allowsReadOnlyInvocation(def, ok, toolName, args, execCtx) {
		return toolpolicy.DisabledResult(toolName), nil
	}
	if !ok {
		return ToolExecutionResult{
			Output:   "tool not registered: " + strings.TrimSpace(toolName),
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}

	sourceType, _ := def.Meta["sourceType"].(string)
	if !strings.EqualFold(strings.TrimSpace(sourceType), "mcp") && r.interaction != nil && r.interaction.Handles(def.Name) {
		result, err := r.invokeInteractionWithPolicy(ctx, def.Name, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
			return r.interaction.Await(callCtx, execCtx, args)
		})
		return validateDeclaredOutputSchema(def, result, err)
	}
	result, err := r.invokeWithPolicy(ctx, def.Name, execCtx, func(callCtx context.Context) (ToolExecutionResult, error) {
		if strings.EqualFold(strings.TrimSpace(sourceType), "mcp") {
			return r.invokeMCPTool(callCtx, def, args, execCtx), nil
		}
		switch strings.ToLower(strings.TrimSpace(def.Name)) {
		case MCPSearchToolName:
			return r.invokeMCPSearch(args, execCtx), nil
		case MCPCallToolName:
			return r.invokeMCPCall(callCtx, args, execCtx), nil
		case ViewDescribeToolName:
			return r.invokeViewDescribe(args, execCtx), nil
		}
		if handler := r.namedHandler(def.Name); handler != nil {
			return handler.Invoke(callCtx, def.Name, args, execCtx)
		}
		return r.runtime.Invoke(callCtx, toolName, args, execCtx)
	})
	return validateDeclaredOutputSchema(def, result, err)
}

func (r *ToolRouter) SupportsToolOutput(toolName string, execCtx *ExecutionContext) bool {
	if r == nil || r.runtime == nil {
		return false
	}
	def, ok := r.lookup(toolName)
	if !ok || strings.EqualFold(strings.TrimSpace(AnyStringNode(def.Meta["sourceType"])), "mcp") {
		return false
	}
	if r.interaction != nil && r.interaction.Handles(def.Name) {
		return false
	}
	if r.namedHandler(def.Name) != nil {
		return false
	}
	support, ok := r.runtime.(ToolOutputStreamingExecutor)
	return ok && support.SupportsToolOutput(def.Name, execCtx)
}

// allowsReadOnlyInvocation judges an mcp_call by the MCP tool it reaches.
// An unresolvable target is left to the gateway, which reports why.
func (r *ToolRouter) allowsReadOnlyInvocation(def api.ToolDetailResponse, found bool, toolName string, args map[string]any, execCtx *ExecutionContext) bool {
	if found && strings.EqualFold(strings.TrimSpace(def.Name), MCPCallToolName) {
		target, resolved := r.ResolveInvocationTarget(def.Name, args, execCtx)
		return !resolved || toolpolicy.AllowsReadOnly(target.Definition, true)
	}
	return allowsReadOnlyInvocation(def, found, toolName, args)
}

func allowsReadOnlyInvocation(def api.ToolDetailResponse, found bool, toolName string, args map[string]any) bool {
	if toolpolicy.OperationAware(toolName) || toolpolicy.OperationAware(def.Name) {
		descriptor, ok := toolpolicy.InvocationDescriptor(def.Name, args)
		return found && ok && descriptor.ReadOnly
	}
	return toolpolicy.AllowsReadOnly(def, found)
}

// validateDeclaredOutputSchema applies only a tool's explicit result schema.
// In particular, results without outputSchema are opaque and must not be
// inspected merely because a business field happens to be named createdAt.
func validateDeclaredOutputSchema(def api.ToolDetailResponse, result ToolExecutionResult, invokeErr error) (ToolExecutionResult, error) {
	if invokeErr != nil || len(def.OutputSchema) == 0 || result.Error != "" {
		return result, invokeErr
	}
	if err := timecontract.ValidateOutputSchema(result.Structured, def.OutputSchema, "tool.result."+strings.TrimSpace(def.Name)); err != nil {
		return ToolExecutionResult{
			Output:    "time contract violation",
			MCPResult: result.MCPResult,
			Error:     "time_contract_violation",
			ExitCode:  -1,
		}, err
	}
	return result, nil
}

func (r *ToolRouter) namedHandler(toolName string) NamedToolHandler {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	handler := r.handlers[normalizeNamedToolName(toolName)]
	r.mu.RUnlock()
	return handler
}

func normalizeNamedToolName(toolName string) string {
	return strings.ToLower(strings.TrimSpace(toolName))
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

type mcpCallError struct{ code, message string }

func (e *mcpCallError) Error() string { return e.code + ": " + e.message }

func (r *ToolRouter) invokeMCPTool(ctx context.Context, def api.ToolDetailResponse, args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	payload, err := r.callMCPTool(ctx, def, args, execCtx)
	if err != nil {
		var callErr *mcpCallError
		if errors.As(err, &callErr) {
			return mcpErrorResult(def.Name, callErr.code, callErr.message)
		}
		return mcpErrorResult(def.Name, "mcp_server_unavailable", err.Error())
	}
	return normalizeMCPResult(def.Name, payload)
}

// callMCPTool sends tools/call for an MCP definition after checking that the
// Agent-scoped instance belongs to the caller. Direct MCP tools and mcp_call
// share it.
func (r *ToolRouter) callMCPTool(ctx context.Context, def api.ToolDetailResponse, args map[string]any, execCtx *ExecutionContext) (any, error) {
	if r.mcp == nil {
		return nil, &mcpCallError{"mcp_disabled", "MCP is disabled"}
	}
	serverKey, _ := def.Meta["serverKey"].(string)
	if strings.TrimSpace(serverKey) == "" {
		serverKey, _ = def.Meta["sourceKey"].(string)
	}
	if strings.TrimSpace(serverKey) == "" {
		return nil, &mcpCallError{"mcp_source_key_missing", "MCP server key is missing"}
	}
	if agentKey := AnyStringNode(def.Meta["agentKey"]); agentKey != "" {
		if execCtx == nil || execCtx.Session.AgentKey != agentKey {
			return nil, &mcpCallError{"mcp_agent_mismatch", "MCP instance is not mounted by this Agent"}
		}
	}
	wireName := AnyStringNode(def.Meta["mcpToolName"])
	if wireName == "" {
		wireName = def.Name
	}
	return r.mcp.CallTool(ctx, serverKey, wireName, args, buildMCPMeta(def.Name, execCtx))
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
		// Output is a lossy model view (base64 elided) and Structured keeps the
		// business root; the untouched result is retained separately.
		result := convertMCPResult(toolName, mapped)
		result.MCPResult = mapped
		return result
	}
	return payloadToToolResult(payload)
}

func convertMCPResult(toolName string, mapped map[string]any) ToolExecutionResult {
	if isError, _ := mapped["isError"].(bool); isError {
		return mcpToolErrorResult(toolName, mapped)
	}
	items, _ := mapped["content"].([]any)
	contentText := renderMCPContent(items)
	if structured, ok := mapped["structuredContent"]; ok && structured != nil {
		result := payloadToToolResult(structured)
		if contentText == "" || mcpContentDuplicatesStructured(items, structured) {
			return result
		}
		// content is the model-facing channel; structuredContent stays in
		// Structured for validation and consumers. Append the structured JSON
		// only when no text block already carries it.
		result.Output = contentText
		if !mcpStructuredEmpty(structured) && !mcpContentContainsStructured(items, structured) {
			result.Output += "\n\n" + mcpStructuredSection(structured)
		}
		return result
	}
	if len(items) > 0 {
		result := ToolExecutionResult{Output: contentText, ExitCode: 0}
		if !mcpContentPlainText(items) {
			result.Structured = map[string]any{"content": items}
			if result.Output == "" {
				result.Output = MarshalJSON(result.Structured)
			}
		}
		return result
	}
	return payloadToToolResult(mapped)
}

func mcpStructuredSection(structured any) string {
	return "structuredContent:\n" + MarshalJSON(structured)
}

// renderMCPContent joins every content block in order for the model. Text
// blocks contribute their text; other blocks keep their type and fields as JSON
// with inline base64 payloads replaced by a size marker.
func renderMCPContent(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok {
			if item != nil {
				parts = append(parts, MarshalJSON(item))
			}
			continue
		}
		if mcpContentBlockType(block) == "text" {
			// Emptiness is judged on trimmed text, but the original is kept:
			// indentation and trailing spaces matter in code and patches.
			if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			continue
		}
		parts = append(parts, MarshalJSON(elideMCPBinary(block)))
	}
	return strings.Join(parts, "\n\n")
}

func mcpContentBlockType(block map[string]any) string {
	return strings.ToLower(strings.TrimSpace(AnyStringNode(block["type"])))
}

func elideMCPBinary(block map[string]any) map[string]any {
	out := CloneMap(block)
	if data, ok := out["data"].(string); ok && data != "" {
		out["data"] = fmt.Sprintf("[base64 omitted: %d chars]", len(data))
	}
	if resource := AnyMapNode(out["resource"]); resource != nil {
		if blob, ok := resource["blob"].(string); ok && blob != "" {
			resource = CloneMap(resource)
			resource["blob"] = fmt.Sprintf("[base64 omitted: %d chars]", len(blob))
			out["resource"] = resource
		}
	}
	return out
}

// mcpContentPlainText reports whether the blocks are bare text blocks, so the
// rendered Output already holds everything and no Structured copy is needed.
func mcpContentPlainText(items []any) bool {
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok || mcpContentBlockType(block) != "text" {
			return false
		}
		for key := range block {
			if key != "type" && key != "text" {
				return false
			}
		}
	}
	return true
}

// mcpContentDuplicatesStructured matches the spec-recommended shape: a single
// text block holding the serialized structuredContent.
func mcpContentDuplicatesStructured(items []any, structured any) bool {
	return len(items) == 1 && mcpContentContainsStructured(items, structured)
}

func mcpContentContainsStructured(items []any, structured any) bool {
	want := MarshalJSON(structured)
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok || mcpContentBlockType(block) != "text" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(AnyStringNode(block["text"])))
		decoder.UseNumber()
		var parsed, trailing any
		// The whole text must be one JSON value: a second Decode has to hit EOF,
		// otherwise trailing text such as "}\nwarning" would be treated as a copy.
		if decoder.Decode(&parsed) != nil || decoder.Decode(&trailing) != io.EOF {
			continue
		}
		if MarshalJSON(parsed) == want {
			return true
		}
	}
	return false
}

func mcpStructuredEmpty(structured any) bool {
	mapped, ok := structured.(map[string]any)
	return ok && len(mapped) == 0
}

func mcpToolErrorResult(toolName string, payload map[string]any) ToolExecutionResult {
	code := "mcp_tool_error"
	message := extractMCPErrorMessage(payload)
	structured := AnyMapNode(payload["structuredContent"])
	if value := strings.TrimSpace(AnyStringNode(structured["error"])); value != "" {
		code = value
	} else if value := strings.TrimSpace(AnyStringNode(structured["code"])); value != "" {
		code = value
	}
	if value := strings.TrimSpace(AnyStringNode(structured["message"])); value != "" {
		message = value
	}
	resultPayload := CloneMap(structured)
	if resultPayload == nil {
		resultPayload = map[string]any{}
	}
	resultPayload["tool"] = toolName
	resultPayload["ok"] = false
	resultPayload["code"] = code
	resultPayload["error"] = code
	resultPayload["message"] = message
	// Structured carries only the platform error contract; server content the
	// payload does not already represent is appended to the model text instead
	// of being written into same-named business fields.
	output := MarshalJSON(resultPayload)
	items, _ := payload["content"].([]any)
	if len(items) > 0 && !(len(structured) > 0 && mcpContentDuplicatesStructured(items, structured)) {
		if text := renderMCPContent(items); text != "" && strings.TrimSpace(text) != message {
			output += "\n\n" + text
		}
	}
	for _, key := range []string{"tool", "ok", "code", "error", "message"} {
		if original, ok := structured[key]; ok && MarshalJSON(original) != MarshalJSON(resultPayload[key]) {
			output += "\n\n" + mcpStructuredSection(structured)
			break
		}
	}
	return ToolExecutionResult{
		Output:     output,
		Structured: resultPayload,
		Error:      code,
		ExitCode:   -1,
	}
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
	if structured := AnyMapNode(payload["structuredContent"]); len(structured) > 0 {
		if message := strings.TrimSpace(AnyStringNode(structured["message"])); message != "" {
			return message
		}
	}
	if message := AnyStringNode(payload["error"]); strings.TrimSpace(message) != "" {
		return message
	}
	items, _ := payload["content"].([]any)
	for _, item := range items {
		if contentMap, ok := item.(map[string]any); ok {
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
	timeout := defaultToolCallTimeout
	if budget.Tool.Timeout > 0 {
		timeout = toolTimeout(budget.Tool)
	}
	if budget.Tool.RetryCount > 0 {
		retryCount = budget.Tool.RetryCount
	}
	// Sleep owns its timer; retrying or applying the ordinary tool timeout would
	// change the requested wait. The parent Run cancellation still applies.
	if toolName == "wait" {
		timeout, retryCount = 0, 0
	}
	// Web control applies this timeout to each Desktop request itself, so a Run
	// can stay suspended while the user connects the browser it needs.
	if isWebControlTool(toolName) {
		timeout = 0
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
			observability.LogToolInvocation(toolName, toolInvocationResultStatus(result), map[string]any{
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

func toolInvocationResultStatus(result ToolExecutionResult) string {
	if result.ExitCode != 0 || strings.TrimSpace(result.Error) != "" {
		return "error"
	}
	return "ok"
}

func (r *ToolRouter) invokeInteractionWithPolicy(ctx context.Context, toolName string, execCtx *ExecutionContext, invoke func(context.Context) (ToolExecutionResult, error)) (ToolExecutionResult, error) {
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
	payload := apperrors.Payload(
		apperrors.CodeToolCallsExceeded,
		"tool call budget exceeded",
		apperrors.WithScope(apperrors.ScopeTool),
		apperrors.WithCategory(apperrors.CategoryTool),
		apperrors.WithDiagnostics(map[string]any{
			"toolCalls":  execCtx.ToolCalls,
			"limitValue": budget.Tool.MaxCalls,
			"limitName":  "budget.tool.maxCalls",
			"toolName":   toolName,
		}),
	)
	return ToolExecutionResult{
		Output:     MarshalJSON(payload),
		Structured: payload,
		Error:      "tool_calls_exceeded",
		ExitCode:   -1,
	}, true
}
