package platformcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/mcp"
	"agent-platform/internal/observability"
	"agent-platform/internal/runenv"
)

const (
	CoderCreationPath = "agents.creation.coder"
	KBaseCreationPath = "agents.creation.kbase"
	maxCandidateBytes = 1 << 20
)

type ToolHandler struct {
	cfg      config.Config
	registry catalog.Registry
}

func NewToolHandler(cfg config.Config, registry catalog.Registry) *ToolHandler {
	return &ToolHandler{cfg: cfg, registry: registry}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{ToolName}
}

func (h *ToolHandler) Invoke(_ context.Context, _ string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	rawOperation, operationExists := args["operation"]
	operationValue, operationIsString := rawOperation.(string)
	operationName := strings.ToLower(strings.TrimSpace(operationValue))
	if !operationExists || !operationIsString || operationName == "" {
		return operationError(operationName, "platform_control_invalid_params", "operation must be a non-empty string", execCtx), nil
	}
	params, validationError := strictParams(args)
	if validationError != nil {
		return operationError(operationName, "platform_control_invalid_params", validationError.Error(), execCtx), nil
	}
	descriptor, exists := LookupOperation(operationName)
	if !exists {
		return operationError(operationName, "platform_control_invalid_operation", "operation is not supported", execCtx), nil
	}
	if !h.cfg.PlatformControl.Enabled {
		return operationError(operationName, "platform_control_disabled", "platform_control is disabled", execCtx), nil
	}
	if execCtx != nil && !descriptor.AllowsExecutionPolicy(execCtx.ToolExecutionPolicy) {
		return operationError(operationName, "platform_control_stage_forbidden", "operation is not permitted in the current stage", execCtx), nil
	}
	if descriptor.Validate == nil || descriptor.Invoke == nil {
		return operationError(operationName, "platform_control_invalid_operation", "operation is not executable", execCtx), nil
	}
	if err := descriptor.Validate(params); err != nil {
		return operationError(operationName, "platform_control_invalid_params", err.Error(), execCtx), nil
	}
	result := descriptor.Invoke(h, operationName, params, execCtx)
	return normalizeEnvelope(operationName, result, execCtx), nil
}

func invokeRegisteredOperation(h *ToolHandler, operationName string, params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	switch operationName {
	case "capabilities.list":
		return h.capabilities(execCtx, nil)
	case "catalog.defaults.get":
		return h.get(strings.TrimSpace(stringValue(params, "path")))
	case "catalog.validate":
		return h.validate(strings.ToLower(strings.TrimSpace(stringValue(params, "resourceType"))), strings.TrimSpace(stringValue(params, "resourceKey")), stringValue(params, "content"))
	case "run.env.set", "run.env.unset":
		return h.mutateEnvironment(operationName, params, execCtx)
	case "runtime.status":
		return h.runtimeStatus(operationName, params, execCtx)
	case "security.explain":
		return h.securityExplain(operationName, params, execCtx)
	}
	return errorResult("platform_control_invalid_operation", "operation is not executable")
}

