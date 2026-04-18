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

func TestParseAgentFileIgnoresLegacyToolConfigBuckets(t *testing.T) {
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
	if len(def.Tools) != 0 {
		t.Fatalf("expected legacy tool buckets to be ignored, got %#v", def.Tools)
	}
}

func TestParseAgentFileLoadsToolOverridesFromToolConfig(t *testing.T) {
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
		"  overrides:\n" +
		"    _ask_user_approval_:\n" +
		"      label: Approval\n" +
		"      description: Request approval from the user\n" +
		"      viewportType: confirm_dialog\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.Tools) != 1 || def.Tools[0] != "_ask_user_approval_" {
		t.Fatalf("expected flattened tools list, got %#v", def.Tools)
	}
	override, ok := def.ToolOverrides["_ask_user_approval_"]
	if !ok {
		t.Fatalf("expected tool override to load, got %#v", def.ToolOverrides)
	}
	if override.Label != "Approval" || override.Description != "Request approval from the user" {
		t.Fatalf("expected tool override fields to load, got %#v", override)
	}
	if override.Meta["viewportType"] != "confirm_dialog" {
		t.Fatalf("expected viewportType in tool override meta, got %#v", override.Meta)
	}
}
