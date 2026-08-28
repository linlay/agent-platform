package llm

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	platformtools "agent-platform/internal/tools"
)

func TestFilterToolDefinitionsRequiresExplicitAllowlist(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{Name: "datetime"},
		{Name: "vision_recognize", Meta: map[string]any{"explicitOnly": true}},
	}

	for _, allowed := range [][]string{nil, {}} {
		if filtered := filterToolDefinitions(defs, allowed); len(filtered) != 0 {
			t.Fatalf("empty allowlist must expose no tools, got %#v", filtered)
		}
	}

	filtered := filterToolDefinitions(defs, []string{"vision_recognize"})
	if len(filtered) != 1 || filtered[0].Name != "vision_recognize" {
		t.Fatalf("expected explicit tool when allowed by name, got %#v", filtered)
	}
}

func TestFilterToolDefinitionsRequiresExplicitPlatformControlGrant(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{Name: "datetime"},
		{Name: "platform_control", Meta: map[string]any{"explicitOnly": true}},
	}

	if filtered := filterToolDefinitions(defs, nil); len(filtered) != 0 {
		t.Fatalf("empty allowlist must hide every tool, got %#v", filtered)
	}
	if filtered := filterToolDefinitions(defs, []string{"datetime"}); len(filtered) != 1 || filtered[0].Name != "datetime" {
		t.Fatalf("platform_control must be hidden from unrelated explicit grants, got %#v", filtered)
	}
	if filtered := filterToolDefinitions(defs, []string{"platform_control"}); len(filtered) != 1 || filtered[0].Name != "platform_control" {
		t.Fatalf("platform_control explicit grant was not honored, got %#v", filtered)
	}
}

func TestPlatformControlSchemaIsByteIdenticalAcrossAgents(t *testing.T) {
	defs, err := platformtools.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	var onlineSchema []byte
	for _, session := range []contracts.QuerySession{
		{AgentKey: "online-office", SkillKeys: []string{"online-docx"}},
		{AgentKey: "cutej", SkillKeys: []string{"platform-admin"}},
	} {
		effective := effectiveToolDefinitions(defs, []string{"platform_control"}, session)
		if len(effective) != 1 || effective[0].Name != "platform_control" {
			t.Fatalf("unexpected platform_control definition for %s: %#v", session.AgentKey, effective)
		}
		encoded, err := json.Marshal(effective[0])
		if err != nil {
			t.Fatalf("marshal definition for %s: %v", session.AgentKey, err)
		}
		if session.AgentKey == "online-office" {
			onlineSchema = encoded
			continue
		}
		if string(encoded) != string(onlineSchema) {
			t.Fatalf("platform_control schema differs by Agent/Skill\nonline=%s\nadmin=%s", onlineSchema, encoded)
		}
	}
}

func TestEffectiveToolDefinitionsUseSandboxBashSchema(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{
			Key:         "bash",
			Name:        "bash",
			Description: "host bash",
			Parameters:  map[string]any{"properties": map[string]any{"command": map[string]any{}}},
		},
		{
			Key:         "bash_sandbox",
			Name:        "bash_sandbox",
			Description: "sandbox bash",
			Parameters:  map[string]any{"properties": map[string]any{"command": map[string]any{}, "description": map[string]any{}}},
		},
	}

	hostDefs := effectiveToolDefinitions(defs, []string{"bash"}, contracts.QuerySession{
		WorkspaceRoot: "/workspace",
	})
	if len(hostDefs) != 1 || hostDefs[0].Name != "bash" || hostDefs[0].Description != "host bash" {
		t.Fatalf("expected host bash definition, got %#v", hostDefs)
	}

	sandboxDefs := effectiveToolDefinitions(defs, []string{"bash"}, contracts.QuerySession{
		AgentHasRuntimeSandbox: true,
		WorkspaceRoot:          "/workspace",
	})
	if len(sandboxDefs) != 1 {
		t.Fatalf("expected one sandbox bash definition, got %#v", sandboxDefs)
	}
	if sandboxDefs[0].Name != "bash" || sandboxDefs[0].Key != "bash" {
		t.Fatalf("expected sandbox schema to remain exposed as bash, got %#v", sandboxDefs[0])
	}
	if sandboxDefs[0].Description != "sandbox bash" {
		t.Fatalf("expected sandbox bash description, got %#v", sandboxDefs[0])
	}
	properties, _ := sandboxDefs[0].Parameters["properties"].(map[string]any)
	if _, ok := properties["description"]; !ok {
		t.Fatalf("expected sandbox bash parameters to include description, got %#v", sandboxDefs[0].Parameters)
	}

	emptySandboxDefs := effectiveToolDefinitions(defs, nil, contracts.QuerySession{
		AgentHasRuntimeSandbox: true,
		WorkspaceRoot:          "/workspace",
	})
	if len(emptySandboxDefs) != 0 {
		t.Fatalf("empty sandbox allowlist must expose no tools, got %#v", emptySandboxDefs)
	}
}

