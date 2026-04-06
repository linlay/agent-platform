package catalog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldLoadRuntimeNameMatchesJavaSemantics(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "demoModePlain", want: true},
		{name: "agent.demo", want: true},
		{name: "agent.demo.yml", want: true},
		{name: "agent.example", want: false},
		{name: "agent.example.yml", want: false},
		{name: ".hidden.example", want: false},
		{name: "sample.demo.yaml", want: true},
	}
	for _, tc := range cases {
		if got := ShouldLoadRuntimeName(tc.name); got != tc.want {
			t.Fatalf("ShouldLoadRuntimeName(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLogicalRuntimeBaseNameStripsDemoAndExampleMarkers(t *testing.T) {
	cases := map[string]string{
		"auth.yml":            "auth",
		"auth.demo.yml":       "auth",
		"auth.example.yml":    "auth",
		"owner.example":       "owner",
		"viewport.demo.yaml":  "viewport",
		"provider.production": "provider",
		"plain":               "plain",
	}
	for input, want := range cases {
		if got := LogicalRuntimeBaseName(input); got != want {
			t.Fatalf("LogicalRuntimeBaseName(%q)=%q want %q", input, got, want)
		}
	}
}

func TestParseAgentFileReadsContextTagsBudgetAndStageSettings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextTags:\n"+
			"  - execution_policy\n"+
			"  - agent_identity\n"+
			"budget:\n"+
			"  runTimeoutMs: 1000\n"+
			"stageSettings:\n"+
			"  stage: alpha\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.ContextTags) != 2 || def.Budget["runTimeoutMs"] != int64(1000) && def.Budget["runTimeoutMs"] != 1000 {
		t.Fatalf("expected parsed context tags and budget, got %#v", def)
	}
	if def.StageSettings["stage"] != "alpha" {
		t.Fatalf("expected stage settings, got %#v", def.StageSettings)
	}
}

func TestLoadTeamsSupportsYAMLAndSkipsExampleFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "default.yaml"), []byte(
		"name: Default Team\n"+
			"defaultAgentKey: runner\n"+
			"agentKeys:\n"+
			"  - runner\n",
	), 0o644); err != nil {
		t.Fatalf("write yaml team: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "example.example.yml"), []byte(
		"name: Example Team\n"+
			"agentKeys:\n"+
			"  - runner\n",
	), 0o644); err != nil {
		t.Fatalf("write example team: %v", err)
	}

	teams, err := loadTeams(root)
	if err != nil {
		t.Fatalf("load teams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected one loadable team, got %#v", teams)
	}
	if _, ok := teams["default"]; !ok {
		t.Fatalf("expected default team to load, got %#v", teams)
	}
	if _, ok := teams["example.example"]; ok {
		t.Fatalf("did not expect example team to load, got %#v", teams)
	}
}

func TestTeamsLogsInvalidAgentKeys(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	registry := &FileRegistry{
		agents: map[string]AgentDefinition{
			"agent_a": {Key: "agent_a"},
		},
		teams: map[string]TeamDefinition{
			"team_a": {
				TeamID:          "team_a",
				Name:            "Team A",
				AgentKeys:       []string{"agent_a", "missing_agent"},
				DefaultAgentKey: "agent_a",
			},
		},
	}

	items := registry.Teams()
	if len(items) != 1 {
		t.Fatalf("expected one team summary, got %#v", items)
	}
	if !strings.Contains(buf.String(), "invalidAgentKeys=[missing_agent]") {
		t.Fatalf("expected invalid agent key warning, got %q", buf.String())
	}
}
