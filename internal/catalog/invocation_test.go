package catalog

import "testing"

func TestAgentInvocationEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  AgentDefinition
		want bool
	}{
		{"nav", AgentDefinition{Mode: "GENERAL"}, false},
		{"invoke", AgentDefinition{Mode: "GENERAL", VisibilityScopes: []string{"invoke"}}, true},
		{"internal", AgentDefinition{Mode: "CODER", VisibilityScopes: []string{"internal"}}, true},
		{"nested", AgentDefinition{Mode: "GENERAL", VisibilityScopes: []string{"invoke"}, Tools: []string{" AGENT_INVOKE "}}, false},
		{"unsupported", AgentDefinition{Mode: "CHANNEL", VisibilityScopes: []string{"invoke"}}, false},
		{"kbase", AgentDefinition{Mode: "KBASE", VisibilityScopes: []string{"invoke"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentInvocationError(tc.def) == nil; got != tc.want {
				t.Fatalf("invocable=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestExecutionBackendDoesNotInferNativeFromAbsenceOfProxy(t *testing.T) {
	for _, tc := range []struct {
		def  AgentDefinition
		want string
	}{
		{AgentDefinition{Mode: "GENERAL"}, "native"}, {AgentDefinition{Mode: "CODER", Engine: AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}}, "acp"},
		{AgentDefinition{Mode: "PROXY"}, "proxy"}, {AgentDefinition{Mode: "CHANNEL"}, "channel"},
	} {
		if got := ExecutionBackend(tc.def); got != tc.want {
			t.Errorf("backend=%s want %s", got, tc.want)
		}
	}
	def := AgentDefinition{Mode: "CODER", Engine: AgentEngineACP, Runtime: map[string]any{"engineId": "fixture/agent"}, VisibilityScopes: []string{"invoke"}}
	capabilities := ResolvedModeCapabilities(def)
	if !capabilities.RunAsChild || capabilities.InvokeChildren || AgentInvocationError(def) != nil {
		t.Fatalf("external child capability=%+v", capabilities)
	}
}
