package tools

import "testing"

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
	if !byName["_ask_user_approval_"] {
		t.Fatal("expected _ask_user_approval_ builtin tool definition")
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

	var questionDef, approvalDef map[string]any
	for _, def := range defs {
		switch def.Name {
		case "_ask_user_question_":
			questionDef = def.Parameters
			if def.Meta["viewportType"] != "builtin" || def.Meta["viewportKey"] != "confirm_dialog" {
				t.Fatalf("unexpected question tool metadata: %#v", def.Meta)
			}
		case "_ask_user_approval_":
			approvalDef = def.Parameters
			if def.Meta["viewportType"] != "builtin" || def.Meta["viewportKey"] != "confirm_dialog" {
				t.Fatalf("unexpected approval tool metadata: %#v", def.Meta)
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
	questionOptionProperties := mapChild(t, questionOptions, "properties")
	if _, ok := questionOptionProperties["value"]; ok {
		t.Fatal("did not expect value on ask user question options")
	}

	approvalProperties := mapChild(t, approvalDef, "properties")
	if !enumContains(t, approvalProperties["mode"], "approval") {
		t.Fatal("expected ask user approval mode=approval")
	}
	approvalQuestions := mapChild(t, approvalProperties, "questions")
	approvalItem := mapChild(t, mapChild(t, approvalQuestions, "items"), "properties")
	if _, ok := approvalItem["allowFreeText"]; !ok {
		t.Fatal("expected allowFreeText on ask user approval items")
	}
	if _, ok := approvalItem["freeTextPlaceholder"]; !ok {
		t.Fatal("expected freeTextPlaceholder on ask user approval items")
	}
	approvalOptions := mapChild(t, mapChild(t, approvalItem, "options"), "items")
	approvalOptionProperties := mapChild(t, approvalOptions, "properties")
	if _, ok := approvalOptionProperties["value"]; !ok {
		t.Fatal("expected value on ask user approval options")
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