func validateOperationParams(operationName string, params map[string]any) error {
	switch operationName {
	case "capabilities.list", "runtime.status":
		return requireFields(params, nil, nil)
	case "catalog.defaults.get":
		if err := requireFields(params, []string{"path"}, nil); err != nil {
			return err
		}
		return requireStringFields(params, "path")
	case "catalog.validate":
		if err := requireFields(params, []string{"resourceType", "resourceKey", "content"}, nil); err != nil {
			return err
		}
		return requireStringFields(params, "resourceType", "resourceKey", "content")
	case "run.env.set":
		if err := requireFields(params, []string{"key", "value"}, []string{"expectedRevision", "idempotencyKey"}); err != nil {
			return err
		}
		return requireStringFields(params, "key", "value")
	case "run.env.unset":
		if err := requireFields(params, []string{"key"}, []string{"expectedRevision", "idempotencyKey"}); err != nil {
			return err
		}
		return requireStringFields(params, "key")
	case "security.explain":
		if err := requireFields(params, []string{"operation"}, []string{"key", "path", "access"}); err != nil {
			return err
		}
		for _, field := range []string{"operation", "key", "path", "access"} {
			if _, exists := params[field]; exists {
				if err := requireStringFields(params, field); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("operation is not supported")
	}
}

func (h *ToolHandler) get(path string) contracts.ToolExecutionResult {
	switch path {
	case CoderCreationPath:
		defaults := h.cfg.CoderSettings.DefaultAgent
		definition := agentcoder.ApplyCreateDefaults(map[string]any{"mode": agentcoder.Mode}, agentcoder.CreateDefaults{
			ModelKey: defaults.ModelKey, ReasoningEffort: defaults.ReasoningEffort, Budget: defaults.Budget,
		})
		missing := missingDefinitionFields(definition, "modelConfig.modelKey")
		return successResult(map[string]any{
			"path":               path,
			"keyPrefix":          agentcoder.CreatePrefix,
			"definitionDefaults": definition,
			"ready":              len(missing) == 0,
			"missingFields":      missing,
		})
	case KBaseCreationPath:
		defaults := h.cfg.KBase.DefaultAgent
		definition := agentkbase.ApplyCreateDefaults(map[string]any{"mode": agentkbase.Mode}, agentkbase.CreateDefaults{
			ModelKey: defaults.ModelKey, ReasoningEffort: defaults.ReasoningEffort, EmbeddingModelKey: h.cfg.KBase.Embedding.ModelKey,
		})
		missing := missingDefinitionFields(definition, "modelConfig.modelKey", "kbaseConfig.embedding.modelKey")
		return successResult(map[string]any{
			"path":               path,
			"keyPrefix":          agentkbase.CreatePrefix,
			"definitionDefaults": definition,
			"ready":              len(missing) == 0,
			"missingFields":      missing,
		})
	default:
		return errorResult("unsupported_config_path", "path must be agents.creation.coder or agents.creation.kbase")
	}
}

func (h *ToolHandler) validate(resourceType string, resourceKey string, content string) contracts.ToolExecutionResult {
	if resourceKey == "" {
		return errorResult("invalid_request", "resourceKey is required")
	}
	if strings.TrimSpace(content) == "" {
		return errorResult("invalid_request", "content is required")
	}
	if len(content) > maxCandidateBytes {
		return errorResult("invalid_request", "content exceeds 1 MiB")
	}

	diagnostics := make([]map[string]any, 0)
	switch resourceType {
	case "agent":
		if err := catalog.ValidateAgentCandidate(resourceKey, []byte(content)); err != nil {
			diagnostics = append(diagnostics, candidateError("invalid_agent_config", err))
		}
	case "team":
		team, err := catalog.ValidateTeamCandidate(resourceKey, []byte(content))
		if err != nil {
			diagnostics = append(diagnostics, candidateError("invalid_team_config", err))
		} else {
			diagnostics = append(diagnostics, h.teamMemberDiagnostics(team.AgentKeys)...)
		}
	case "skill":
		for _, item := range catalog.ValidateSkillCandidate(resourceKey, []byte(content), h.cfg.Skills.MaxPromptChars) {
			diagnostics = append(diagnostics, diagnostic(item.Severity, item.Code, sanitizeDiagnostic(item.Message)))
		}
	case "mcp-server":
		if err := mcp.ValidateServerCandidate(resourceKey, []byte(content)); err != nil {
			diagnostics = append(diagnostics, candidateError("invalid_mcp_server_config", err))
		}
	default:
		return errorResult("unsupported_resource_type", "resourceType must be agent, team, skill, or mcp-server")
	}

	return successResult(map[string]any{
		"resourceType": resourceType,
		"resourceKey":  resourceKey,
		"valid":        !hasErrorDiagnostic(diagnostics),
		"diagnostics":  diagnostics,
	})
}

func (h *ToolHandler) teamMemberDiagnostics(agentKeys []string) []map[string]any {
	diagnostics := make([]map[string]any, 0)
	if len(agentKeys) == 0 {
		return append(diagnostics, diagnostic("error", "empty_agent_keys", "agentKeys must contain at least one agent"))
	}
	seen := map[string]struct{}{}
	for _, raw := range agentKeys {
		key := strings.TrimSpace(raw)
		if key == "" {
			diagnostics = append(diagnostics, diagnostic("error", "empty_agent_key", "agentKeys must not contain empty values"))
			continue
		}
		if _, exists := seen[key]; exists {
			diagnostics = append(diagnostics, diagnostic("error", "duplicate_agent_key", "agentKeys must not contain duplicates"))
			continue
		}
		seen[key] = struct{}{}
		if h.registry != nil {
			if _, ok := h.registry.AgentDefinition(key); !ok {
				diagnostics = append(diagnostics, diagnostic("error", "unknown_agent", "team member is not present in the agent catalog: "+key))
			}
		}
	}
	return diagnostics
}

func missingDefinitionFields(definition map[string]any, paths ...string) []string {
	missing := make([]string, 0, len(paths))
	for _, path := range paths {
		var value any = definition
		for _, segment := range strings.Split(path, ".") {
			node, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = node[segment]
		}
		if strings.TrimSpace(contracts.AnyStringNode(value)) == "" {
			missing = append(missing, path)
		}
	}
	return missing
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func candidateError(code string, err error) map[string]any {
	message := "candidate configuration is invalid"
	if err != nil {
		message = sanitizeDiagnostic(err.Error())
	}
	return diagnostic("error", code, message)
}

func sanitizeDiagnostic(message string) string {
	message = strings.TrimSpace(observability.SanitizeLog(message))
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500]) + "..."
	}
	return message
}

