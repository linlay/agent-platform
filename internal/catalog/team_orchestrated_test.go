package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestLoadTeamsSupportsOrchestratedDirectoryAndLegacyFile(t *testing.T) {
	root := t.TempDir()
	teamDir := filepath.Join(root, "research")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("mkdir team: %v", err)
	}
	writeTestTeamFile(t, filepath.Join(teamDir, "team.yml"), strings.Join([]string{
		"name: Research Team",
		"description: Multi-agent research",
		"agentKeys:",
		"  - researcher",
		"  - reviewer",
		"orchestrator:",
		"  modelConfig:",
		"    modelKey: coordinator-model",
		"    serviceTier: priority",
		"    reasoning:",
		"      enabled: true",
		"      effort: HIGH",
		"    sampling:",
		"      temperature: 0.25",
		"  budget:",
		"    maxSteps: 18",
		"  maxParallel: 3",
	}, "\n"))
	writeTestTeamFile(t, filepath.Join(teamDir, "SOUL.md"), "Coordinate carefully.")
	writeTestTeamFile(t, filepath.Join(teamDir, "AGENTS.md"), "Use the reviewer after research.")
	writeTestTeamFile(t, filepath.Join(root, "legacy.yml"), strings.Join([]string{
		"name: Legacy Team",
		"description: Existing routing",
		"defaultAgentKey: researcher",
		"agentKeys:",
		"  - researcher",
	}, "\n"))

	teams, err := loadTeams(root)
	if err != nil {
		t.Fatalf("load teams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("teams = %#v, want orchestrated and legacy", teams)
	}
	orchestrated := teams["research"]
	if orchestrated.RuntimeMode != TeamRuntimeModeOrchestrated {
		t.Fatalf("runtime mode = %q", orchestrated.RuntimeMode)
	}
	if orchestrated.Description != "Multi-agent research" || orchestrated.TeamDir != teamDir {
		t.Fatalf("orchestrated definition = %#v", orchestrated)
	}
	if orchestrated.DefaultAgentKey != "" {
		t.Fatalf("orchestrated default agent = %q, want empty", orchestrated.DefaultAgentKey)
	}
	if orchestrated.Orchestrator.ModelKey != "coordinator-model" || orchestrated.Orchestrator.ServiceTier != "priority" || orchestrated.Orchestrator.MaxParallel != 3 {
		t.Fatalf("orchestrator = %#v", orchestrated.Orchestrator)
	}
	if orchestrated.SoulPrompt != "Coordinate carefully." || orchestrated.AgentsPrompt != "Use the reviewer after research." {
		t.Fatalf("prompts = %q / %q", orchestrated.SoulPrompt, orchestrated.AgentsPrompt)
	}
	execute := mapNode(orchestrated.Orchestrator.StageSettings["execute"])
	modelConfig := mapNode(execute["modelConfig"])
	if reasoning := mapNode(modelConfig["reasoning"]); reasoning["effort"] != "HIGH" {
		t.Fatalf("reasoning defaults = %#v", reasoning)
	}
	if sampling := mapNode(modelConfig["sampling"]); sampling["temperature"] != 0.25 {
		t.Fatalf("sampling defaults = %#v", sampling)
	}

	legacy := teams["legacy"]
	if legacy.RuntimeMode != TeamRuntimeModeLegacy || legacy.DefaultAgentKey != "researcher" {
		t.Fatalf("legacy definition = %#v", legacy)
	}
	if legacy.Description != "Existing routing" || legacy.Orchestrator.ModelKey != "" {
		t.Fatalf("legacy metadata = %#v", legacy)
	}
}

