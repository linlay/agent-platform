package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/deprecation"
)

func loadTeams(root string) (map[string]TeamDefinition, error) {
	items := map[string]TeamDefinition{}
	sources := map[string]string{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !ShouldLoadRuntimeName(name) {
			continue
		}

		var def TeamDefinition
		var source string
		if entry.IsDir() {
			teamDir := filepath.Join(root, name)
			configPath := resolveDirectoryTeamConfig(teamDir)
			if configPath == "" {
				log.Printf("[catalog][teams] skip directory %s: no team.yml or team.yaml found", name)
				continue
			}
			source = configPath
			def, err = parseDirectoryTeam(configPath, name, teamDir)
		} else {
			lower := strings.ToLower(name)
			if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
				continue
			}
			source = filepath.Join(root, name)
			teamID := strings.TrimSuffix(name, filepath.Ext(name))
			return nil, deprecation.New("legacy flat Team definition %q was removed; migrate it to runtime/teams/%s/team.yml with an orchestrator", source, teamID)
		}
		if err != nil {
			if deprecation.Is(err) {
				return nil, err
			}
			log.Printf("[catalog][teams] skip source %s: parse error: %v", source, err)
			continue
		}

		if previous, exists := sources[def.TeamID]; exists {
			return nil, fmt.Errorf("duplicate team id %q from %s and %s", def.TeamID, previous, source)
		}
		sources[def.TeamID] = source
		items[def.TeamID] = def
	}
	return items, nil
}

