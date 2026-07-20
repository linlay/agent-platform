package platformconfig

import (
	"context"
	"encoding/json"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mcp"
	"agent-platform/internal/observability"
)

const (
	ToolName          = "platform_config"
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

func (h *ToolHandler) Invoke(_ context.Context, _ string, args map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	action := strings.ToLower(strings.TrimSpace(stringValue(args, "action")))
	switch action {
	case "get":
		return h.get(strings.TrimSpace(stringValue(args, "path"))), nil
	case "validate":
		return h.validate(
			strings.ToLower(strings.TrimSpace(stringValue(args, "resourceType"))),
			strings.TrimSpace(stringValue(args, "resourceKey")),
			stringValue(args, "content"),
		), nil
	default:
		return errorResult("invalid_action", "action must be get or validate"), nil
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
	data, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{Output: string(data), Structured: payload, ExitCode: 0}
}

func errorResult(code string, message string) contracts.ToolExecutionResult {
	payload := map[string]any{"error": code, "message": strings.TrimSpace(message)}
	data, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{Output: string(data), Structured: payload, Error: code, ExitCode: -1}
}
