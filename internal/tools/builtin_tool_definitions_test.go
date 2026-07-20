package tools

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestLoadEmbeddedToolDefinitionsIncludesAskUserBuiltins(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	byName := make(map[string]bool, len(defs))
	for _, def := range defs {
		byName[def.Name] = true
	}

	if !byName["ask_user_question"] {
		t.Fatal("expected ask_user_question builtin tool definition")
	}
	if !byName["finalize_planning"] {
		t.Fatal("expected finalize_planning builtin tool definition")
	}
	if byName["planning"+"_write"] {
		t.Fatal("did not expect removed planning alias to remain a tool name")
	}
	if byName["confirm_dialog"] {
		t.Fatal("did not expect confirm_dialog to remain a tool name")
	}
	if !byName["agent_invoke"] {
		t.Fatal("expected agent_invoke builtin tool definition")
	}
	if !byName["regex"] {
		t.Fatal("expected regex builtin tool definition")
	}
	if !byName["web_fetch"] {
		t.Fatal("expected web_fetch builtin tool definition")
	}
	if !byName["image_generate"] {
		t.Fatal("expected image_generate builtin tool definition")
	}
}

func TestLoadEmbeddedToolDefinitionsAppliesBuiltinToolCatalogVisibility(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	visibleNames := map[string]bool{
		"agent_invoke": true, "artifact_publish": true, "ask_user_question": true,
		"bash": true, "bash_sandbox": true, "datetime": true,
		"desktop_action": true, "desktop_cdp": true,
		"file_edit": true, "file_glob": true, "file_grep": true, "file_read": true, "file_write": true,
		"finalize_planning": true, "image_generate": true,
		"kbase_files": true, "kbase_read": true, "kbase_refresh": true, "kbase_search": true, "kbase_status": true,
		"plan_add_tasks": true, "plan_get_tasks": true, "plan_update_task": true,
		"platform_config": true, "regex": true, "vision_recognize": true, "web_fetch": true,
	}
	for _, def := range defs {
		visible, ok := def.Meta["catalogVisible"].(bool)
		if !ok {
			t.Fatalf("builtin tool %q is missing catalogVisible metadata: %#v", def.Name, def.Meta)
		}
		if visible != visibleNames[def.Name] {
			t.Errorf("builtin tool %q catalogVisible = %t, want %t", def.Name, visible, visibleNames[def.Name])
		}
	}
	for _, hiddenName := range []string{
		"agent_delegate", "_session_search_", "_skill_candidate_list_", "_skill_candidate_write_",
		"memory_timeline", "memory_update", "memory_write", "memory_read", "memory_promote", "memory_search", "memory_consolidate", "memory_forget",
	} {
		if visibleNames[hiddenName] {
			t.Fatalf("hidden builtin tool %q was allowlisted", hiddenName)
		}
	}
}

func TestApplyBuiltinToolCatalogVisibilityData(t *testing.T) {
	baseDefs := []api.ToolDetailResponse{
		{Name: "bash", Meta: map[string]any{}},
		{Name: "memory_search", Meta: map[string]any{}},
	}

	t.Run("applies a case insensitive allowlist", func(t *testing.T) {
		defs := []api.ToolDetailResponse{
			{Name: "bash", Meta: map[string]any{}},
			{Name: "memory_search", Meta: map[string]any{}},
		}
		err := applyBuiltinToolCatalogVisibilityData(defs, []byte("visibleBuiltinTools:\n  - BASH\n"))
		if err != nil {
			t.Fatalf("apply visibility: %v", err)
		}
		if defs[0].Meta["catalogVisible"] != true || defs[1].Meta["catalogVisible"] != false {
			t.Fatalf("unexpected catalog visibility: %#v", defs)
		}
	})

	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "missing list", data: "other: []\n", want: "requires visibleBuiltinTools"},
		{name: "empty name", data: "visibleBuiltinTools:\n  - '   '\n", want: "must not be empty"},
		{name: "duplicate name", data: "visibleBuiltinTools:\n  - bash\n  - BASH\n", want: "duplicate tool"},
		{name: "unknown name", data: "visibleBuiltinTools:\n  - not_a_tool\n", want: "unknown tool"},
		{name: "non string", data: "visibleBuiltinTools:\n  - 7\n", want: "must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defs := []api.ToolDetailResponse{
				{Name: baseDefs[0].Name, Meta: map[string]any{}},
				{Name: baseDefs[1].Name, Meta: map[string]any{}},
			}
			err := applyBuiltinToolCatalogVisibilityData(defs, []byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("apply visibility error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEmbeddedAgentDelegateSchemaAndInternalMetadata(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	for _, def := range defs {
		if def.Name != "agent_delegate" {
			continue
		}
		if def.Meta["clientVisible"] != true || def.Meta["explicitOnly"] != true || def.Meta["internalOnly"] != true || def.Meta["catalogVisible"] != false {
			t.Fatalf("unexpected agent_delegate metadata: %#v", def.Meta)
		}
		if def.Parameters["additionalProperties"] != false || !reflect.DeepEqual(def.Parameters["required"], []any{"tasks"}) {
			t.Fatalf("unexpected agent_delegate root schema: %#v", def.Parameters)
		}
		properties := mapChild(t, def.Parameters, "properties")
		tasks := mapChild(t, properties, "tasks")
		if contracts.AnyIntNode(tasks["minItems"]) != 1 {
			t.Fatalf("agent_delegate tasks minItems=%#v", tasks["minItems"])
		}
		items := mapChild(t, tasks, "items")
		if items["additionalProperties"] != false || !reflect.DeepEqual(items["required"], []any{"agentKey"}) {
			t.Fatalf("unexpected agent_delegate task schema: %#v", items)
		}
		itemProperties := mapChild(t, items, "properties")
		for _, name := range []string{"agentKey", "task", "taskName", "files"} {
			if _, ok := itemProperties[name]; !ok {
				t.Fatalf("agent_delegate task is missing %q: %#v", name, itemProperties)
			}
		}
		files := mapChild(t, itemProperties, "files")
		if contracts.AnyIntNode(files["maxItems"]) != 10 {
			t.Fatalf("agent_delegate files maxItems=%#v", files["maxItems"])
		}
		return
	}
	t.Fatal("embedded agent_delegate definition is unavailable")
}

