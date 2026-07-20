package platformconfig

import (
	"context"
	"reflect"
	"strings"
	"testing"

	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func TestGetCoderCreationDefaultsMatchesModeCreateDefaults(t *testing.T) {
	cfg := config.Config{
		CoderSettings: config.CoderSettingsConfig{
			DefaultAgent: config.CoderDefaultAgentConfig{
				ModelKey:        "coder-model",
				ReasoningEffort: "HIGH",
				Budget:          map[string]any{"maxSteps": 88},
			},
			ACPBridges: map[string]config.CoderACPBridgeConfig{
				"secret-bridge": {AuthToken: "sk-do-not-leak"},
			},
		},
		ResourceTicket: config.ResourceTicketConfig{Secret: "resource-ticket-secret"},
		ContainerHub:   config.ContainerHubConfig{AuthToken: "container-hub-secret"},
		Gateways:       []config.GatewayEntry{{ID: "private-gateway", JwtToken: "gateway-jwt-secret"}},
	}
	handler := NewToolHandler(cfg, nil)
	result, err := handler.Invoke(context.Background(), ToolName, map[string]any{
		"action": "get",
		"path":   CoderCreationPath,
	}, nil)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("get coder defaults failed: result=%#v err=%v", result, err)
	}
	want := agentcoder.ApplyCreateDefaults(map[string]any{"mode": agentcoder.Mode}, agentcoder.CreateDefaults{
		ModelKey: "coder-model", ReasoningEffort: "HIGH", Budget: map[string]any{"maxSteps": 88},
	})
	if got := result.Structured["definitionDefaults"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitionDefaults = %#v, want %#v", got, want)
	}
	if result.Structured["ready"] != true {
		t.Fatalf("ready = %#v, want true", result.Structured["ready"])
	}
	for _, secret := range []string{"sk-do-not-leak", "secret-bridge", "resource-ticket-secret", "container-hub-secret", "gateway-jwt-secret"} {
		if strings.Contains(result.Output, secret) {
			t.Fatalf("response leaked sensitive configuration %q: %s", secret, result.Output)
		}
	}
}

func TestGetCoderCreationDefaultsReportsMissingModel(t *testing.T) {
	result, _ := NewToolHandler(config.Config{}, nil).Invoke(context.Background(), ToolName, map[string]any{
		"action": "get", "path": CoderCreationPath,
	}, nil)
	if result.Structured["ready"] != false {
		t.Fatalf("ready = %#v, want false", result.Structured["ready"])
	}
	if got, want := result.Structured["missingFields"], []string{"modelConfig.modelKey"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missingFields = %#v, want %#v", got, want)
	}
}

func TestGetKBaseCreationDefaultsAndMissingFields(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		cfg := config.Config{KBase: config.KBaseConfig{
			DefaultAgent: config.KBaseDefaultAgentConfig{ModelKey: "answer-model", ReasoningEffort: "MEDIUM"},
			Embedding:    config.KBaseEmbeddingConfig{ModelKey: "embedding-model"},
		}}
		result, _ := NewToolHandler(cfg, nil).Invoke(context.Background(), ToolName, map[string]any{
			"action": "get", "path": KBaseCreationPath,
		}, nil)
		want := agentkbase.ApplyCreateDefaults(map[string]any{"mode": agentkbase.Mode}, agentkbase.CreateDefaults{
			ModelKey: "answer-model", ReasoningEffort: "MEDIUM", EmbeddingModelKey: "embedding-model",
		})
		if !reflect.DeepEqual(result.Structured["definitionDefaults"], want) || result.Structured["ready"] != true {
			t.Fatalf("unexpected KBASE defaults: %#v", result.Structured)
		}
	})

	t.Run("missing", func(t *testing.T) {
		result, _ := NewToolHandler(config.Config{}, nil).Invoke(context.Background(), ToolName, map[string]any{
			"action": "get", "path": KBaseCreationPath,
		}, nil)
		if result.Structured["ready"] != false {
			t.Fatalf("ready = %#v, want false", result.Structured["ready"])
		}
		missing, _ := result.Structured["missingFields"].([]string)
		want := []string{"modelConfig.modelKey", "kbaseConfig.embedding.modelKey"}
		if !reflect.DeepEqual(missing, want) {
			t.Fatalf("missingFields = %#v, want %#v", missing, want)
		}
	})
}

func TestGetRejectsEveryNonAllowlistedPath(t *testing.T) {
	handler := NewToolHandler(config.Config{}, nil)
	for _, path := range []string{"agents.creation", "agents.creation.coder.modelConfig.modelKey", "paths.agentsDir", "*", ""} {
		result, _ := handler.Invoke(context.Background(), ToolName, map[string]any{"action": "get", "path": path}, nil)
		if result.Error != "unsupported_config_path" {
			t.Fatalf("path %q error = %q, want unsupported_config_path", path, result.Error)
		}
	}
}

