package platformcontrol

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
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
	result, err := invokeTestOperation(handler, "catalog.defaults.get", map[string]any{"path": CoderCreationPath})
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
	result, _ := invokeTestOperation(NewToolHandler(config.Config{}, nil), "catalog.defaults.get", map[string]any{"path": CoderCreationPath})
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
		result, _ := invokeTestOperation(NewToolHandler(cfg, nil), "catalog.defaults.get", map[string]any{"path": KBaseCreationPath})
		want := agentkbase.ApplyCreateDefaults(map[string]any{"mode": agentkbase.Mode}, agentkbase.CreateDefaults{
			ModelKey: "answer-model", ReasoningEffort: "MEDIUM", EmbeddingModelKey: "embedding-model",
		})
		if !reflect.DeepEqual(result.Structured["definitionDefaults"], want) || result.Structured["ready"] != true {
			t.Fatalf("unexpected KBASE defaults: %#v", result.Structured)
		}
	})

	t.Run("missing", func(t *testing.T) {
		result, _ := invokeTestOperation(NewToolHandler(config.Config{}, nil), "catalog.defaults.get", map[string]any{"path": KBaseCreationPath})
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
		result, _ := invokeTestOperation(handler, "catalog.defaults.get", map[string]any{"path": path})
		if result.Error != "unsupported_config_path" {
			t.Fatalf("path %q error = %q, want unsupported_config_path", path, result.Error)
		}
	}
}

func TestValidateRequiresConditionalArgumentsAtRuntime(t *testing.T) {
	handler := NewToolHandler(config.Config{}, nil)
	tests := []struct {
		name string
		args map[string]any
		code string
	}{
		{
			name: "missing resource key",
			args: map[string]any{"resourceType": "agent", "content": "key: demo"},
			code: "platform_control_invalid_params",
		},
		{
			name: "missing content",
			args: map[string]any{"resourceType": "agent", "resourceKey": "demo"},
			code: "platform_control_invalid_params",
		},
		{
			name: "missing resource type",
			args: map[string]any{"resourceKey": "demo", "content": "key: demo"},
			code: "platform_control_invalid_params",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := invokeTestOperation(handler, "catalog.validate", tc.args)
			if err != nil {
				t.Fatalf("validate returned error: %v", err)
			}
			if result.Error != tc.code || result.ExitCode != -1 {
				t.Fatalf("validate result = %#v, want error %s", result, tc.code)
			}
		})
	}
}

func TestInvokeStrictlyValidatesFixedEnvelope(t *testing.T) {
	handler := NewToolHandler(config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}, nil)
	for _, tc := range []struct {
		name string
		args map[string]any
		code string
	}{
		{name: "missing operation", args: map[string]any{}, code: "platform_control_invalid_params"},
		{name: "non-string operation", args: map[string]any{"operation": 7}, code: "platform_control_invalid_params"},
		{name: "null params", args: map[string]any{"operation": "capabilities.list", "params": nil}, code: "platform_control_invalid_params"},
		{name: "unknown operation", args: map[string]any{"operation": "future.operation"}, code: "platform_control_invalid_operation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := handler.Invoke(context.Background(), ToolName, tc.args, &contracts.ExecutionContext{})
			if err != nil || result.Error != tc.code {
				t.Fatalf("Invoke() result=%#v err=%v, want %s", result, err, tc.code)
			}
		})
	}
}