func TestWebFetchToolSchemaMatchesContract(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var webFetchDef map[string]any
	for _, def := range defs {
		if def.Name == "web_fetch" {
			webFetchDef = def.Parameters
			if def.Meta["submitResultFormat"] != "json-compact" {
				t.Fatalf("unexpected submit result format: %#v", def.Meta)
			}
			break
		}
	}
	if webFetchDef == nil {
		t.Fatal("expected web_fetch builtin tool definition")
	}
	properties := mapChild(t, webFetchDef, "properties")
	for _, want := range []string{"url", "prompt", "profile"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("expected web_fetch property %q", want)
		}
	}
	required, ok := webFetchDef["required"].([]any)
	if !ok {
		t.Fatalf("expected web_fetch required array, got %#v", webFetchDef["required"])
	}
	if len(required) != 2 || required[0] != "url" || required[1] != "prompt" {
		t.Fatalf("unexpected web_fetch required fields: %#v", required)
	}
}

func TestImageGenerateToolSchemaMatchesContract(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var imageGenerateDef map[string]any
	for _, def := range defs {
		if def.Name == "image_generate" {
			imageGenerateDef = def.Parameters
			if def.Meta["submitResultFormat"] != "json-compact" || def.Meta["explicitOnly"] != true {
				t.Fatalf("unexpected image_generate metadata: %#v", def.Meta)
			}
			break
		}
	}
	if imageGenerateDef == nil {
		t.Fatal("expected image_generate builtin tool definition")
	}
	properties := mapChild(t, imageGenerateDef, "properties")
	for _, want := range []string{"prompt", "profile", "size", "response_format", "n"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("expected image_generate property %q", want)
		}
	}
	if !enumContains(t, properties["response_format"], "b64_json") || !enumContains(t, properties["response_format"], "url") {
		t.Fatalf("expected image_generate response_format enum, got %#v", properties["response_format"])
	}
	required, ok := imageGenerateDef["required"].([]any)
	if !ok {
		t.Fatalf("expected image_generate required array, got %#v", imageGenerateDef["required"])
	}
	if len(required) != 1 || required[0] != "prompt" {
		t.Fatalf("unexpected image_generate required fields: %#v", required)
	}
}

func TestRegexToolSchemaMatchesContract(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var regexDef map[string]any
	for _, def := range defs {
		if def.Name == "regex" {
			regexDef = def.Parameters
			break
		}
	}
	if regexDef == nil {
		t.Fatal("expected regex builtin tool definition")
	}

	properties := mapChild(t, regexDef, "properties")
	for _, want := range []string{"count", "matches"} {
		if !enumContains(t, properties["operation"], want) {
			t.Fatalf("expected regex operation enum to include %q", want)
		}
	}
	for _, want := range []string{"operation", "text", "pattern"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("expected regex property %q", want)
		}
	}
	required, ok := regexDef["required"].([]any)
	if !ok {
		t.Fatalf("expected regex required array, got %#v", regexDef["required"])
	}
	if len(required) != 3 || required[0] != "operation" || required[1] != "text" || required[2] != "pattern" {
		t.Fatalf("unexpected regex required fields: %#v", required)
	}
}

