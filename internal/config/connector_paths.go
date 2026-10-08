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
	return connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, NativeKanbanControlDir: p.NativeKanbanControlDir, NativeTaskControlDir: p.NativeTaskControlDir, NativePlatformControlDir: p.NativePlatformControlDir, NativeWebControlDir: p.NativeWebControlDir, StateRoot: p.EffectiveConnectorStateDir()}
}

func validateConnectorPaths(p PathsConfig) error {
	if err := p.ConnectorSources().ValidateRoots(); err != nil {
		return err
	}
	if err := (connector.Sources{ExternalRoot: p.EffectiveConnectorsCenterDir(), BuiltinRoot: p.BuiltinConnectorsDir, NativeKanbanControlDir: p.NativeKanbanControlDir, NativeTaskControlDir: p.NativeTaskControlDir, NativePlatformControlDir: p.NativePlatformControlDir, NativeWebControlDir: p.NativeWebControlDir, StateRoot: p.EffectiveStateDir()}).ValidateRoots(); err != nil {
		return fmt.Errorf("AP_RUNTIME_STATE_DIR: %w", err)
	}
	// Connector sources and persistent state must stay outside generated Agents
	// and unrelated runtime data.
	for name, root := range map[string]string{"connectors-center-dir": p.EffectiveConnectorsCenterDir(), "state-dir": p.EffectiveStateDir(), "ru-connectors": p.ConnectorSources().SharedRoot()} {
		for _, other := range []string{p.AgentsDir, p.EffectiveRUAgentsDir(), p.SkillsCenterDir, p.TeamsDir, p.ChatsDir, p.MemoryDir, p.KBaseDir, p.KBasesDir, p.RUKBasesDir, p.RegistriesDir, p.ToolsDir, p.OwnerDir, p.RootDir, p.AutomationsDir, p.PanDir} {
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

// PrepareNativeConnectors publishes the binary's embedded resources and retains
// their shared version for the caller's lifetime, even with no Agent mounts.
func (p *PathsConfig) PrepareNativeConnectors() (func(), error) {
	pkg, release, err := p.ConnectorSources().InstallEmbeddedPlatformControl()
	if err != nil {
		return nil, err
	}
	web, releaseWeb, err := p.ConnectorSources().InstallEmbeddedWebControl()
	if err != nil {
		release()
		return nil, err
	}
	task, releaseTask, err := p.ConnectorSources().InstallEmbeddedTaskControl()
	if err != nil {
		releaseWeb()
		release()
		return nil, err
	}
	kanban, releaseKanban, err := p.ConnectorSources().InstallEmbeddedKanbanControl()
	if err != nil {
		releaseTask()
		releaseWeb()
		release()
		return nil, err
	}
	p.NativeKanbanControlDir = kanban.Dir
	p.NativeTaskControlDir = task.Dir
	p.NativePlatformControlDir = pkg.Dir
	p.NativeWebControlDir = web.Dir
	return func() { releaseKanban(); releaseTask(); releaseWeb(); release() }, nil
}
