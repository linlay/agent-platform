package tools

import (
	"fmt"
	"strings"
	"testing"
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

	if !byName["_ask_user_question_"] {
		t.Fatal("expected _ask_user_question_ builtin tool definition")
	}
	if byName["confirm_dialog"] {
		t.Fatal("did not expect confirm_dialog to remain a tool name")
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
		case "_ask_user_question_":
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
	questionsField := mapChild(t, questionProperties, "questions")
	questionItem := mapChild(t, mapChild(t, questionsField, "items"), "properties")
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
	required, ok := conditional["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "options" {
		t.Fatalf("expected select conditional to require options, got %#v", conditional["required"])
	}
	questionOptionProperties := mapChild(t, questionOptions, "properties")
	if _, ok := questionOptionProperties["value"]; ok {
		t.Fatal("did not expect value on ask user question options")
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
