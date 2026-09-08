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
	if root := p.EffectiveStateDir(); root != "" {
		return filepath.Join(root, "connectors")
	}
	return ""
}

func (p PathsConfig) ConnectorSources() connector.Sources {
	return connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, RuntimeRoot: p.EffectiveRUConnectorsDir(), StateRoot: p.EffectiveConnectorStateDir(), LegacyStateRoot: p.LegacyConnectorStateDir}
}

func validateConnectorPaths(p PathsConfig) error {
	if err := p.ConnectorSources().ValidateRoots(); err != nil {
		return err
	}
	if err := (connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, RuntimeRoot: p.EffectiveRUConnectorsDir(), StateRoot: p.EffectiveStateDir()}).ValidateRoots(); err != nil {
		return fmt.Errorf("AP_RUNTIME_STATE_DIR: %w", err)
	}
	// A generated connector tree must not overlap any other runtime data.
	for name, root := range map[string]string{"connectors-center-dir": p.EffectiveConnectorsCenterDir(), "state-dir": p.EffectiveStateDir(), "ru-connectors-dir": p.EffectiveRUConnectorsDir()} {
		for _, other := range []string{p.AgentsDir, p.EffectiveRUAgentsDir(), p.SkillsCenterDir, p.TeamsDir, p.ChatsDir, p.MemoryDir, p.KBaseDir, p.RegistriesDir, p.ToolsDir, p.OwnerDir, p.RootDir, p.AutomationsDir, p.PanDir} {
			if other == "" {
				continue
			}
			if connector.RootsOverlap(root, other) {
				return fmt.Errorf("runtime directory %s must not overlap %s", name, other)
			}
		}
	}
	return nil
}
