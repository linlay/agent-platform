package config

import (
	"fmt"
	"path/filepath"

	"agent-platform/internal/connector"
)

func (p PathsConfig) EffectiveConnectorStateDir() string {
	if root := p.EffectiveStateDir(); root != "" {
		return filepath.Join(root, "connectors")
	}
	return ""
}

func (p PathsConfig) ConnectorSources() connector.Sources {
	return connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, StateRoot: p.EffectiveConnectorStateDir(), LegacyStateRoot: p.LegacyConnectorStateDir}
}

func validateConnectorPaths(p PathsConfig) error {
	if err := p.ConnectorSources().ValidateRoots(); err != nil {
		return err
	}
	if err := (connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, StateRoot: p.EffectiveStateDir()}).ValidateRoots(); err != nil {
		return fmt.Errorf("AP_RUNTIME_STATE_DIR: %w", err)
	}
	// Connector sources and persistent state must stay outside generated Agents
	// and unrelated runtime data.
	for name, root := range map[string]string{"connectors-center-dir": p.EffectiveConnectorsCenterDir(), "state-dir": p.EffectiveStateDir()} {
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
