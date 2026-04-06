package viewport

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

type ServerDefinition struct {
	Key       string
	BaseURL   string
	AuthToken string
	TimeoutMs int
}

type ServerRegistry struct {
	root string
}

func NewServerRegistry(root string) *ServerRegistry {
	return &ServerRegistry{root: root}
}

func (r *ServerRegistry) List() ([]ServerDefinition, error) {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]ServerDefinition, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !catalog.ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		tree, err := config.LoadYAMLTree(filepath.Join(r.root, name))
		if err != nil {
			continue
		}
		rootNode, _ := tree.(map[string]any)
		server := ServerDefinition{
			Key:       strings.TrimSpace(anyString(rootNode["key"])),
			BaseURL:   strings.TrimSpace(anyString(rootNode["baseUrl"])),
			AuthToken: strings.TrimSpace(anyString(rootNode["authToken"])),
			TimeoutMs: anyInt(rootNode["timeoutMs"]),
		}
		if server.Key == "" {
			server.Key = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		}
		if server.BaseURL != "" {
			out = append(out, server)
		}
	}
	return out, nil
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func anyInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
