package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

func (p PathsConfig) connectorSibling(name string) string {
	if root := p.EffectiveConnectorsCenterDir(); root != "" {
		return filepath.Join(filepath.Dir(root), name)
	}
	return ""
}

func (p PathsConfig) EffectiveRUConnectorsDir() string {
	if value := strings.TrimSpace(p.RUConnectorsDir); value != "" {
		return value
	}
	return p.connectorSibling("ru-connectors")
}

func (p PathsConfig) EffectiveConnectorStateDir() string {
	if value := strings.TrimSpace(p.ConnectorStateDir); value != "" {
		return value
	}
	return p.connectorSibling("connector-state")
}

func (p PathsConfig) ConnectorSources() connector.Sources {
	return connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, RuntimeRoot: p.EffectiveRUConnectorsDir(), StateRoot: p.EffectiveConnectorStateDir()}
}

func validateConnectorPaths(p PathsConfig) error {
	if err := p.ConnectorSources().ValidateRoots(); err != nil {
		return err
	}
	// A generated connector tree must not overlap any other runtime data.
	for name, root := range map[string]string{"connectors-center-dir": p.EffectiveConnectorsCenterDir(), "ru-connectors-dir": p.EffectiveRUConnectorsDir(), "connector-state-dir": p.EffectiveConnectorStateDir()} {
		for _, other := range []string{p.AgentsDir, p.EffectiveRUAgentsDir(), p.SkillsCenterDir, p.TeamsDir, p.ChatsDir, p.MemoryDir, p.KBaseDir, p.RegistriesDir, p.ToolsDir, p.OwnerDir, p.RootDir, p.AutomationsDir, p.PanDir} {
			if other == "" {
				continue
			}
			if connector.RootsOverlap(root, other) {
				return fmt.Errorf("paths.%s must not overlap %s", name, other)
			}
		}
	}
	return nil
}
