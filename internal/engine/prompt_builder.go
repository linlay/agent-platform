package engine

import (
	"fmt"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
)

var defaultContextTagOrder = []string{
	"agent_identity",
	"run_session",
	"scene",
	"references",
	"skills",
	"memory_context",
	"execution_policy",
}

func buildSystemPrompt(session QuerySession, req api.QueryRequest, modelKey string) string {
	var builder strings.Builder
	tags := orderedContextTags(session.ContextTags)
	for _, tag := range tags {
		section := promptSection(tag, session, req, modelKey)
		if section == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(section)
	}
	builder.WriteString("\n\nUse available tools only when they are necessary, and provide a final assistant answer when the run completes.")
	return builder.String()
}

func orderedContextTags(tags []string) []string {
	if len(tags) == 0 {
		return append([]string(nil), defaultContextTagOrder...)
	}
	weights := map[string]int{}
	for idx, tag := range defaultContextTagOrder {
		weights[tag] = idx
	}
	normalized := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, tag := range tags {
		key := strings.ToLower(strings.TrimSpace(tag))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		wi, okI := weights[normalized[i]]
		wj, okJ := weights[normalized[j]]
		switch {
		case okI && okJ:
			return wi < wj
		case okI:
			return true
		case okJ:
			return false
		default:
			return normalized[i] < normalized[j]
		}
	})
	return normalized
}

func promptSection(tag string, session QuerySession, req api.QueryRequest, modelKey string) string {
	switch tag {
	case "agent_identity":
		var builder strings.Builder
		builder.WriteString("Agent Identity:\n")
		builder.WriteString("- role: Go runner agent\n")
		if session.AgentName != "" {
			builder.WriteString("- agentName: " + session.AgentName + "\n")
		}
		builder.WriteString("- agentKey: " + session.AgentKey + "\n")
		builder.WriteString("- modelKey: " + modelKey)
		return builder.String()
	case "run_session":
		var builder strings.Builder
		builder.WriteString("Run Session:\n")
		builder.WriteString("- runId: " + session.RunID + "\n")
		builder.WriteString("- chatId: " + session.ChatID + "\n")
		if session.ChatName != "" {
			builder.WriteString("- chatName: " + session.ChatName + "\n")
		}
		if session.Mode != "" {
			builder.WriteString("- mode: " + session.Mode + "\n")
		}
		if session.Subject != "" {
			builder.WriteString("- subject: " + session.Subject + "\n")
		}
		builder.WriteString(fmt.Sprintf("- created: %t", session.Created))
		return builder.String()
	case "scene":
		if req.Scene == nil || (strings.TrimSpace(req.Scene.URL) == "" && strings.TrimSpace(req.Scene.Title) == "") {
			return ""
		}
		var builder strings.Builder
		builder.WriteString("Scene:\n")
		if req.Scene.Title != "" {
			builder.WriteString("- title: " + strings.TrimSpace(req.Scene.Title) + "\n")
		}
		if req.Scene.URL != "" {
			builder.WriteString("- url: " + strings.TrimSpace(req.Scene.URL))
		}
		return strings.TrimRight(builder.String(), "\n")
	case "references":
		if len(req.References) == 0 {
			return ""
		}
		var builder strings.Builder
		builder.WriteString("References:\n")
		for _, ref := range req.References {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(ref.Name))
			if ref.SandboxPath != "" {
				builder.WriteString(" sandboxPath=" + ref.SandboxPath)
			}
			builder.WriteString("\n")
		}
		return strings.TrimRight(builder.String(), "\n")
	case "skills":
		if len(session.SkillKeys) == 0 {
			return ""
		}
		return "Skills:\n- " + strings.Join(session.SkillKeys, "\n- ")
	case "memory_context":
		// Prefer session-level memory context (auto-queried from store)
		if strings.TrimSpace(session.MemoryContext) != "" {
			return "Runtime Context: Agent Memory\n" + strings.TrimSpace(session.MemoryContext)
		}
		// Fallback to request params
		if memoryText, _ := req.Params["memoryContext"].(string); strings.TrimSpace(memoryText) != "" {
			return "Runtime Context: Agent Memory\n" + strings.TrimSpace(memoryText)
		}
		return ""
	case "execution_policy":
		var lines []string
		if len(session.Budget) > 0 {
			lines = append(lines, "- budget="+formatPromptMap(session.Budget))
		}
		if len(session.StageSettings) > 0 {
			lines = append(lines, "- stageSettings="+formatPromptMap(session.StageSettings))
		}
		if len(lines) == 0 {
			return ""
		}
		return "Execution Policy:\n" + strings.Join(lines, "\n")
	default:
		return ""
	}
}

func formatPromptMap(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, values[key]))
	}
	return strings.Join(parts, ",")
}
