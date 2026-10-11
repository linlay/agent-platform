package runops

import (
	"agent-platform/internal/config"
	"agent-platform/internal/resources"
	"testing"
)

func TestChatStartSchemaKeepsNewArgumentsOptional(t *testing.T) {
	data, err := resources.ToolFS.ReadFile("tools/chat_start.yml")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	definition := tree.(map[string]any)
	schema := definition["inputSchema"].(map[string]any)
	required := schema["required"].([]any)
	if len(required) != 1 || required[0] != "message" {
		t.Fatalf("unexpected required fields: %#v", required)
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["teamId"]; ok {
		t.Fatal("chat_start still advertises teamId")
	}
	if _, ok := properties["agentKey"]; !ok {
		t.Fatal("chat_start is missing optional agentKey")
	}

	for _, key := range []string{"accessLevel", "mustUseSkills", "chatName", "modelKey", "reasoningEffort"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	level := properties["accessLevel"].(map[string]any)
	enum, ok := level["enum"].([]any)
	if !ok || len(enum) != 3 || enum[0] != "default" || enum[1] != "auto_approve" || enum[2] != "full_access" {
		t.Fatalf("invalid accessLevel enum: %#v", level["enum"])
	}
	if schema["additionalProperties"] != false {
		t.Fatal("unknown properties permitted")
	}
}
