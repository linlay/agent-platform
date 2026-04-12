package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/config"
)

func loadTeams(root string) (map[string]TeamDefinition, error) {
	items := map[string]TeamDefinition{}
	err := visitRuntimeEntries(
		root,
		nil,
		func(name string, entry os.DirEntry) bool {
			return !entry.IsDir() && ShouldLoadRuntimeName(name) && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"))
		},
		func(name string, _ os.DirEntry) {
			path := filepath.Join(root, name)
			def, err := parseTeamFile(path)
			if err != nil {
				log.Printf("[catalog][teams] skip file %s: parse error: %v", name, err)
				return
			}
			items[def.TeamID] = def
		},
	)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func parseTeamFile(path string) (TeamDefinition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return TeamDefinition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return TeamDefinition{}, fmt.Errorf("team file must be a map")
	}
	base := filepath.Base(path)
	teamID := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	return TeamDefinition{
		TeamID:          teamID,
		Name:            defaultString(stringNode(root["name"]), teamID),
		Icon:            root["icon"],
		AgentKeys:       listStrings(root["agentKeys"]),
		DefaultAgentKey: stringNode(root["defaultAgentKey"]),
	}, nil
}