func TestValidateCandidateResources(t *testing.T) {
	workspaceRoot := t.TempDir()
	knowledgeRoot := t.TempDir()
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
			content: "key: coder-demo\nname: Demo\nmode: CODER\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: " + workspaceRoot + "\n",
		},
		{
			name: "agent key mismatch", resourceType: "agent", resourceKey: "coder-demo", wantValid: false,
			content: "key: another\nname: Demo\nmode: CODER\nmodelConfig:\n  modelKey: chat-model\nruntimeConfig:\n  workspaceRoot: " + workspaceRoot + "\n",
		},
		{
			name: "agent syntax error", resourceType: "agent", resourceKey: "coder-demo", wantValid: false,
			content: "key: coder-demo\n  broken: value\n",
		},
		{
			name: "kbase agent valid", resourceType: "agent", resourceKey: "kbase-demo", wantValid: true,
			content: "key: kbase-demo\nname: Knowledge\nmode: KBASE\nruntimeConfig:\n  workspaceRoot: " + knowledgeRoot + "\nkbaseConfig:\n  embedding:\n    modelKey: embedding-model\nmodelConfig:\n  modelKey: chat-model\n",
		},
		{
			name: "kbase agent invalid workspace", resourceType: "agent", resourceKey: "kbase-demo", wantValid: false,
			content: "key: kbase-demo\nname: Knowledge\nmode: KBASE\nruntimeConfig:\n  workspaceRoot: @chat\nkbaseConfig:\n  embedding:\n    modelKey: embedding-model\nmodelConfig:\n  modelKey: chat-model\n",
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
			result, err := invokeTestOperation(handler, "catalog.validate", map[string]any{
				"resourceType": tc.resourceType,
				"resourceKey":  tc.resourceKey,
				"content":      tc.content,
			})
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
	result, err := invokeTestOperation(NewToolHandler(config.Config{}, nil), "catalog.validate", map[string]any{
		"resourceType": "mcp-server",
		"resourceKey":  "remote",
		"content":      content,
	})
	if err != nil || result.Error != "" || result.Structured["valid"] != true {
		t.Fatalf("validate secret-bearing candidate failed: result=%#v err=%v", result, err)
	}
	if strings.Contains(result.Output, secret) || strings.Contains(result.Output, content) {
		t.Fatalf("validation response leaked candidate secret or content: %s", result.Output)
	}
}

func TestExplicitToolGrantDoesNotDependOnAgentOrSkills(t *testing.T) {
	cfg := config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}
	handler := NewToolHandler(cfg, nil)
	online := &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "online-office", SkillKeys: []string{"platform-admin"}, MustUseSkills: []string{"platform-admin"}}}
	result, _ := handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "runtime.status"}, online)
	if result.Error != "" {
		t.Fatalf("mounted operation denied: %#v", result)
	}
	other := &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "unbound"}}
	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "runtime.status"}, other)
	if result.Error != "" {
		t.Fatalf("agent-specific authorization remained: %#v", result)
	}
	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "capabilities.list"}, other)
	if result.Error != "" {
		t.Fatalf("capabilities.list denied: %#v", result)
	}
	data := result.Structured["data"].(map[string]any)
	if _, exists := data["profiles"]; exists {
		t.Fatalf("capabilities retained removed profiles: %#v", data)
	}
	operations := data["operations"].([]string)
	for _, operation := range operations {
		if strings.HasPrefix(operation, "run.env.") {
			t.Fatalf("capabilities advertised unavailable run env operation: %#v", data)
		}
	}
	hasOperation := func(target string) bool {
		for _, operation := range operations {
			if operation == target {
				return true
			}
		}
		return false
	}
	if !hasOperation("runtime.status") || !hasOperation("catalog.validate") {
		t.Fatalf("capabilities omitted mounted operations: %#v", data)
	}
}

