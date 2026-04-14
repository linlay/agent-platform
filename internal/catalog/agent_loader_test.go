package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAgentFileSupportsFlattenedToolConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - _datetime_\n" +
		"    - _ask_user_question_\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.Tools) != 2 || def.Tools[0] != "_datetime_" || def.Tools[1] != "_ask_user_question_" {
		t.Fatalf("expected flattened tools list, got %#v", def.Tools)
	}
}

func TestParseAgentFileSupportsLegacyToolConfigBuckets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  backends:\n" +
		"    - _datetime_\n" +
		"  frontends:\n" +
		"    - _ask_user_question_\n" +
		"  actions:\n" +
		"    - _plan_update_task_\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.Tools) != 3 || def.Tools[0] != "_datetime_" || def.Tools[1] != "_ask_user_question_" || def.Tools[2] != "_plan_update_task_" {
		t.Fatalf("expected legacy tool buckets, got %#v", def.Tools)
	}
}

func TestParseAgentFilePrefersFlattenedToolsOverLegacyBuckets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"mode: REACT\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"toolConfig:\n" +
		"  tools:\n" +
		"    - _ask_user_approval_\n" +
		"  backends:\n" +
		"    - _datetime_\n" +
		"  frontends:\n" +
		"    - _ask_user_question_\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.Tools) != 1 || def.Tools[0] != "_ask_user_approval_" {
		t.Fatalf("expected flattened tools to win, got %#v", def.Tools)
	}
}