func TestOrchestratedTeamValidationAndMaxParallelDefault(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, "valid")
	if err := os.MkdirAll(validDir, 0o755); err != nil {
		t.Fatalf("mkdir valid: %v", err)
	}
	writeTestTeamFile(t, filepath.Join(validDir, "team.yml"), "agentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n")
	valid, err := parseDirectoryTeam(filepath.Join(validDir, "team.yml"), "valid", validDir)
	if err != nil {
		t.Fatalf("parse valid: %v", err)
	}
	if valid.Orchestrator.MaxParallel != TeamDefaultMaxParallel {
		t.Fatalf("default maxParallel = %d", valid.Orchestrator.MaxParallel)
	}

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing orchestrator", content: "agentKeys: [worker]\n", want: "orchestrator is required"},
		{name: "missing model", content: "agentKeys: [worker]\norchestrator:\n  modelConfig: {}\n", want: "modelKey is required"},
		{name: "legacy default", content: "defaultAgentKey: worker\nagentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n", want: "must not configure defaultAgentKey"},
		{name: "zero parallel", content: "agentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n  maxParallel: 0\n", want: "maxParallel must be between"},
		{name: "fractional parallel", content: "agentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n  maxParallel: 1.5\n", want: "maxParallel must be between"},
		{name: "too much parallel", content: "agentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n  maxParallel: 6\n", want: "maxParallel must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "team.yml")
			writeTestTeamFile(t, path, tt.content)
			_, err := parseDirectoryTeam(path, "invalid", filepath.Dir(path))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestTeamReloadRejectsDuplicateIDAndPreservesPreviousSnapshot(t *testing.T) {
	root := t.TempDir()
	writeTestTeamFile(t, filepath.Join(root, "dupe.yml"), "name: Legacy Dupe\ndefaultAgentKey: worker\nagentKeys: [worker]\n")
	dir := filepath.Join(root, "dupe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir duplicate: %v", err)
	}
	writeTestTeamFile(t, filepath.Join(dir, "team.yml"), "name: Directory Dupe\nagentKeys: [worker]\norchestrator:\n  modelConfig:\n    modelKey: coordinator\n")

	registry := &FileRegistry{
		cfg: config.Config{Paths: config.PathsConfig{TeamsDir: root}},
		teams: map[string]TeamDefinition{
			"stable": {TeamID: "stable", Name: "Stable", RuntimeMode: TeamRuntimeModeLegacy},
		},
	}
	err := registry.Reload(context.Background(), "teams")
	if err == nil || !strings.Contains(err.Error(), "duplicate team id \"dupe\"") {
		t.Fatalf("reload error = %v", err)
	}
	if stable, ok := registry.TeamDefinition("stable"); !ok || stable.Name != "Stable" {
		t.Fatalf("previous registry snapshot was replaced: %#v, %v", stable, ok)
	}
	if _, ok := registry.TeamDefinition("dupe"); ok {
		t.Fatal("duplicate team must not be installed")
	}
}

func TestTeamReloadOnlyAffectsSubsequentSnapshots(t *testing.T) {
	root := t.TempDir()
	teamDir := filepath.Join(root, "team")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("mkdir team: %v", err)
	}
	configPath := filepath.Join(teamDir, "team.yml")
	writeTestTeamFile(t, configPath, "name: Before\nagentKeys:\n  - worker\norchestrator:\n  modelConfig:\n    modelKey: model-before\n  maxParallel: 2\n")
	writeTestTeamFile(t, filepath.Join(teamDir, "SOUL.md"), "Before prompt")
	registry := &FileRegistry{
		cfg:    config.Config{Paths: config.PathsConfig{TeamsDir: root}},
		agents: map[string]AgentDefinition{"worker": {Key: "worker"}, "reviewer": {Key: "reviewer"}},
		teams:  map[string]TeamDefinition{},
	}
	if err := registry.Reload(context.Background(), "teams"); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	before, ok := registry.ResolveTeam("team")
	if !ok {
		t.Fatal("resolve initial team")
	}

	writeTestTeamFile(t, configPath, "name: After\nagentKeys:\n  - worker\n  - reviewer\norchestrator:\n  modelConfig:\n    modelKey: model-after\n  maxParallel: 4\n")
	writeTestTeamFile(t, filepath.Join(teamDir, "SOUL.md"), "After prompt")
	if err := registry.Reload(context.Background(), "teams"); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	after, ok := registry.ResolveTeam("team")
	if !ok {
		t.Fatal("resolve reloaded team")
	}
	if before.Name != "Before" || before.Orchestrator.ModelKey != "model-before" || before.Orchestrator.MaxParallel != 2 || before.SoulPrompt != "Before prompt" || len(before.AgentKeys) != 1 {
		t.Fatalf("active snapshot changed after reload: %#v", before)
	}
	if after.Name != "After" || after.Orchestrator.ModelKey != "model-after" || after.Orchestrator.MaxParallel != 4 || after.SoulPrompt != "After prompt" || len(after.AgentKeys) != 2 {
		t.Fatalf("next snapshot did not use reload: %#v", after)
	}
	if before.RosterFingerprint == after.RosterFingerprint || before.ToolSchemaFingerprint == after.ToolSchemaFingerprint || before.OrchestratorFingerprint == after.OrchestratorFingerprint {
		t.Fatalf("fingerprints did not reflect reload: %#v / %#v", before, after)
	}
}