func TestResolveAllowedToolNamesDistinguishesInheritedAndExplicitEmpty(t *testing.T) {
	session := contracts.QuerySession{
		Mode:      "CODER",
		ToolNames: []string{"bash"},
	}

	inherited := resolveAllowedToolNames(session, "coder", nil)
	for _, name := range []string{"bash", "plan_add_tasks", "plan_get_tasks", "plan_update_task"} {
		if !containsToolName(inherited, name) {
			t.Fatalf("inherited CODER tools missing %q: %#v", name, inherited)
		}
	}
	if explicitEmpty := resolveAllowedToolNames(session, "coder", []string{}); len(explicitEmpty) != 0 {
		t.Fatalf("explicit empty stage allowlist must disable all tools, got %#v", explicitEmpty)
	}
}

func TestExplicitEmptyStageAllowlistRejectsCachedToolSpecs(t *testing.T) {
	cached := toOpenAIToolSpecs([]api.ToolDetailResponse{{Name: "datetime"}})
	if cachedToolsCompatibleWithStageOverride([]string{}, cached) {
		t.Fatal("cached tool specs must not override an explicit empty stage allowlist")
	}
	if !cachedToolsCompatibleWithStageOverride(nil, cached) {
		t.Fatal("nil stage allowlist should inherit the prepared session cache")
	}
	if !cachedToolsCompatibleWithStageOverride([]string{"datetime"}, cached) {
		t.Fatal("non-empty stage allowlist retains existing mode-specific cache behavior")
	}
}

