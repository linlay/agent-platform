package runtimeresource

import (
	"fmt"
	"path/filepath"

	"agent-platform/internal/builtins"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/mcp"
	"agent-platform/internal/models"
	"agent-platform/internal/runtimeskills"
	"agent-platform/internal/tools"
)

func validateCandidate(root string) error {
	builtinRoot, err := builtins.ProcessConnectorsRoot()
	if err != nil {
		return fmt.Errorf("validate Platform connector resources: %w", err)
	}
	registriesDir := filepath.Join(root, "registries")
	if _, err := models.LoadModelRegistry(registriesDir); err != nil {
		return fmt.Errorf("validate Model/Provider Registry: %w", err)
	}
	if _, err := mcp.NewRegistry(filepath.Join(root, "connectors-center"), builtinRoot); err != nil {
		return fmt.Errorf("validate MCP Registry: %w", err)
	}
	toolDefinitions, err := tools.LoadRuntimeToolDefinitions(filepath.Join(root, "tools"))
	if err != nil {
		return fmt.Errorf("validate Tool resources: %w", err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{
			BuiltinConnectorsDir: builtinRoot,
			ConnectorsCenterDir:  filepath.Join(root, "connectors-center"),
			StateDir:             filepath.Join(root, ".state"),
			RegistriesDir:        registriesDir,
			ToolsDir:             filepath.Join(root, "tools"),
			AgentsDir:            filepath.Join(root, "agents"),
			RUAgentsDir:          filepath.Join(root, ".validation", "ru-agents"),
			TeamsDir:             filepath.Join(root, "teams"),
			RootDir:              filepath.Join(root, "root"),
			ChatsDir:             filepath.Join(root, ".validation", "chats"),
			MemoryDir:            filepath.Join(root, ".validation", "memory"),
			SkillsCenterDir:      filepath.Join(root, "skills-center"),
		},
		Skills: config.SkillCatalogConfig{MaxPromptChars: 1 << 20},
	}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		return fmt.Errorf("validate embedded Desktop connector: %w", err)
	}
	defer release()
	defer runtimeskills.Remove(filepath.Join(root, ".validation"))
	if _, err := catalog.NewFileRegistry(cfg, toolDefinitions); err != nil {
		return fmt.Errorf("validate Agent/Team/Skill resources: %w", err)
	}
	// Individual invalid Agents are isolated by the catalog and remain visible
	// through its admin diagnostics. They must not block an otherwise valid
	// runtime resource transaction or prevent Agent Platform from starting.
	return nil
}