func TestResolveTeamFreezesOrchestratorConfigPromptsAndRoster(t *testing.T) {
	registry := &FileRegistry{
		agents: map[string]AgentDefinition{
			"worker": {Key: "worker", Tools: []string{"file_read"}},
		},
		teams: map[string]TeamDefinition{
			"team": {
				TeamID:       "team",
				Name:         "Team",
				Description:  "Before",
				Icon:         map[string]any{"color": "blue"},
				AgentKeys:    []string{"worker", "missing", "worker"},
				RuntimeMode:  TeamRuntimeModeOrchestrated,
				SoulPrompt:   "Original soul",
				AgentsPrompt: "Original instructions",
				Orchestrator: TeamOrchestratorConfig{
					ModelKey:    "coordinator",
					MaxParallel: 2,
					Budget: map[string]any{
						"tool": map[string]any{"maxCalls": int64(4)},
					},
					StageSettings: map[string]any{
						"execute": map[string]any{"modelConfig": map[string]any{"reasoning": map[string]any{"effort": "HIGH"}}},
					},
				},
			},
		},
	}

	first, ok := registry.ResolveTeam("team")
	if !ok {
		t.Fatal("resolve team")
	}
	if got, want := first.AgentKeys, []string{"worker", "missing"}; !equalStrings(got, want) {
		t.Fatalf("agent keys = %#v, want %#v", got, want)
	}
	if first.RosterFingerprint == "" || first.ToolSchemaFingerprint == "" || first.OrchestratorFingerprint == "" {
		t.Fatalf("fingerprints = %q / %q / %q", first.RosterFingerprint, first.ToolSchemaFingerprint, first.OrchestratorFingerprint)
	}

	first.Icon.(map[string]any)["color"] = "red"
	first.Orchestrator.Budget["tool"].(map[string]any)["maxCalls"] = int64(99)
	first.Orchestrator.StageSettings["execute"].(map[string]any)["changed"] = true
	member, _ := first.AgentDefinition("worker")
	member.Tools[0] = "mutated"
	definition, _ := registry.TeamDefinition("team")
	definition.AgentKeys[0] = "mutated"
	definition.Orchestrator.Budget["tool"].(map[string]any)["maxCalls"] = int64(88)

	second, _ := registry.ResolveTeam("team")
	if second.Icon.(map[string]any)["color"] != "blue" {
		t.Fatalf("icon leaked through snapshot: %#v", second.Icon)
	}
	if second.Orchestrator.Budget["tool"].(map[string]any)["maxCalls"] != int64(4) {
		t.Fatalf("budget leaked through snapshot: %#v", second.Orchestrator.Budget)
	}
	if _, exists := second.Orchestrator.StageSettings["execute"].(map[string]any)["changed"]; exists {
		t.Fatalf("stage settings leaked through snapshot: %#v", second.Orchestrator.StageSettings)
	}
	secondMember, _ := second.AgentDefinition("worker")
	if secondMember.Tools[0] != "file_read" || second.AgentKeys[0] != "worker" {
		t.Fatalf("member or roster leaked: %#v / %#v", secondMember, second.AgentKeys)
	}
	if second.RosterFingerprint != first.RosterFingerprint || second.ToolSchemaFingerprint != first.ToolSchemaFingerprint || second.OrchestratorFingerprint != first.OrchestratorFingerprint {
		t.Fatalf("stable fingerprints changed: %#v / %#v", first, second)
	}
}

func TestTeamsSummaryExposesSafeOrchestratedMetadata(t *testing.T) {
	registry := &FileRegistry{
		agents: map[string]AgentDefinition{"worker": {Key: "worker"}},
		teams: map[string]TeamDefinition{
			"team": {
				TeamID:      "team",
				Name:        "Team",
				Description: "Description",
				AgentKeys:   []string{"worker", "missing"},
				RuntimeMode: TeamRuntimeModeOrchestrated,
				SoulPrompt:  "secret prompt",
				Orchestrator: TeamOrchestratorConfig{
					ModelKey:    "private-model",
					MaxParallel: 4,
				},
			},
		},
	}
	items := registry.Teams()
	if len(items) != 1 {
		t.Fatalf("teams = %#v", items)
	}
	item := items[0]
	if item.Description != "Description" || item.RuntimeMode != TeamRuntimeModeOrchestrated {
		t.Fatalf("summary = %#v", item)
	}
	if item.Meta["maxParallel"] != 4 || item.Meta["orchestrated"] != true {
		t.Fatalf("meta = %#v", item.Meta)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	serialized := string(data)
	for _, secret := range []string{"private-model", "secret prompt", "orchestratorFingerprint"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("summary leaked %q: %s", secret, serialized)
		}
	}
}

func writeTestTeamFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
