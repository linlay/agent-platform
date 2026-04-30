package llm

import (
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func TestBuildInjectedPromptPayloadIncludesEstimatedTokens(t *testing.T) {
	messages := []openAIMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "assistant", Content: "previous answer"},
		{Role: "user", Content: "show debug"},
	}

	payload := buildInjectedPromptPayload(
		contracts.QuerySession{
			AgentKey:            "a",
			AgentName:           "A",
			AgentRole:           "assistant",
			Mode:                "REACT",
			ContextTags:         []string{"session"},
			AgentsPrompt:        "stage instructions",
			SkillCatalogPrompt:  "skill catalog",
			StableMemoryContext: "Runtime Context: Stable Memory\n- stable",
			ResolvedStageSettings: contracts.PlanExecuteSettings{
				Execute: contracts.StageSettings{SystemPrompt: "stage system"},
			},
			AgentHasMemoryConfig: true,
		},
		api.QueryRequest{Message: "show debug"},
		PromptBuildOptions{},
		messages,
	)
	if payload == nil {
		t.Fatal("expected payload")
	}

	if got, _ := payload["systemPromptTokens"].(int); got <= 0 {
		t.Fatalf("expected systemPromptTokens > 0, got %#v", payload["systemPromptTokens"])
	}
	if got, _ := payload["historyMessagesTokens"].(int); got <= 0 {
		t.Fatalf("expected historyMessagesTokens > 0, got %#v", payload["historyMessagesTokens"])
	}
	if got, _ := payload["currentUserMessageTokens"].(int); got <= 0 {
		t.Fatalf("expected currentUserMessageTokens > 0, got %#v", payload["currentUserMessageTokens"])
	}
	if got, _ := payload["providerMessagesTokens"].(int); got <= 0 {
		t.Fatalf("expected providerMessagesTokens > 0, got %#v", payload["providerMessagesTokens"])
	}
	systemSections, _ := payload["systemSections"].([]map[string]any)
	if len(systemSections) == 0 {
		t.Fatalf("expected systemSections, got %#v", payload["systemSections"])
	}

	providerMessages, _ := payload["providerMessages"].([]any)
	if len(providerMessages) != 3 {
		t.Fatalf("expected 3 provider messages, got %#v", providerMessages)
	}
	first, _ := providerMessages[0].(map[string]any)
	if got, _ := first["estimatedTokens"].(int); got <= 0 {
		t.Fatalf("expected estimatedTokens on message, got %#v", first)
	}
}
