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
		{name: "legacy react", mode: "REACT", wantMode: "GENERAL", wantEngine: "native"},
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
	legacy := normalizeEditableDefinition(map[string]any{"key": "a", "mode": "REACT", "engine": "native"})
	if legacy["mode"] != "GENERAL" {
		t.Fatalf("legacy REACT should be saved as GENERAL, got %#v", legacy["mode"])
	}
	if _, exists := legacy["engine"]; exists {
		t.Fatalf("default native engine must not be written: %#v", legacy)
	}
	acp := normalizeEditableDefinition(map[string]any{"key": "a", "engine": "ACP"})
	if acp["engine"] != "acp" {
		t.Fatalf("engine = %#v, want acp", acp["engine"])
	}
	if _, exists := acp["mode"]; exists {
		t.Fatalf("engine: acp must not synthesize a mode: %#v", acp)
	}
}
