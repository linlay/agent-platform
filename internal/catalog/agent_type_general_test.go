package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentModeAndEngine(t *testing.T) {
	for _, tc := range []struct {
		name, mode, engine   string
		wantMode, wantEngine string
		wantErr              string
	}{
		{name: "default", wantMode: "GENERAL", wantEngine: "native"},
		{name: "general", mode: "general", wantMode: "GENERAL", wantEngine: "native"},
		{name: "legacy react rejected", mode: "REACT", wantErr: "use GENERAL"},
		{name: "mixed case legacy rejected", mode: " ReAcT ", wantErr: "use GENERAL"},
		{name: "native coder", mode: "CODER", engine: "native", wantMode: "CODER", wantEngine: "native"},
		{name: "acp without mode", engine: "acp", wantMode: "CODER", wantEngine: "acp"},
		{name: "acp with coder", mode: "CODER", engine: "ACP", wantMode: "CODER", wantEngine: "acp"},
		{name: "acp with general", mode: "GENERAL", engine: "acp", wantErr: "engine: acp does not take a mode"},
		{name: "acp with kbase", mode: "KBASE", engine: "acp", wantErr: "engine: acp does not take a mode"},
		{name: "unknown engine", engine: "codex", wantErr: "engine must be native or acp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, engine, err := ParseAgentModeAndEngine(tc.mode, tc.engine)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || mode != tc.wantMode || engine != tc.wantEngine {
				t.Fatalf("got (%q, %q, %v), want (%q, %q)", mode, engine, err, tc.wantMode, tc.wantEngine)
			}
		})
	}
}

func TestAgentModeForAPIMapsLegacyReact(t *testing.T) {
	if got := AgentModeForAPI("react"); got != "GENERAL" {
		t.Fatalf("AgentModeForAPI(react) = %q", got)
	}
	if got := NormalizeAgentModeForRuntime("REACT"); got != "GENERAL" {
		t.Fatalf("historical runtime mode = %q", got)
	}
	if got := NormalizeAgentModeForRuntime(""); got != "GENERAL" {
		t.Fatalf("NormalizeAgentModeForRuntime(empty) = %q", got)
	}
	if got := DefinitionRuntimeMode(map[string]any{"engine": "acp"}); got != AgentModeCoder {
		t.Fatalf("DefinitionRuntimeMode(engine acp) = %q", got)
	}
}

func TestParseAgentFileEngineRules(t *testing.T) {
	workspace := t.TempDir()
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "agent.yml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runtime := "runtimeConfig:\n  acpBridgeId: codex\n  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"

	def, err := parseAgentDefinitionForTest(write(t, "key: acp\nengine: acp\n"+runtime))
	if err != nil {
		t.Fatalf("engine: acp without mode: %v", err)
	}
	if def.Mode != AgentModeCoder || def.Engine != AgentEngineACP || !AgentUsesACPCoderBackend(def) || AgentEngineForAPI(def) != "acp" {
		t.Fatalf("unexpected ACP definition: mode=%q engine=%q", def.Mode, def.Engine)
	}

	for name, tc := range map[string]struct{ content, want string }{
		"bridge without engine": {"key: coder\nmode: CODER\n" + runtime, "runtimeConfig.acpBridgeId requires engine: acp"},
		"engine without bridge": {"key: coder\nengine: acp\nruntimeConfig:\n  workspaceRoot: " + filepath.ToSlash(workspace) + "\n", "runtimeConfig.acpBridgeId is required for engine: acp"},
		"acp with general mode": {"key: coder\nmode: GENERAL\nengine: acp\n" + runtime, "engine: acp does not take a mode"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAgentDefinitionForTest(write(t, tc.content)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeEditableDefinitionModeAndEngine(t *testing.T) {
	canonical := normalizeEditableDefinition(map[string]any{"key": "a", "mode": "GENERAL", "engine": "native"})
	if canonical["mode"] != "GENERAL" {
		t.Fatalf("GENERAL should be saved as GENERAL, got %#v", canonical["mode"])
	}
	if _, exists := canonical["engine"]; exists {
		t.Fatalf("default native engine must not be written: %#v", canonical)
	}
	acp := normalizeEditableDefinition(map[string]any{"key": "a", "engine": "ACP"})
	if acp["engine"] != "acp" {
		t.Fatalf("engine = %#v, want acp", acp["engine"])
	}
	if _, exists := acp["mode"]; exists {
		t.Fatalf("engine: acp must not synthesize a mode: %#v", acp)
	}
}

func TestKBaseAgentUsesDeclaredToolsLikeAnyNativeAgent(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "agent.yml")
	content := "key: docs\nmode: KBASE\nmodelConfig:\n  modelKey: mock-model\n" +
		"runtimeConfig:\n  workspaceRoot: " + filepath.ToSlash(workspace) + "\n" +
		"toolConfig:\n  tools:\n    - file_read\n    - web_fetch\n" +
		"skillConfig:\n  skills:\n    - online-docx\n" +
		"kbaseConfig: {}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	def, err := parseAgentDefinitionForTest(path)
	if err != nil {
		t.Fatalf("parse KBASE agent: %v", err)
	}
	// Skills and capability flags do not change the declared tool set.
	for _, tool := range []string{"file_read", "web_fetch"} {
		if !containsString(def.Tools, tool) {
			t.Fatalf("expected tool %s, got %#v", tool, def.Tools)
		}
	}
	if len(def.Tools) != 2 {
		t.Fatalf("undeclared tool was added: %#v", def.Tools)
	}
	if !kbaseAgentHasFileTool(def.Tools) || kbaseAgentHasFileTool([]string{"kbase_search", "datetime"}) {
		t.Fatalf("file tool detection is wrong for %#v", def.Tools)
	}
}

func TestEditableDefinitionRejectsLegacyReact(t *testing.T) {
	for _, mode := range []string{"REACT", "react", " ReAcT "} {
		definition := map[string]any{"key": "demo", "mode": mode, "modelConfig": map[string]any{"modelKey": "test"}}
		if err := validateEditableDefinition("demo", definition); err == nil || !strings.Contains(err.Error(), "use GENERAL") {
			t.Fatalf("mode %q: expected rejection before normalization, got %v", mode, err)
		}
	}
}

func TestImportedDefinitionRejectsLegacyReact(t *testing.T) {
	for _, mode := range []string{"REACT", "react", " ReAcT "} {
		if err := validateImportedAgentDefinitionStructure("demo", map[string]any{"key": "demo", "mode": mode}); err == nil || !strings.Contains(err.Error(), "use GENERAL") {
			t.Fatalf("import mode %q: expected rejection, got %v", mode, err)
		}
	}
}
