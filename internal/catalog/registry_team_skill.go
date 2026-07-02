package catalog

import (
	"log"
	"strings"

	"agent-platform/internal/api"
)

func (r *FileRegistry) Teams() []api.TeamSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agentKeys := sortedKeys(r.agents)
	agentsByID := make(map[string]AgentDefinition, len(agentKeys))
	for _, key := range agentKeys {
		agentsByID[key] = r.agents[key]
	}

	keys := sortedKeys(r.teams)
	items := make([]api.TeamSummary, 0, len(keys))
	for _, key := range keys {
		team := r.teams[key]
		invalidAgentKeys := make([]string, 0)
		icon := team.Icon
		for _, agentKey := range team.AgentKeys {
			agent, ok := agentsByID[agentKey]
			if !ok {
				invalidAgentKeys = append(invalidAgentKeys, agentKey)
				continue
			}
			if icon == nil {
				icon = agent.Icon
			}
		}
		defaultValid := team.DefaultAgentKey != "" && containsString(team.AgentKeys, team.DefaultAgentKey) && agentsByID[team.DefaultAgentKey].Key != ""
		if len(invalidAgentKeys) > 0 {
			log.Printf("[catalog][teams] team=%s invalidAgentKeys=%v", team.TeamID, invalidAgentKeys)
		}
		items = append(items, api.TeamSummary{
			TeamID:    team.TeamID,
			Name:      team.Name,
			Icon:      icon,
			AgentKeys: append([]string(nil), team.AgentKeys...),
			Meta: map[string]any{
				"invalidAgentKeys":     invalidAgentKeys,
				"defaultAgentKey":      team.DefaultAgentKey,
				"defaultAgentKeyValid": defaultValid,
			},
		})
	}
	return items
}

func (r *FileRegistry) Skills(tag string) []api.SkillSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(tag))
	keys := sortedKeys(r.skills)
	items := make([]api.SkillSummary, 0, len(keys))
	for _, key := range keys {
		skill := r.skills[key]
		if needle != "" && !matchesSkillTag(skill, needle) {
			continue
		}
		items = append(items, api.SkillSummary{
			Key:         skill.Key,
			Name:        skill.Name,
			Description: skill.Description,
			Meta:        skillSummaryMeta(skill),
		})
	}
	return items
}

func (r *FileRegistry) Tools(kind string, tag string) []api.ToolSummary {
	needleKind := strings.ToLower(strings.TrimSpace(kind))
	needleTag := strings.ToLower(strings.TrimSpace(tag))
	items := make([]api.ToolSummary, 0, len(r.tools))
	for _, tool := range r.tools {
		metaKind, _ := tool.Meta["kind"].(string)
		if needleKind != "" && strings.ToLower(metaKind) != needleKind {
			continue
		}
		if needleTag != "" && !matchesToolTag(tool, needleTag) {
			continue
		}
		sourceCategory := toolSummarySourceCategory(tool)
		sourceType := strings.TrimSpace(anyStringValue(tool.Meta["sourceType"]))
		serverKey := ""
		if strings.EqualFold(sourceType, "mcp") {
			serverKey = strings.TrimSpace(anyStringValue(tool.Meta["serverKey"]))
		}
		items = append(items, api.ToolSummary{
			Key:            tool.Key,
			Name:           tool.Name,
			Label:          tool.Label,
			Description:    tool.Description,
			Kind:           strings.TrimSpace(metaKind),
			SourceType:     sourceType,
			SourceCategory: sourceCategory,
			ServerKey:      serverKey,
		})
	}
	return items
}

func toolSummarySourceCategory(tool api.ToolDetailResponse) string {
	if tool.Meta == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(anyStringValue(tool.Meta["sourceCategory"]))) {
	case "platform":
		return "platform"
	case "external":
		return "external"
	case "mcp":
		return "mcp"
	}
	switch strings.ToLower(strings.TrimSpace(anyStringValue(tool.Meta["sourceType"]))) {
	case "mcp":
		return "mcp"
	case "agent-local":
		return "external"
	case "local":
		return "platform"
	}
	if strings.EqualFold(strings.TrimSpace(anyStringValue(tool.Meta["kind"])), "external") {
		return "external"
	}
	return ""
}

func anyStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func matchesSkillTag(skill SkillDefinition, needle string) bool {
	for _, trigger := range skill.Triggers {
		if strings.Contains(strings.ToLower(trigger), needle) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(skill.Key), needle) ||
		strings.Contains(strings.ToLower(skill.Name), needle) ||
		strings.Contains(strings.ToLower(skill.Description), needle) ||
		strings.Contains(strings.ToLower(skill.Prompt), needle)
}

func skillSummaryMeta(skill SkillDefinition) map[string]any {
	meta := map[string]any{
		"promptTruncated": skill.PromptTruncated,
	}
	if len(skill.Triggers) > 0 {
		triggers := make([]string, len(skill.Triggers))
		copy(triggers, skill.Triggers)
		meta["triggers"] = triggers
	}
	if safeMetadata := safeSkillSummaryMetadata(skill.Metadata); len(safeMetadata) > 0 {
		meta["metadata"] = safeMetadata
	}
	return meta
}

func safeSkillSummaryMetadata(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"version", "category", "author"} {
		if value, ok := values[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out[key] = strings.TrimSpace(text)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func matchesToolTag(tool api.ToolDetailResponse, needle string) bool {
	fields := []string{
		tool.Key,
		tool.Name,
		tool.Label,
		tool.Description,
		tool.AfterCallHint,
		stringNode(tool.Meta["kind"]),
		stringNode(tool.Meta["viewportType"]),
		stringNode(tool.Meta["viewportKey"]),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}