func TestEffectiveToolDefinitionsRequireExplicitRootsWithoutWorkspace(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{
			Name:        "bash",
			Description: "run shell",
			Parameters: map[string]any{
				"type":     "object",
				"required": []any{"command"},
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
					"cwd":     map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "file_glob",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "file_grep",
			Parameters: map[string]any{
				"type":     "object",
				"required": []any{"pattern"},
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string"},
				},
			},
		},
		{
			Name: "file_read",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "path"},
				},
			},
		},
		{
			Name:        "artifact_publish",
			Description: "publish artifact",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"artifacts": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"path": map[string]any{"type": "string", "description": "path"},
							},
						},
					},
				},
			},
		},
		{
			Name: "vision_recognize",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"images": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"file_path": map[string]any{"type": "string", "description": "path"},
							},
						},
					},
				},
			},
		},
	}

	allowed := []string{"bash", "file_glob", "file_grep", "file_read", "artifact_publish", "vision_recognize"}
	workspaceLess := effectiveToolDefinitions(defs, allowed, contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: "/runtime/chats/chat-1"},
		},
	})
	for _, toolName := range []string{"bash", "file_glob", "file_grep"} {
		definition := toolDefinitionByName(t, workspaceLess, toolName)
		required := schemaRequiredSet(definition.Parameters)
		parameterName := "path"
		if toolName == "bash" {
			parameterName = "cwd"
		}
		if !required[parameterName] {
			t.Fatalf("expected %s.%s to be required without Workspace, got %#v", toolName, parameterName, definition.Parameters)
		}
	}
	readDefinition := toolDefinitionByName(t, workspaceLess, "file_read")
	readProperties, _ := readDefinition.Parameters["properties"].(map[string]any)
	filePath, _ := readProperties["file_path"].(map[string]any)
	description, _ := filePath["description"].(string)
	for _, expected := range []string{"@chat", "@skills", "@temp", "relative paths", "@workspace"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("expected file_read.file_path description to contain %q, got %q", expected, description)
		}
	}
	artifactDefinition := toolDefinitionByName(t, workspaceLess, "artifact_publish")
	artifactPathDescription := nestedSchemaDescription(
		artifactDefinition.Parameters,
		"properties", "artifacts", "items", "properties", "path",
	)
	if !strings.Contains(artifactPathDescription, "@chat/...") ||
		!strings.Contains(artifactPathDescription, "@temp/...") ||
		!strings.Contains(artifactDefinition.Description, "Artifact sources must use explicit @chat/... or @temp/... paths") {
		t.Fatalf("expected Workspace-less artifact source contract, got %#v", artifactDefinition)
	}
	visionDefinition := toolDefinitionByName(t, workspaceLess, "vision_recognize")
	visionPathDescription := nestedSchemaDescription(
		visionDefinition.Parameters,
		"properties", "images", "items", "properties", "file_path",
	)
	if !strings.Contains(visionPathDescription, "reference_name") ||
		!strings.Contains(visionPathDescription, "relative paths") {
		t.Fatalf("expected Workspace-less vision file path contract, got %#v", visionDefinition)
	}

	// The workspace-less contract is session-local: neither the source catalog
	// definition nor a later Workspace session may inherit its required fields.
	if schemaRequiredSet(defs[0].Parameters)["cwd"] || schemaRequiredSet(defs[1].Parameters)["path"] {
		t.Fatalf("source definitions were mutated: %#v", defs)
	}
	if got := nestedSchemaDescription(defs[4].Parameters, "properties", "artifacts", "items", "properties", "path"); got != "path" {
		t.Fatalf("nested source artifact schema was mutated: %q", got)
	}
	withWorkspace := effectiveToolDefinitions(defs, allowed, contracts.QuerySession{
		WorkspaceRoot: "/workspace",
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: "/workspace", ChatDir: "/runtime/chats/chat-2"},
		},
	})
	if schemaRequiredSet(toolDefinitionByName(t, withWorkspace, "bash").Parameters)["cwd"] {
		t.Fatalf("Workspace session unexpectedly requires bash.cwd: %#v", withWorkspace)
	}
	if schemaRequiredSet(toolDefinitionByName(t, withWorkspace, "file_glob").Parameters)["path"] {
		t.Fatalf("Workspace session unexpectedly requires file_glob.path: %#v", withWorkspace)
	}

	filesystemRoot := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	rootWorkspace := effectiveToolDefinitions(defs, allowed, contracts.QuerySession{
		WorkspaceRoot: filesystemRoot,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: filesystemRoot, ChatDir: "/runtime/chats/chat-3"},
		},
	})
	for _, toolName := range []string{"file_glob", "file_grep"} {
		definition := toolDefinitionByName(t, rootWorkspace, toolName)
		if !schemaRequiredSet(definition.Parameters)["path"] {
			t.Fatalf("root Workspace must require %s.path: %#v", toolName, definition.Parameters)
		}
		if description := nestedSchemaDescription(definition.Parameters, "properties", "path"); !strings.Contains(description, "filesystem root") || !strings.Contains(description, "narrower") {
			t.Fatalf("root Workspace path guidance is not actionable for %s: %q", toolName, description)
		}
	}
	if schemaRequiredSet(toolDefinitionByName(t, rootWorkspace, "bash").Parameters)["cwd"] {
		t.Fatalf("root Workspace file-search hardening must not require bash.cwd: %#v", rootWorkspace)
	}
	for _, toolName := range []string{"file_glob", "file_grep"} {
		if schemaRequiredSet(toolDefinitionByName(t, defs, toolName).Parameters)["path"] {
			t.Fatalf("root Workspace session mutated the source definition for %s: %#v", toolName, defs)
		}
	}
	afterRootWorkspace := effectiveToolDefinitions(defs, allowed, contracts.QuerySession{WorkspaceRoot: "/workspace-after-root"})
	for _, toolName := range []string{"file_glob", "file_grep"} {
		if schemaRequiredSet(toolDefinitionByName(t, afterRootWorkspace, toolName).Parameters)["path"] {
			t.Fatalf("root Workspace hardening leaked into a later ordinary session for %s: %#v", toolName, afterRootWorkspace)
		}
	}
}

func containsToolName(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func nestedSchemaDescription(schema map[string]any, path ...string) string {
	current := schema
	for _, name := range path {
		next, _ := current[name].(map[string]any)
		if next == nil {
			return ""
		}
		current = next
	}
	description, _ := current["description"].(string)
	return description
}

func toolDefinitionByName(t *testing.T, definitions []api.ToolDetailResponse, name string) api.ToolDetailResponse {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}
	t.Fatalf("missing tool definition %q in %#v", name, definitions)
	return api.ToolDetailResponse{}
}

func schemaRequiredSet(parameters map[string]any) map[string]bool {
	required := make(map[string]bool)
	switch values := parameters["required"].(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	case []string:
		for _, name := range values {
			required[name] = true
		}
	}
	return required
}
