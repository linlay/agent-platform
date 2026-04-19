package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		"    - _ask_user_question_\n" +
		"  overrides:\n" +
		"    _ask_user_question_:\n" +
		"      label: Ask\n" +
		"      description: Ask the user a question\n" +
		"      viewportType: builtin\n" +
		"      viewportKey: question_dialog\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.Tools) != 1 || def.Tools[0] != "_ask_user_question_" {
		t.Fatalf("expected flattened tools list, got %#v", def.Tools)
	}
	override, ok := def.ToolOverrides["_ask_user_question_"]
	if !ok {
		t.Fatalf("expected tool override to load, got %#v", def.ToolOverrides)
	}
	if override.Label != "Ask" || override.Description != "Ask the user a question" {
		t.Fatalf("expected tool override fields to load, got %#v", override)
	}
	if override.Meta["viewportType"] != "builtin" || override.Meta["viewportKey"] != "question_dialog" {
		t.Fatalf("expected viewport metadata in tool override meta, got %#v", override.Meta)
	}
}

func TestParseAgentFileLoadsSandboxEnv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	content := "" +
		"key: demo\n" +
		"name: Demo\n" +
		"modelConfig:\n" +
		"  modelKey: demo-model\n" +
		"sandboxConfig:\n" +
		"  env:\n" +
		"    HTTP_PROXY: http://127.0.0.1:7890\n" +
		"    EMPTY: \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	got, ok := def.Sandbox["env"].(map[string]string)
	if !ok {
		t.Fatalf("expected sandbox env map[string]string, got %#v", def.Sandbox["env"])
	}
	want := map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:7890",
		"EMPTY":      "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox env = %#v, want %#v", got, want)
	}
}

func TestParseAgentFileRejectsInvalidSandboxEnv(t *testing.T) {
	tests := []struct {
		name        string
		envValue    any
		errContains string
	}{
		{
			name:        "env must be map",
			envValue:    []any{"HTTP_PROXY"},
			errContains: "sandboxConfig.env must be a map[string]string",
		},
		{
			name: "value must be string",
			envValue: map[string]any{
				"HTTP_PROXY": int64(7890),
			},
			errContains: `sandboxConfig.env["HTTP_PROXY"] must be a string`,
		},
		{
			name: "key must not be empty",
			envValue: map[string]any{
				"": "value",
			},
			errContains: "sandboxConfig.env contains an empty key",
		},
		{
			name: "key must not contain whitespace",
			envValue: map[string]any{
				"BAD KEY": "value",
			},
			errContains: `sandboxConfig.env key "BAD KEY" must not contain whitespace`,
		},
		{
			name: "key must not contain equals",
			envValue: map[string]any{
				"BAD=KEY": "value",
			},
			errContains: `sandboxConfig.env key "BAD=KEY" must not contain '='`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSandboxEnv(tt.envValue)
			if err == nil {
				t.Fatal("expected parseSandboxEnv error")
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.errContains)
			}
		})
	}
}
