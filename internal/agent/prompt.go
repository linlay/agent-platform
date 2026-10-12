package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type PromptContext struct {
	AgentKey           string
	AgentName          string
	Mode               string
	PlanningMode       bool
	WorkspaceDir       string
	ChatDir            string
	AvailableTools     []string
	LanguagePreference string
	UserRequest        string
}

var retiredEditingPlaceholder = regexp.MustCompile(`\{\{\s*editing_mode\s*\}\}`)

// ValidatePromptTemplate rejects retired Run-state placeholders instead of
// leaving an unresolved literal in the model's system instructions.
func ValidatePromptTemplate(prompt string) error {
	if retiredEditingPlaceholder.MatchString(prompt) {
		return fmt.Errorf("editing_mode is a retired prompt variable; Workspace editing is controlled by Run permissions")
	}
	return nil
}

func RenderPromptTemplate(prompt string, values map[string]string) string {
	result := prompt
	for key, value := range values {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
		result = strings.ReplaceAll(result, "{{ "+key+" }}", value)
	}
	return strings.TrimSpace(result)
}

func CommonPromptValues(ctx PromptContext) map[string]string {
	language := strings.TrimSpace(ctx.LanguagePreference)
	if language == "" {
		language = "zh-CN"
	}
	workspaceDir := strings.TrimSpace(ctx.WorkspaceDir)
	if workspaceDir == "" {
		workspaceDir = "unavailable"
	}
	chatDir := strings.TrimSpace(ctx.ChatDir)
	if chatDir == "" {
		chatDir = "unavailable"
	}
	return map[string]string{
		"agent_key":           strings.TrimSpace(ctx.AgentKey),
		"agent_name":          strings.TrimSpace(ctx.AgentName),
		"mode":                strings.TrimSpace(ctx.Mode),
		"planning_mode":       fmt.Sprintf("%t", ctx.PlanningMode),
		"workspace_dir":       workspaceDir,
		"chat_dir":            chatDir,
		"current_date":        time.Now().Format("2006-01-02"),
		"timezone":            LocalTimezoneName(),
		"language_preference": language,
		"available_tools":     strings.Join(NormalizeToolNames(ctx.AvailableTools), ", "),
		"user_request":        ctx.UserRequest,
	}
}

func NormalizeToolNames(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func LocalTimezoneName() string {
	name, offset := time.Now().Zone()
	if strings.TrimSpace(name) != "" {
		return name
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, offset/3600, (offset%3600)/60)
}

func FirstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
