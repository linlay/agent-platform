package engine

import (
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
)

func TestBuildSystemPromptRespectsContextTagOrder(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		RunID:         "run_1",
		ChatID:        "chat_1",
		AgentKey:      "agent_1",
		AgentName:     "Agent One",
		ContextTags:   []string{"execution_policy", "skills", "agent_identity", "run_session"},
		SkillKeys:     []string{"skill_a", "skill_b"},
		Budget:        map[string]any{"runTimeoutMs": 1000},
		StageSettings: map[string]any{"phase": "alpha"},
	}, api.QueryRequest{}, "mock-model")

	agentIdx := strings.Index(prompt, "Agent Identity:")
	runIdx := strings.Index(prompt, "Run Session:")
	skillsIdx := strings.Index(prompt, "Skills:")
	policyIdx := strings.Index(prompt, "Execution Policy:")
	if !(agentIdx >= 0 && runIdx > agentIdx && skillsIdx > runIdx && policyIdx > skillsIdx) {
		t.Fatalf("expected ordered prompt sections, got %q", prompt)
	}
}