func TestValidateCandidateResources(t *testing.T) {
	registry := stubRegistry{agents: map[string]catalog.AgentDefinition{
		"member": {Key: "member", Mode: "REACT", ModelKey: "chat-model"},
	}}
	handler := NewToolHandler(config.Config{Skills: config.SkillCatalogConfig{MaxPromptChars: 8000}}, registry)

	tests := []struct {
		name         string
		resourceType string
		resourceKey  string
		content      string
		wantValid    bool
	}{
		{
			name: "agent valid", resourceType: "agent", resourceKey: "coder-demo", wantValid: true,
			content: "key: coder-demo\nname: Demo\nmode: CODER\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: '@chat'\n",
		},
		{
			name: "agent key mismatch", resourceType: "agent", resourceKey: "coder-demo", wantValid: false,
			content: "key: another\nname: Demo\nmode: CODER\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: '@chat'\n",
		},
		{
			name: "agent syntax error", resourceType: "agent", resourceKey: "coder-demo", wantValid: false,
			content: "key: coder-demo\n  broken: value\n",
		},
		{
			name: "kbase agent valid", resourceType: "agent", resourceKey: "kbase-demo", wantValid: true,
			content: "key: kbase-demo\nname: Knowledge\nmode: KBASE\nkbaseConfig:\n  embedding:\n    modelKey: embedding-model\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: /tmp/knowledge\n",
		},
		{
			name: "kbase agent invalid workspace", resourceType: "agent", resourceKey: "kbase-demo", wantValid: false,
			content: "key: kbase-demo\nname: Knowledge\nmode: KBASE\nkbaseConfig:\n  embedding:\n    modelKey: embedding-model\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: '@chat'\n",
		},
		{
			name: "team valid", resourceType: "team", resourceKey: "research", wantValid: true,
			content: "name: Research\nagentKeys:\n  - member\norchestrator:\n  modelConfig:\n    modelKey: chat-model\n",
		},
		{
			name: "team unknown member", resourceType: "team", resourceKey: "research", wantValid: false,
			content: "name: Research\nagentKeys:\n  - missing\norchestrator:\n  modelConfig:\n    modelKey: chat-model\n",
		},
		{
			name: "team syntax error", resourceType: "team", resourceKey: "research", wantValid: false,
			content: "name: Research\n  broken: value\n",
		},
		{
			name: "team empty members", resourceType: "team", resourceKey: "research", wantValid: false,
			content: "name: Research\nagentKeys: []\norchestrator:\n  modelConfig:\n    modelKey: chat-model\n",
		},
		{
			name: "team invalid concurrency", resourceType: "team", resourceKey: "research", wantValid: false,
			content: "name: Research\nagentKeys:\n  - member\norchestrator:\n  modelConfig:\n    modelKey: chat-model\n  maxParallel: 6\n",
		},
		{
			name: "skill valid", resourceType: "skill", resourceKey: "demo-skill", wantValid: true,
			content: "---\nname: demo-skill\ndescription: Demo skill\n---\n\n# Demo\n\nFollow the workflow.\n",
		},
		{
			name: "skill missing frontmatter", resourceType: "skill", resourceKey: "demo-skill", wantValid: false,
			content: "# Demo\n",
		},
		{
			name: "skill frontmatter syntax error", resourceType: "skill", resourceKey: "demo-skill", wantValid: false,
			content: "---\nname: demo-skill\n  broken: value\ndescription: Demo\n---\n\n# Demo\n",
		},
		{
			name: "mcp valid", resourceType: "mcp-server", resourceKey: "remote", wantValid: true,
			content: "serverKey: remote\ntransport: streamable-http\nbaseUrl: http://127.0.0.1:8080/mcp\n",
		},
		{
			name: "mcp mixed transports", resourceType: "mcp-server", resourceKey: "remote", wantValid: false,
			content: "serverKey: remote\ntransport: streamable-http\nbaseUrl: http://127.0.0.1:8080/mcp\ncommand: node\n",
		},
		{
			name: "mcp key mismatch", resourceType: "mcp-server", resourceKey: "remote", wantValid: false,
			content: "serverKey: another\ntransport: streamable-http\nbaseUrl: http://127.0.0.1:8080/mcp\n",
		},
		{
			name: "mcp syntax error", resourceType: "mcp-server", resourceKey: "remote", wantValid: false,
			content: "serverKey: remote\n  broken: value\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := handler.Invoke(context.Background(), ToolName, map[string]any{
				"action":       "validate",
				"resourceType": tc.resourceType,
				"resourceKey":  tc.resourceKey,
				"content":      tc.content,
			}, nil)
			if err != nil || result.Error != "" {
				t.Fatalf("validate failed: result=%#v err=%v", result, err)
			}
			if got, _ := result.Structured["valid"].(bool); got != tc.wantValid {
				t.Fatalf("valid = %v, want %v; diagnostics=%#v", got, tc.wantValid, result.Structured["diagnostics"])
			}
			if strings.Contains(result.Output, tc.content) {
				t.Fatalf("validation response echoed candidate content")
			}
		})
	}
}

func TestValidateDoesNotEchoCandidateSecrets(t *testing.T) {
	const secret = "mcp-secret-value-that-must-not-leak"
	content := "serverKey: remote\ntransport: streamable-http\nbaseUrl: http://127.0.0.1:8080/mcp\nauthToken: " + secret + "\n"
	result, err := NewToolHandler(config.Config{}, nil).Invoke(context.Background(), ToolName, map[string]any{
		"action":       "validate",
		"resourceType": "mcp-server",
		"resourceKey":  "remote",
		"content":      content,
	}, nil)
	if err != nil || result.Error != "" || result.Structured["valid"] != true {
		t.Fatalf("validate secret-bearing candidate failed: result=%#v err=%v", result, err)
	}
	if strings.Contains(result.Output, secret) || strings.Contains(result.Output, content) {
		t.Fatalf("validation response leaked candidate secret or content: %s", result.Output)
	}
}

type stubRegistry struct {
	agents map[string]catalog.AgentDefinition
}

func (s stubRegistry) Agents(string) []api.AgentSummary { return nil }
func (s stubRegistry) Teams() []api.TeamSummary         { return nil }
func (s stubRegistry) Skills(string) []api.SkillSummary { return nil }
func (s stubRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}
func (s stubRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (s stubRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (s stubRegistry) DefaultAgentKey() string { return "" }
func (s stubRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := s.agents[key]
	return def, ok
}
func (s stubRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (s stubRegistry) Reload(context.Context, string) error { return nil }