func TestAskUserToolSchemasMatchContract(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var questionDef map[string]any
	for _, def := range defs {
		switch def.Name {
		case "ask_user_question":
			questionDef = def.Parameters
			if def.Meta["kind"] != "frontend" {
				t.Fatalf("unexpected question tool metadata: %#v", def.Meta)
			}
		}
	}

	questionProperties := mapChild(t, questionDef, "properties")
	if !enumContains(t, questionProperties["mode"], "question") {
		t.Fatal("expected ask user question mode=question")
	}
	if _, hasTimeout := questionProperties["timeout"]; hasTimeout {
		t.Fatal("did not expect ask_user_question root property timeout")
	}
	if _, ok := questionDef["required"].([]any); !ok {
		t.Fatalf("expected ask_user_question required array, got %#v", questionDef["required"])
	}
	questionsField := mapChild(t, questionProperties, "questions")
	questionItem := mapChild(t, mapChild(t, questionsField, "items"), "properties")
	questionType := questionItem["type"]
	if !enumContains(t, questionType, "date") {
		t.Fatal("expected ask user question type enum to include date")
	}
	if !enumContains(t, questionType, "datetime") {
		t.Fatal("expected ask user question type enum to include datetime")
	}
	if _, ok := questionItem["allowFreeText"]; !ok {
		t.Fatal("expected allowFreeText on ask user question items")
	}
	if _, ok := questionItem["freeTextPlaceholder"]; !ok {
		t.Fatal("expected freeTextPlaceholder on ask user question items")
	}
	questionOptions := mapChild(t, mapChild(t, questionItem, "options"), "items")
	questionOptionsSchema := mapChild(t, questionItem, "options")
	if fmt.Sprint(questionOptionsSchema["minItems"]) != "1" {
		t.Fatalf("expected ask user question options minItems=1, got %#v", questionOptionsSchema["minItems"])
	}
	allOf, ok := mapChild(t, questionsField, "items")["allOf"].([]any)
	if !ok || len(allOf) == 0 {
		t.Fatalf("expected conditional schema on ask user question items, got %#v", mapChild(t, questionsField, "items")["allOf"])
	}
	conditional := mapChild(t, allOf[0].(map[string]any), "then")
	condRequired, ok := conditional["required"].([]any)
	if !ok || len(condRequired) != 1 || condRequired[0] != "options" {
		t.Fatalf("expected select conditional to require options, got %#v", conditional["required"])
	}
	questionOptionProperties := mapChild(t, questionOptions, "properties")
	if _, ok := questionOptionProperties["value"]; ok {
		t.Fatal("did not expect value on ask user question options")
	}
	if _, ok := questionOptionProperties["description"]; !ok {
		t.Fatal("expected description on ask user question options")
	}
	if _, ok := questionOptionProperties["previewHtml"]; !ok {
		t.Fatal("expected previewHtml on ask user question options")
	}
	if _, ok := questionOptionProperties["recommended"]; !ok {
		t.Fatal("expected recommended on ask user question options")
	}
	if questionOptionProperties["recommended"].(map[string]any)["type"] != "boolean" {
		t.Fatal("expected recommended type boolean")
	}
	if questionOptions["additionalProperties"] != false {
		t.Fatalf("expected ask user question options additionalProperties=false, got %#v", questionOptions["additionalProperties"])
	}
}

func TestFileGrepOutputModeEnumIsSchemaArray(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var fileGrepDef map[string]any
	for _, def := range defs {
		if def.Name == "file_grep" {
			fileGrepDef = def.Parameters
			break
		}
	}
	if fileGrepDef == nil {
		t.Fatal("expected file_grep builtin tool definition")
	}

	properties := mapChild(t, fileGrepDef, "properties")
	outputMode := properties["output_mode"]
	for _, want := range []string{"content", "files_with_matches", "count"} {
		if !enumContains(t, outputMode, want) {
			t.Fatalf("expected file_grep output_mode enum to include %q", want)
		}
	}
}

func TestFileGlobSchemaIncludesRequiredPattern(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var fileGlobDef map[string]any
	for _, def := range defs {
		if def.Name == "file_glob" {
			fileGlobDef = def.Parameters
			break
		}
	}
	if fileGlobDef == nil {
		t.Fatal("expected file_glob builtin tool definition")
	}

	properties := mapChild(t, fileGlobDef, "properties")
	if _, ok := properties["pattern"]; !ok {
		t.Fatal("expected file_glob pattern property")
	}
	required, ok := fileGlobDef["required"].([]any)
	if !ok {
		t.Fatalf("expected file_glob required array, got %#v", fileGlobDef["required"])
	}
	if len(required) != 1 || required[0] != "pattern" {
		t.Fatalf("expected file_glob to require pattern, got %#v", required)
	}
}