func diagnostic(severity string, code string, message string) map[string]any {
	return map[string]any{
		"severity": severity,
		"code":     code,
		"message":  strings.TrimSpace(message),
	}
}

func hasErrorDiagnostic(diagnostics []map[string]any) bool {
	for _, item := range diagnostics {
		if strings.EqualFold(contracts.AnyStringNode(item["severity"]), "error") {
			return true
		}
	}
	return false
}

func successResult(payload map[string]any) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{
		Output:     contracts.CompactToolModelOutput(payload, ""),
		Structured: payload,
		ExitCode:   0,
	}
}

func errorResult(code string, message string) contracts.ToolExecutionResult {
	payload := map[string]any{"error": code, "message": strings.TrimSpace(message)}
	return contracts.ToolExecutionResult{
		Output:     contracts.CompactToolModelOutput(payload, ""),
		Structured: payload,
		Error:      code,
		ExitCode:   -1,
	}
}

func strictParams(args map[string]any) (map[string]any, error) {
	for key := range args {
		if key != "operation" && key != "params" {
			return nil, fmt.Errorf("unknown top-level field %q", key)
		}
	}
	raw, exists := args["params"]
	if !exists {
		return map[string]any{}, nil
	}
	params, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params must be an object")
	}
	if len(params) > 32 {
		return nil, fmt.Errorf("params exceeds 32 properties")
	}
	return params, nil
}

func requireFields(params map[string]any, required, optional []string) error {
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range params {
		if !allowed[key] {
			return fmt.Errorf("unknown params field %q", key)
		}
	}
	for _, key := range required {
		if _, ok := params[key]; !ok {
			return fmt.Errorf("params.%s is required", key)
		}
	}
	return nil
}

func requireStringFields(params map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, ok := params[field].(string); !ok {
			return fmt.Errorf("params.%s must be a string", field)
		}
	}
	return nil
}

func (h *ToolHandler) capabilities(execCtx *contracts.ExecutionContext, _ []string) contracts.ToolExecutionResult {
	agentKey := ""
	revision := uint64(0)
	if execCtx != nil {
		agentKey = strings.TrimSpace(execCtx.Session.AgentKey)
		if execCtx.RunEnvironment != nil {
			revision = execCtx.RunEnvironment.Revision()
		}
	}
	allowed := make([]string, 0, len(descriptors))
	for _, operation := range OperationNames() {
		descriptor, exists := LookupOperation(operation)
		available, _ := operationAvailable(execCtx, descriptor)
		if exists && available {
			allowed = append(allowed, operation)
		}
	}
	return successResult(map[string]any{
		"agentKey": agentKey, "operations": allowed,
		"limits":         map[string]any{"maxDynamicKeys": h.cfg.PlatformControl.MaxDynamicKeys, "maxValueBytes": h.cfg.PlatformControl.MaxValueBytes, "maxTotalBytes": h.cfg.PlatformControl.MaxTotalBytes},
		"runEnvRevision": revision,
	})
}

func operationAvailable(execCtx *contracts.ExecutionContext, descriptor Descriptor) (bool, string) {
	if execCtx == nil {
		return descriptor.ReadOnly && !strings.HasPrefix(descriptor.Name, "run.env."), "execution context unavailable"
	}
	if !descriptor.AllowsExecutionPolicy(execCtx.ToolExecutionPolicy) {
		return false, "operation is not permitted in the current stage"
	}
	if strings.HasPrefix(descriptor.Name, "run.env.") && execCtx.RunEnvironment == nil {
		return false, "run environment unavailable"
	}
	if !descriptor.ReadOnly && (strings.TrimSpace(execCtx.Session.SubTaskID) != "" || strings.TrimSpace(execCtx.Session.TeamID) != "") {
		return false, "run environment mutation is limited to ordinary root runs"
	}
	return true, ""
}

