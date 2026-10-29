package catalog

import (
	"bytes"
	"testing"
)

func TestAgentAccessConfigLoadsAndStructuredSavesPreserveIt(t *testing.T) {
	definition := map[string]any{"key": "worker", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "test"}, "accessConfig": map[string]any{"default": "auto_approve"}, "interactionConfig": map[string]any{"accessLevel": false}}
	def, _, err := parseAgentTree("agent.yml", definition)
	if err != nil || def.AccessConfig.EffectiveDefault() != "auto_approve" {
		t.Fatalf("def=%+v err=%v", def, err)
	}
	normalized := normalizeEditableDefinition(definition)
	data := renderYAMLMap(normalized)
	if bytes.Index(data, []byte("modelConfig:")) > bytes.Index(data, []byte("accessConfig:")) || bytes.Index(data, []byte("accessConfig:")) > bytes.Index(data, []byte("interactionConfig:")) {
		t.Fatalf("wrong order: %s", data)
	}
	delete(definition, "accessConfig")
	normalized = normalizeEditableDefinition(definition)
	if _, exists := normalized["accessConfig"]; exists {
		t.Fatal("save synthesized a permission override")
	}
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "EXECUTOR"} {
		definition["mode"] = mode
		definition["accessConfig"] = map[string]any{"default": "FULL_ACCESS"}
		if _, _, err := parseAgentTree("agent.yml", definition); err == nil {
			t.Fatalf("%s accepted invalid permission", mode)
		}
	}
}
