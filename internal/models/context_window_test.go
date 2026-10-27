package models

import (
	"strings"
	"testing"
)

func TestEffectiveContextWindowTakesTheSmallerLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model int
		agent int
		want  int
	}{
		{name: "neither configured", want: DefaultContextWindow},
		{name: "model only", model: 200000, want: 200000},
		{name: "agent below model", model: 200000, agent: 150000, want: 150000},
		{name: "agent above model", model: 100000, agent: 150000, want: 100000},
		{name: "agent without model limit", agent: 64000, want: 64000},
		{name: "agent above default", agent: 200000, want: DefaultContextWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveContextWindow(ModelDefinition{ContextWindow: tc.model}, tc.agent); got != tc.want {
				t.Fatalf("EffectiveContextWindow(%d, %d) = %d, want %d", tc.model, tc.agent, got, tc.want)
			}
		})
	}
}

func TestValidateAgentMaxInputTokensAgainstModelLimit(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: test", "maxInputTokens: 200000")
	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateAgentMaxInputTokens("mock-model", 200000); err != nil {
		t.Fatalf("limit equal to the model must pass: %v", err)
	}
	if err := registry.ValidateAgentMaxInputTokens("mock-model", 0); err != nil {
		t.Fatalf("unset limit must pass: %v", err)
	}
	if err := registry.ValidateAgentMaxInputTokens("mock-model", 200001); err == nil || !strings.Contains(err.Error(), "exceeds model mock-model limit 200000") {
		t.Fatalf("expected limit error, got %v", err)
	}
	if err := registry.ValidateAgentMaxInputTokens("missing-model", 1000); err == nil {
		t.Fatal("unknown model must fail")
	}
}
