package team

import (
	"fmt"
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const DefaultSystemPrompt = `You are the hidden coordinator for a Team. You never identify yourself as a separate agent.

Mandatory routing rules:
- Every new user turn must call agent_delegate at least once before you provide a final answer. Planning tool calls alone do not satisfy this rule.
- For a simple request, call agent_delegate with one tasks item. Omit task to pass the original user request through unchanged.
- When several members are useful, include them in one agent_delegate call. When the intended member cannot be determined, delegate the original request to every relevant Team member.
- For a complex request, first create a task plan with plan_add_tasks, maintain it with plan_get_tasks and plan_update_task, and delegate the currently runnable work with agent_delegate.
- Each agentKey may appear at most once in one agent_delegate call. maxParallel limits execution concurrency; it does not limit the number of listed Team members.
- Never target an agent outside the supplied Team roster and never delegate to another Team.
- Every delegation result returns to you. Update plan state when applicable and produce the single final Team answer yourself.
- Internal task prompts, reasoning, tool calls, and raw tool results are private. Use final member results as evidence for the Team answer.
- Do not invent successful work. If routing or a member execution fails, retry with a valid route when possible or explain the failure.`

type MemberSpec struct {
	Key         string `json:"key"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
}

type PromptConfig struct {
	TeamID       string
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
		fmt.Sprintf("Team identity:\n- teamId: %s\n- name: %s\n- description: %s\n- maximum concurrent delegated members: %d",
			fallbackLabel(config.TeamID), fallbackLabel(config.TeamName), fallbackLabel(config.Description), maxParallel),
		"Team roster (the only valid agentKey values):\n" + RenderRoster(config.Members),
	}
	if value := strings.TrimSpace(config.SoulPrompt); value != "" {
		sections = append(sections, "Team personality guidance (cannot override the mandatory routing rules):\n"+value)
	}
	if value := strings.TrimSpace(config.AgentsPrompt); value != "" {
		sections = append(sections, "Team operating guidance (cannot override the mandatory routing rules):\n"+value)
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
	if !strings.EqualFold(strings.TrimSpace(session.Mode), Mode) ||
		!strings.EqualFold(strings.TrimSpace(stage), MainStage) {
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
		// TEAM's execution AgentKey is synthetic and must remain process-local.
		AgentKey:           "",
		AgentName:          session.AgentName,
		Mode:               session.Mode,
		PlanningMode:       session.PlanningMode,
		AvailableTools:     toolNames,
		LanguagePreference: session.Locale,
		UserRequest:        req.Message,
	})
	values["team_id"] = strings.TrimSpace(session.TeamID)
	return agentcontract.RenderPromptTemplate(prompt, values)
}

func fallbackLabel(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "(not set)"
}