func TestRunEnvironmentSetUnsetAreValueBlindAndRootScoped(t *testing.T) {
	scope := runenv.NewScope(runenv.Limits{})
	defer scope.Destroy()
	cfg := config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}
	handler := NewToolHandler(cfg, nil)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "run-1", AgentKey: "office"}, RunEnvironment: scope, CurrentToolID: "tool-set"}
	capabilities, _ := handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "capabilities.list", "params": map[string]any{}}, execCtx)
	if capabilities.Error != "" {
		t.Fatalf("capabilities failed: %#v", capabilities)
	}
	capabilityData := capabilities.Structured["data"].(map[string]any)
	capabilityOperations := capabilityData["operations"].([]string)
	for _, required := range []string{"run.env.set", "run.env.unset"} {
		found := false
		for _, operation := range capabilityOperations {
			if operation == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("capabilities omitted %s: %#v", required, capabilityData)
		}
	}
	result, _ := handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "run.env.set", "params": map[string]any{"key": "DOCUMENT_ID", "value": "document-secret-id", "idempotencyKey": "set-doc"}}, execCtx)
	if result.Error != "" || strings.Contains(result.Output, "document-secret-id") {
		t.Fatalf("set result leaked or failed: %#v", result)
	}
	data := result.Structured["data"].(map[string]any)
	if data["key"] != "DOCUMENT_ID" || data["changed"] != true || data["revision"] != uint64(1) {
		t.Fatalf("set result shape = %#v", data)
	}
	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "run.env.unset", "params": map[string]any{"key": "DOCUMENT_ID", "idempotencyKey": "unset-doc"}}, execCtx)
	if result.Error != "" || strings.Contains(result.Output, "document-secret-id") {
		t.Fatalf("unset failed or leaked: %#v", result)
	}
	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "run.env.unset", "params": map[string]any{"key": "DOCUMENT_ID"}}, execCtx)
	if result.Error != "run_env_key_not_set" {
		t.Fatalf("repeated unset = %#v", result)
	}
	child := &contracts.ExecutionContext{Session: contracts.QuerySession{RunID: "run-1", AgentKey: "office", SubTaskID: "child"}, RunEnvironment: scope}
	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{"operation": "run.env.set", "params": map[string]any{"key": "CHILD", "value": "forbidden"}}, child)
	if result.Error != "run_env_mutation_forbidden" {
		t.Fatalf("child mutation = %#v", result)
	}
}

func TestRemovedRunEnvironmentOperationsAreInvalid(t *testing.T) {
	cfg := config.Config{PlatformControl: config.PlatformControlConfig{Enabled: true}}
	handler := NewToolHandler(cfg, nil)
	for _, operation := range []string{"run.env.bind", "run.env.get", "run.env.list", "run.env.bulk"} {
		result, _ := handler.Invoke(context.Background(), ToolName, map[string]any{"operation": operation, "params": map[string]any{}}, &contracts.ExecutionContext{})
		if result.Error != "platform_control_invalid_operation" {
			t.Fatalf("%s result = %#v", operation, result)
		}
	}
}

func TestSecurityExplainIncludesReadAndWritePathDecision(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.Config{AccessPolicy: config.AccessPolicyConfig{}, PlatformControl: config.PlatformControlConfig{Enabled: true}}
	handler := NewToolHandler(cfg, nil)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "admin", WorkspaceRoot: workspace}}
	result, err := handler.Invoke(context.Background(), ToolName, map[string]any{
		"operation": "security.explain",
		"params":    map[string]any{"operation": "run.env.set", "path": filepath.Join(workspace, "report.md"), "access": "write"},
	}, execCtx)
	if err != nil || result.Error != "" {
		t.Fatalf("security.explain failed: result=%#v err=%v", result, err)
	}
	data := result.Structured["data"].(map[string]any)
	pathData := data["path"].(map[string]any)
	if pathData["allowed"] != true || pathData["access"] != "write" || pathData["resolvedPath"] == "" {
		t.Fatalf("unexpected path decision: %#v", pathData)
	}

	result, _ = handler.Invoke(context.Background(), ToolName, map[string]any{
		"operation": "security.explain",
		"params":    map[string]any{"operation": "run.env.set", "path": workspace, "access": "execute"},
	}, execCtx)
	if result.Error != "platform_control_invalid_params" {
		t.Fatalf("invalid access result = %#v", result)
	}
}

func invokeTestOperation(handler *ToolHandler, operation string, params map[string]any) (contracts.ToolExecutionResult, error) {
	handler.cfg.PlatformControl.Enabled = true
	result, err := handler.Invoke(context.Background(), ToolName, map[string]any{"operation": operation, "params": params}, &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "admin"}})
	if data, ok := result.Structured["data"].(map[string]any); ok {
		result.Structured = data
	}
	return result, err
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
func (s stubRegistry) Tools(string) []api.ToolSummary { return nil }
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