func resolveDirectoryTeamConfig(teamDir string) string {
	for _, name := range []string{"team.yml", "team.yaml"} {
		path := filepath.Join(teamDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func parseDirectoryTeam(path string, teamID string, teamDir string) (TeamDefinition, error) {
	return parseTeamConfig(path, strings.TrimSpace(teamID), teamDir)
}

func parseTeamConfig(path string, teamID string, teamDir string) (TeamDefinition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return TeamDefinition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return TeamDefinition{}, fmt.Errorf("team file must be a map")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return TeamDefinition{}, fmt.Errorf("team id is required")
	}

	def := TeamDefinition{
		TeamID:      teamID,
		Name:        defaultString(stringNode(root["name"]), teamID),
		Description: stringNode(root["description"]),
		Icon:        cloneAgentSnapshotValue(root["icon"]),
		AgentKeys:   append([]string(nil), listStrings(root["agentKeys"])...),
		RuntimeMode: TeamRuntimeModeOrchestrated,
		TeamDir:     strings.TrimSpace(teamDir),
	}
	if _, exists := root["defaultAgentKey"]; exists {
		return TeamDefinition{}, deprecation.New("Team defaultAgentKey was removed; configure runtime/teams/<teamId>/team.yml with an orchestrator instead")
	}
	if _, exists := root["runtimeMode"]; exists {
		return TeamDefinition{}, deprecation.New("Team runtimeMode was removed; directory Teams are always orchestrated")
	}

	orchestrator, err := parseTeamOrchestrator(path, mapNode(root["orchestrator"]))
	if err != nil {
		return TeamDefinition{}, err
	}
	def.Orchestrator = orchestrator
	def.SoulPrompt = readOptionalMarkdown(filepath.Join(teamDir, "SOUL.md"))
	def.AgentsPrompt = readOptionalMarkdown(filepath.Join(teamDir, "AGENTS.md"))
	return def, nil
}

func parseTeamOrchestrator(path string, raw map[string]any) (TeamOrchestratorConfig, error) {
	if len(raw) == 0 {
		return TeamOrchestratorConfig{}, fmt.Errorf("orchestrator is required")
	}
	modelConfig := mapNode(raw["modelConfig"])
	modelKey := stringNode(modelConfig["modelKey"])
	if modelKey == "" {
		return TeamOrchestratorConfig{}, fmt.Errorf("orchestrator.modelConfig.modelKey is required")
	}
	stageSettings := contracts.CloneMap(mapNode(raw["stageSettings"]))
	if err := validateAgentSamplingConfig(path, map[string]any{
		"modelConfig":   modelConfig,
		"stageSettings": stageSettings,
	}); err != nil {
		return TeamOrchestratorConfig{}, err
	}
	stageSettings = applyModelReasoningDefaults(stageSettings, mapNode(modelConfig["reasoning"]))
	stageSettings = applyModelSamplingDefaults(stageSettings, mapNode(modelConfig["sampling"]))

	budget := contracts.CloneMap(mapNode(raw["budget"]))
	budget = mergeStageSettingsBudgets(budget, stageSettings)
	maxParallel := TeamDefaultMaxParallel
	if value, exists := raw["maxParallel"]; exists {
		var valid bool
		maxParallel, valid = parseTeamMaxParallel(value)
		if !valid || maxParallel < 1 || maxParallel > TeamDefaultMaxParallel {
			return TeamOrchestratorConfig{}, fmt.Errorf("orchestrator.maxParallel must be between 1 and %d", TeamDefaultMaxParallel)
		}
	}
	return TeamOrchestratorConfig{
		ModelKey:      modelKey,
		ServiceTier:   stringNode(modelConfig["serviceTier"]),
		Budget:        budget,
		MaxParallel:   maxParallel,
		StageSettings: stageSettings,
	}, nil
}

func parseTeamMaxParallel(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		return int(typed), float64(int(typed)) == typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func teamRosterFingerprint(snapshot TeamSnapshot) string {
	type rosterMember struct {
		Key         string `json:"key"`
		Name        string `json:"name,omitempty"`
		Role        string `json:"role,omitempty"`
		Description string `json:"description,omitempty"`
		Available   bool   `json:"available"`
	}
	members := make([]rosterMember, 0, len(snapshot.AgentKeys))
	for _, key := range snapshot.AgentKeys {
		member := rosterMember{Key: key}
		if def, ok := snapshot.agentDefinitions[key]; ok {
			member.Available = true
			member.Name = strings.TrimSpace(def.Name)
			member.Role = strings.TrimSpace(def.Role)
			member.Description = strings.TrimSpace(def.Description)
		}
		members = append(members, member)
	}
	return teamCatalogFingerprint(struct {
		Members []rosterMember `json:"members"`
	}{Members: members})
}

func teamOrchestratorFingerprint(snapshot TeamSnapshot) string {
	return teamCatalogFingerprint(struct {
		TeamID            string                 `json:"teamId"`
		RuntimeMode       string                 `json:"runtimeMode"`
		AgentKeys         []string               `json:"agentKeys"`
		ToolSchemaVersion string                 `json:"toolSchemaVersion"`
		Orchestrator      TeamOrchestratorConfig `json:"orchestrator"`
		SoulPrompt        string                 `json:"soulPrompt"`
		AgentsPrompt      string                 `json:"agentsPrompt"`
	}{
		TeamID:            snapshot.TeamID,
		RuntimeMode:       snapshot.RuntimeMode,
		AgentKeys:         append([]string(nil), snapshot.AgentKeys...),
		ToolSchemaVersion: agentteam.HiddenToolSchemaVersion,
		Orchestrator:      cloneTeamOrchestratorConfig(snapshot.Orchestrator),
		SoulPrompt:        snapshot.SoulPrompt,
		AgentsPrompt:      snapshot.AgentsPrompt,
	})
}

func teamHiddenToolSchemaFingerprint(snapshot TeamSnapshot) string {
	return teamCatalogFingerprint(struct {
		SchemaVersion string   `json:"schemaVersion"`
		AgentKeys     []string `json:"agentKeys"`
	}{
		SchemaVersion: agentteam.HiddenToolSchemaVersion,
		AgentKeys:     append([]string(nil), snapshot.ValidAgentKeys...),
	})
}

func teamCatalogFingerprint(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