func TestKBaseFilesSchemaIncludesModeAndStatusEnums(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	var kbaseFilesDef map[string]any
	for _, def := range defs {
		if def.Name == "kbase_files" {
			kbaseFilesDef = def.Parameters
			break
		}
	}
	if kbaseFilesDef == nil {
		t.Fatal("expected kbase_files builtin tool definition")
	}

	properties := mapChild(t, kbaseFilesDef, "properties")
	for _, want := range []string{"files", "tree"} {
		if !enumContains(t, properties["mode"], want) {
			t.Fatalf("expected kbase_files mode enum to include %q", want)
		}
	}
	for _, want := range []string{"active", "skipped", "error", "deleted", "all"} {
		if !enumContains(t, properties["status"], want) {
			t.Fatalf("expected kbase_files status enum to include %q", want)
		}
	}
}

func TestKBaseToolReadOnlyMetadata(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	want := map[string]bool{
		"kbase_search":  true,
		"kbase_files":   true,
		"kbase_read":    true,
		"kbase_status":  true,
		"kbase_refresh": false,
	}
	seen := map[string]bool{}
	for _, def := range defs {
		expected, ok := want[def.Name]
		if !ok {
			continue
		}
		actual, exists := def.Meta["readOnly"].(bool)
		if !exists || actual != expected {
			t.Fatalf("expected %s readOnly=%t, got %#v", def.Name, expected, def.Meta["readOnly"])
		}
		seen[def.Name] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("missing KBASE tool definitions: got %#v want %#v", seen, want)
	}
}

func TestPlatformConfigSchemaUsesExactReadAllowlist(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	var platformConfig api.ToolDetailResponse
	for _, def := range defs {
		if def.Name == "platform_config" {
			platformConfig = def
			break
		}
	}
	if platformConfig.Name == "" {
		t.Fatal("expected platform_config builtin tool definition")
	}
	if platformConfig.Meta["readOnly"] != true {
		t.Fatalf("platform_config readOnly = %#v, want true", platformConfig.Meta["readOnly"])
	}
	if platformConfig.Meta["explicitOnly"] != true {
		t.Fatalf("platform_config explicitOnly = %#v, want true", platformConfig.Meta["explicitOnly"])
	}
	properties := mapChild(t, platformConfig.Parameters, "properties")
	path, ok := properties["path"].(map[string]any)
	if !ok {
		t.Fatalf("platform_config path schema = %#v", properties["path"])
	}
	if got, want := path["enum"], []any{"agents.creation.coder", "agents.creation.kbase"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("platform_config path enum = %#v, want %#v", got, want)
	}
}

func TestEmbeddedToolDescriptionsAreEnglishFriendlyAndComplete(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	for _, def := range defs {
		if strings.TrimSpace(def.Description) == "" {
			t.Fatalf("expected non-empty top-level description for %s", def.Name)
		}
		if err := validateSchemaDescriptions(def.Parameters, ""); err != nil {
			t.Fatalf("validate descriptions for %s: %v", def.Name, err)
		}
	}
}

func mapChild(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("expected %q to be an object, got %#v", key, parent[key])
	}
	return child
}

func enumContains(t *testing.T, field any, want string) bool {
	t.Helper()
	mapped, ok := field.(map[string]any)
	if !ok {
		t.Fatalf("expected field to be an object, got %#v", field)
	}
	items, ok := mapped["enum"].([]any)
	if !ok {
		t.Fatalf("expected enum to be an array, got %#v", mapped["enum"])
	}
	for _, item := range items {
		if text, ok := item.(string); ok && text == want {
			return true
		}
	}
	return false
}

func validateSchemaDescriptions(schema map[string]any, path string) error {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return nil
	}

	requiredSet := map[string]bool{}
	if required, ok := schema["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				requiredSet[name] = true
			}
		}
	}

	for name, rawChild := range properties {
		child, ok := rawChild.(map[string]any)
		if !ok {
			return fmt.Errorf("property %s must be an object schema, got %#v", formatSchemaPath(path, name), rawChild)
		}
		description := strings.TrimSpace(stringValue(child["description"]))
		if description == "" {
			return fmt.Errorf("property %s is missing description", formatSchemaPath(path, name))
		}
		if requiredSet[name] && !strings.HasPrefix(description, "Required.") {
			return fmt.Errorf("required property %s must start with \"Required.\", got %q", formatSchemaPath(path, name), description)
		}
		if err := validateNestedSchemaDescriptions(child, formatSchemaPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func validateNestedSchemaDescriptions(schema map[string]any, path string) error {
	if err := validateSchemaDescriptions(schema, path); err != nil {
		return err
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if err := validateSchemaDescriptions(items, path+"[]"); err != nil {
			return err
		}
	}
	return nil
}

func formatSchemaPath(prefix string, name string) string {
	if strings.TrimSpace(prefix) == "" {
		return name
	}
	return prefix + "." + name
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
