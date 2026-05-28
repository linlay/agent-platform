package catalog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

const AgentOrderFileName = "agent-order.json"

type AgentOrderFile struct {
	Version   int      `json:"version"`
	Order     []string `json:"order"`
	UpdatedAt int64    `json:"updatedAt"`
}

func EmptyAgentOrderFile() AgentOrderFile {
	return AgentOrderFile{Version: 1, Order: []string{}, UpdatedAt: 0}
}

func AgentOrderPath(agentsDir string) string {
	if agentsDir == "" {
		return AgentOrderFileName
	}
	return filepath.Join(agentsDir, AgentOrderFileName)
}

func ReadAgentOrderFile(agentsDir string) (AgentOrderFile, error) {
	path := AgentOrderPath(agentsDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return EmptyAgentOrderFile(), nil
	}
	if err != nil {
		return AgentOrderFile{}, err
	}
	var file AgentOrderFile
	if err := json.Unmarshal(data, &file); err != nil {
		return AgentOrderFile{}, err
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Order == nil {
		file.Order = []string{}
	}
	return file, nil
}

func orderKeys[T any](values map[string]T, order []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(values))
	for _, key := range order {
		if _, ok := values[key]; !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	remaining := make([]string, 0, len(values)-len(keys))
	for key := range values {
		if _, exists := seen[key]; exists {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	keys = append(keys, remaining...)
	return keys
}

func (r *FileRegistry) orderedAgentKeysLocked() []string {
	orderFile, err := ReadAgentOrderFile(r.cfg.Paths.AgentsDir)
	if err != nil {
		return sortedKeys(r.agents)
	}
	return orderKeys(r.agents, orderFile.Order)
}
