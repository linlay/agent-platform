package catalog

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/skillmeta"
)

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
			Presentation: skillmeta.Parse(skill.Metadata, skill.Version),
			ID:           skill.ID,
			Name:         skill.Name,
			Description:  skill.Description,
			Meta:         skillSummaryMeta(skill),
		})
	}
	return items
}

func (r *FileRegistry) Tools(tag string) []api.ToolSummary {
	needleTag := strings.ToLower(strings.TrimSpace(tag))
	items := make([]api.ToolSummary, 0, len(r.tools))
	for _, tool := range r.tools {
		if needleTag != "" && !matchesToolTag(tool, needleTag) {
			continue
		}
		sourceCategory := toolSummarySourceCategory(tool)
		sourceType := strings.TrimSpace(anyStringValue(tool.Meta["sourceType"]))
		serverKey := ""
		if strings.EqualFold(sourceType, "mcp") {
			serverKey = strings.TrimSpace(anyStringValue(tool.Meta["serverKey"]))
		}
		translations, _ := tool.Meta["toolI18n"].(map[string]any)
		items = append(items, api.ToolSummary{
			ToolI18n:       translations,
			Key:            tool.Key,
			Name:           tool.Name,
			Label:          tool.Label,
			Description:    tool.Description,
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
	return strings.Contains(strings.ToLower(skill.ID), needle) ||
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
	safeMetadata := safeSkillSummaryMetadata(skill.Metadata)
	if skill.Version != "" {
		if safeMetadata == nil {
			safeMetadata = map[string]any{}
		}
		safeMetadata["version"] = skill.Version
	}
	if len(safeMetadata) > 0 {
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
	}
	if ref, ok := tool.Meta["view"].(map[string]any); ok {
		fields = append(fields, stringNode(ref["key"]), stringNode(ref["connectorId"]))
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}
