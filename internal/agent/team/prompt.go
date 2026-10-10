package team

import (
	"fmt"
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const DefaultSystemPrompt = `You are an Agent with member delegation capability.
- You may answer directly, use your own tools, or delegate work to the supplied members.
- When agent_delegate is available, omit task to forward the current Run request unchanged, or provide a focused task.
- Each member may appear once per batch. maxParallel limits concurrent execution, not batch size.
- Use only the frozen member roster. Never delegate to yourself or another TEAM Agent.
- Member results return to you; produce the final answer and do not invent successful work.
- If plan tools are available, use them as needed for complex work and keep at most one plan stage in progress.
- In planning mode, describe the plan and wait for approval; delegation is unavailable.`

type MemberSpec struct {
	Key         string `json:"key"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
}

type PromptConfig struct {
	AgentKey     string
	TeamName     string
	Description  string
	Members      []MemberSpec
	SoulPrompt   string
	AgentsPrompt string
	MaxParallel  int
}

func BuildSystemPrompt(config PromptConfig) string {
	maxParallel := NormalizeMaxParallel(config.MaxParallel)
	sections := []string{
		strings.TrimSpace(DefaultSystemPrompt),
		fmt.Sprintf("Team identity:\n- agentKey: %s\n- name: %s\n- description: %s\n- maximum concurrent delegated members: %d",
			fallbackLabel(config.AgentKey), fallbackLabel(config.TeamName), fallbackLabel(config.Description), maxParallel),
		"Team roster (the only valid agentKey values):\n" + RenderRoster(config.Members),
	}
	if value := strings.TrimSpace(config.SoulPrompt); value != "" {
		sections = append(sections, "Team personality guidance (subject to the delegation rules):\n"+value)
	}
	if value := strings.TrimSpace(config.AgentsPrompt); value != "" {
		sections = append(sections, "Team operating guidance (subject to the delegation rules):\n"+value)
	}
	return strings.Join(sections, "\n\n")
}

func RenderRoster(members []MemberSpec) string {
	if len(members) == 0 {
		return "- (no available members)"
	}
	lines := make([]string, 0, len(members))
	seen := map[string]struct{}{}
	for _, member := range members {
		key := strings.TrimSpace(member.Key)
		if key == "" {
			continue
		}
		lookup := strings.ToLower(key)
		if _, ok := seen[lookup]; ok {
			continue
		}
		seen[lookup] = struct{}{}
		parts := []string{"agentKey=" + key}
		if name := strings.TrimSpace(member.Name); name != "" {
			parts = append(parts, "name="+name)
		}
		if role := strings.TrimSpace(member.Role); role != "" {
			parts = append(parts, "role="+role)
		}
		if description := strings.TrimSpace(member.Description); description != "" {
			parts = append(parts, "description="+description)
		}
		lines = append(lines, "- "+strings.Join(parts, "; "))
	}
	if len(lines) == 0 {
		return "- (no available members)"
	}
	return strings.Join(lines, "\n")
}

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), Mode) {
		return ""
	}
	prompt := strings.TrimSpace(session.ModeSystemPrompt)
	if prompt == "" {
		prompt = DefaultSystemPrompt
	}
	if len(toolNames) == 0 {
		toolNames = session.ToolNames
	}
	values := agentcontract.CommonPromptValues(agentcontract.PromptContext{
		AgentKey:           session.AgentKey,
		AgentName:          session.AgentName,
		Mode:               session.Mode,
		PlanningMode:       session.PlanningMode,
		AvailableTools:     toolNames,
		LanguagePreference: session.Locale,
		UserRequest:        req.Message,
	})
	values["agent_key"] = strings.TrimSpace(session.AgentKey)
	return agentcontract.RenderPromptTemplate(prompt, values)
}

func fallbackLabel(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "(not set)"
}