func (h *ToolHandler) mutateEnvironment(operationName string, params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	if execCtx == nil || execCtx.RunEnvironment == nil {
		return errorResult("run_env_unavailable", "current run has no dynamic environment state")
	}
	if strings.TrimSpace(execCtx.Session.SubTaskID) != "" || strings.TrimSpace(execCtx.Session.TeamID) != "" {
		return errorResult("run_env_mutation_forbidden", "only an ordinary root agent run may mutate its environment")
	}
	optional := []string{"expectedRevision", "idempotencyKey"}
	request := runenv.MutationRequest{DefaultIdempotencyKey: strings.TrimSpace(execCtx.Session.RunID) + ":" + strings.TrimSpace(execCtx.CurrentToolID)}
	if expected, ok, err := optionalRevision(params["expectedRevision"]); err != nil {
		return errorResult("run_env_invalid_revision", err.Error())
	} else if ok {
		request.ExpectedRevision = &expected
	}
	request.IdempotencyKey = strings.TrimSpace(stringValue(params, "idempotencyKey"))
	if _, exists := params["idempotencyKey"]; exists {
		if err := requireStringFields(params, "idempotencyKey"); err != nil {
			return errorResult("platform_control_invalid_params", err.Error())
		}
		if len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 128 {
			return errorResult("platform_control_invalid_params", "params.idempotencyKey must contain 1 to 128 characters")
		}
	}
	switch operationName {
	case "run.env.set":
		if err := requireFields(params, []string{"key", "value"}, optional); err != nil {
			return errorResult("platform_control_invalid_params", err.Error())
		}
		if err := requireStringFields(params, "key", "value"); err != nil {
			return errorResult("platform_control_invalid_params", err.Error())
		}
		request.Operation = runenv.OperationSet
		request.Name = stringValue(params, "key")
		request.Value = stringValue(params, "value")
	case "run.env.unset":
		if err := requireFields(params, []string{"key"}, optional); err != nil {
			return errorResult("platform_control_invalid_params", err.Error())
		}
		if err := requireStringFields(params, "key"); err != nil {
			return errorResult("platform_control_invalid_params", err.Error())
		}
		request.Operation = runenv.OperationUnset
		request.Name = stringValue(params, "key")
	}
	result, err := execCtx.RunEnvironment.Mutate(request)
	if err != nil {
		return runEnvironmentError(err)
	}
	return successResult(map[string]any{"key": result.Key, "changed": result.Changed, "idempotent": result.Idempotent, "revision": result.Revision})
}

func (h *ToolHandler) runtimeStatus(operationName string, params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	if err := requireFields(params, nil, nil); err != nil {
		return errorResult("platform_control_invalid_params", err.Error())
	}
	revision := uint64(0)
	if execCtx != nil && execCtx.RunEnvironment != nil {
		revision = execCtx.RunEnvironment.Revision()
	}
	return successResult(map[string]any{
		"platformControl": map[string]any{"enabled": h.cfg.PlatformControl.Enabled},
		"containerHub":    map[string]any{"enabled": h.cfg.ContainerHub.Enabled},
		"memory":          map[string]any{"enabled": h.cfg.Memory.Enabled},
		"runEnv":          map[string]any{"available": execCtx != nil && execCtx.RunEnvironment != nil, "revision": revision},
	})
}

