package platformcontrol

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"agent-platform/internal/adminsource"
	agentcoder "agent-platform/internal/agent/coder"
	agentgeneral "agent-platform/internal/agent/general"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/automation"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
	"agent-platform/internal/models"
	"agent-platform/internal/observability"
)

const (
	GeneralCreationPath = "agents.creation.general"
	CoderCreationPath   = "agents.creation.coder"
	KBaseCreationPath   = "agents.creation.kbase"
	maxCandidateBytes   = 1 << 20
)

type ToolHandler struct {
	automations     *automation.Service
	RuntimeSnapshot func() map[string]any
	cfg             config.Config
	registry        catalog.Registry
	chats           ChatPinService
	sources         *adminsource.ControlService
	conversations   *conversation.Service
	models          *models.ModelRegistry
}

type ChatPinService interface {
	SetChatPinned(string, bool) (conversation.PinResult, error)
}

func NewToolHandler(cfg config.Config, registry catalog.Registry, chats ChatPinService) *ToolHandler {
	return &ToolHandler{cfg: cfg, registry: registry, chats: chats}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{"automation_query", "automation_manage", "catalog_query", "catalog_manage", "chat_query", "chat_manage", "platform_inspect"}
}

func (h *ToolHandler) get(path string) contracts.ToolExecutionResult {
	switch path {
	case GeneralCreationPath:
		defaults := h.cfg.GeneralSettings.DefaultAgent
		definition := agentgeneral.ApplyCreateDefaults(map[string]any{"mode": agentgeneral.Mode}, agentgeneral.CreateDefaults{
			ModelKey: defaults.ModelKey, ReasoningEffort: defaults.ReasoningEffort, Budget: defaults.Budget,
		})
		missing := missingDefinitionFields(definition, "modelConfig.modelKey")
		return successResult(map[string]any{
			"path":               path,
			"definitionDefaults": definition,
			"ready":              len(missing) == 0,
			"missingFields":      missing,
		})
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
			ModelKey: defaults.ModelKey, ReasoningEffort: defaults.ReasoningEffort,
		})
		definition = agentkbase.ApplyCreateToolDefaults(definition)
		missing := missingDefinitionFields(definition, "modelConfig.modelKey")
		return successResult(map[string]any{
			"path":               path,
			"keyPrefix":          agentkbase.CreatePrefix,
			"definitionDefaults": definition,
			"ready":              len(missing) == 0,
			"missingFields":      missing,
		})
	default:
		return errorResult("unsupported_config_path", "path must be agents.creation.general, agents.creation.coder or agents.creation.kbase")
	}
}

func (h *ToolHandler) validate(resourceType string, resourceKey string, content string) contracts.ToolExecutionResult {
	if resourceKey == "" {
		return errorResult("invalid_request", "resourceKey is required")
	}
	if strings.TrimSpace(content) == "" {
		return errorResult("invalid_request", "content is required")
	}
	if strings.TrimSpace(content) == "[REDACTED]" {
		return errorResult("catalog_candidate_redacted", "content is a redacted history placeholder, not candidate content; reread the candidate file and submit its complete content using only resourceType, resourceKey, content")
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
	case "connector":
		if err := connector.ValidateManifest(resourceKey, []byte(content)); err != nil {
			diagnostics = append(diagnostics, candidateError("invalid_connector_manifest", err))
		}
	default:
		return errorResult("unsupported_resource_type", "resourceType must be agent, team, skill, or connector")
	}

	return successResult(map[string]any{
		"resourceType": resourceType,
		"resourceKey":  resourceKey,
		"valid":        !hasErrorDiagnostic(diagnostics),
		"candidate": map[string]any{
			"sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(content))),
			"bytes":  len([]byte(content)),
		},
		"diagnostics": diagnostics,
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
