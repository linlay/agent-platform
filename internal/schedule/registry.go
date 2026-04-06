package schedule

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

type Definition struct {
	ID      string
	Name    string
	Spec    string
	Request api.QueryRequest
}

type Registry struct {
	root string
}

func NewRegistry(root string) *Registry {
	return &Registry{root: root}
}

func (r *Registry) Load() ([]Definition, error) {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defs := make([]Definition, 0)
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		def, err := parseDefinition(filepath.Join(r.root, entry.Name()))
		if err != nil {
			continue
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func parseDefinition(path string) (Definition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return Definition{}, err
	}
	root, _ := tree.(map[string]any)
	base := filepath.Base(path)
	id := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	request := api.QueryRequest{
		AgentKey: stringNode(root["agentKey"]),
		TeamID:   stringNode(root["teamId"]),
		Message:  stringNode(root["message"]),
	}
	return Definition{
		ID:      id,
		Name:    defaultString(stringNode(root["name"]), id),
		Spec:    defaultString(stringNode(root["cron"]), "@hourly"),
		Request: request,
	}, nil
}

func stringNode(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