func (h *ToolHandler) securityExplain(operationName string, params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	if err := requireFields(params, []string{"operation"}, []string{"key", "path", "access"}); err != nil {
		return errorResult("platform_control_invalid_params", err.Error())
	}
	for _, field := range []string{"operation", "key", "path", "access"} {
		if _, exists := params[field]; exists {
			if err := requireStringFields(params, field); err != nil {
				return errorResult("platform_control_invalid_params", err.Error())
			}
		}
	}
	target := strings.ToLower(strings.TrimSpace(stringValue(params, "operation")))
	descriptor, known := LookupOperation(target)
	available, unavailableReason := operationAvailable(execCtx, descriptor)
	allowed := known && available
	data := map[string]any{"operation": target, "known": known, "allowed": allowed}
	if known {
		data["riskClass"] = descriptor.RiskClass
		data["readOnly"] = descriptor.ReadOnly
		data["barrier"] = descriptor.Barrier
		if !available {
			data["reason"] = unavailableReason
		}
	}
	if key := strings.ToUpper(strings.TrimSpace(stringValue(params, "key"))); key != "" {
		keyData := map[string]any{"name": key}
		if err := runenv.ValidateName(key, h.cfg.PlatformControl.DenyKeys); err != nil {
			keyData["allowed"] = false
			keyData["reason"] = err.Error()
		} else if execCtx == nil || execCtx.RunEnvironment == nil {
			keyData["allowed"] = false
			keyData["reason"] = "run environment unavailable"
		} else {
			keyData["allowed"] = true
		}
		data["key"] = keyData
	}
	if rawPath := strings.TrimSpace(stringValue(params, "path")); rawPath != "" {
		access := strings.ToLower(strings.TrimSpace(stringValue(params, "access")))
		if access == "" {
			access = string(filetools.ReadAccess)
		}
		if access != string(filetools.ReadAccess) && access != string(filetools.WriteAccess) {
			return errorResult("platform_control_invalid_params", "params.access must be read or write")
		}
		pathData := map[string]any{"path": rawPath, "access": access}
		if execCtx == nil {
			pathData["allowed"] = false
			pathData["reason"] = "execution context unavailable"
		} else {
			plan, err := filetools.BuildAccessPlanFromPolicy(h.cfg.AccessPolicy, execCtx.Session, filetools.AccessMode(access), rawPath)
			if err != nil {
				pathData["allowed"] = false
				pathData["reason"] = sanitizeDiagnostic(err.Error())
			} else {
				pathData["allowed"] = plan.AllowedByWhitelist || plan.AutoApproved
				pathData["blocked"] = plan.Blocked
				pathData["requiresApproval"] = !plan.Blocked && !plan.AllowedByWhitelist && !plan.AutoApproved
				pathData["autoApproved"] = plan.AutoApproved
				pathData["accessLevel"] = plan.AccessLevel
				pathData["root"] = plan.Root
				pathData["resolvedPath"] = plan.Path
				if strings.TrimSpace(plan.Reason) != "" {
					pathData["reason"] = plan.Reason
				}
			}
		}
		data["path"] = pathData
	}
	return successResult(data)
}

func optionalRevision(value any) (uint64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return uint64(typed), true, nil
		}
	case int64:
		if typed >= 0 {
			return uint64(typed), true, nil
		}
	case float64:
		if typed >= 0 && typed == float64(uint64(typed)) {
			return uint64(typed), true, nil
		}
	}
	return 0, false, fmt.Errorf("expectedRevision must be a non-negative integer")
}

func runEnvironmentError(err error) contracts.ToolExecutionResult {
	code := "run_env_invalid_request"
	message := err.Error()
	switch {
	case errors.Is(err, runenv.ErrClosed):
		code = "run_env_closed"
	case errors.Is(err, runenv.ErrRevisionConflict):
		code = "run_env_revision_conflict"
	case errors.Is(err, runenv.ErrKeyNotSet):
		code = "run_env_key_not_set"
	case strings.Contains(message, "idempotency key"):
		code = "run_env_idempotency_conflict"
	case strings.Contains(message, "name must match"):
		code = "run_env_key_invalid"
	case strings.Contains(message, "reserved or denied"), strings.Contains(message, "denied by platform policy"):
		code = "run_env_key_forbidden"
	case strings.Contains(message, "value must"), strings.Contains(message, "value exceeds"):
		code = "run_env_value_invalid"
	case strings.Contains(message, "dynamic keys"), strings.Contains(message, "total bytes"):
		code = "run_env_limit_exceeded"
	case strings.Contains(message, "checkpoint"):
		code = "run_env_checkpoint_failed"
	}
	return errorResult(code, message)
}

func normalizeEnvelope(operation string, result contracts.ToolExecutionResult, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	revision := uint64(0)
	if execCtx != nil && execCtx.RunEnvironment != nil {
		revision = execCtx.RunEnvironment.Revision()
	}
	data := result.Structured
	if data == nil {
		data = map[string]any{}
	}
	envelope := map[string]any{"operation": operation, "status": "ok", "scope": "run", "revision": revision, "data": data}
	if result.Error != "" {
		envelope["status"] = "error"
	}
	result.Structured = envelope
	result.Output = contracts.CompactToolModelOutput(envelope, "")
	return result
}

func operationError(operation, code, message string, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	return normalizeEnvelope(operation, errorResult(code, message), execCtx)
}
