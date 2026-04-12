package viewport

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

type ServerDefinition struct {
	Key          string
	BaseURL      string
	EndpointPath string
	AuthToken    string
	TimeoutMs    int
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
			Key:          strings.TrimSpace(contracts.FirstNonEmptyString(rootNode["key"], rootNode["serverKey"])),
			BaseURL:      strings.TrimSpace(contracts.StringValue(rootNode["baseUrl"])),
			EndpointPath: strings.TrimSpace(contracts.StringValue(rootNode["endpointPath"])),
			AuthToken:    strings.TrimSpace(contracts.StringValue(rootNode["authToken"])),
			TimeoutMs:    contracts.IntValue(rootNode["timeoutMs"]),
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
