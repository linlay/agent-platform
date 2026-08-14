package runtimeresource

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/mcp"
	"agent-platform/internal/models"
	"agent-platform/internal/tools"
	"agent-platform/internal/viewport"
)

func validateCandidate(root string) error {
	registriesDir := filepath.Join(root, "registries")
	if _, err := models.LoadModelRegistry(registriesDir); err != nil {
		return fmt.Errorf("validate Model/Provider Registry: %w", err)
	}
	if _, err := mcp.NewRegistry(filepath.Join(registriesDir, "mcp-servers")); err != nil {
		return fmt.Errorf("validate MCP Registry: %w", err)
	}
	toolDefinitions, err := tools.LoadRuntimeToolDefinitions(filepath.Join(root, "tools"))
	if err != nil {
		return fmt.Errorf("validate Tool resources: %w", err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{
			RegistriesDir:   registriesDir,
			ToolsDir:        filepath.Join(root, "tools"),
			AgentsDir:       filepath.Join(root, "agents"),
			RUAgentsDir:     filepath.Join(root, ".validation", "ru-agents"),
			TeamsDir:        filepath.Join(root, "teams"),
			RootDir:         root,
			ChatsDir:        filepath.Join(root, ".validation", "chats"),
			MemoryDir:       filepath.Join(root, ".validation", "memory"),
			SkillsCenterDir: filepath.Join(root, "skills-center"),
		},
		Skills: config.SkillCatalogConfig{MaxPromptChars: 1 << 20},
	}
	catalogRegistry, err := catalog.NewFileRegistry(cfg, toolDefinitions)
	if err != nil {
		return fmt.Errorf("validate Agent/Team/Skill resources: %w", err)
	}
	for _, agent := range catalogRegistry.AdminAgents() {
		if agent.Status != catalog.AdminAgentStatusInvalid {
			continue
		}
		message := "invalid Agent resource"
		if len(agent.Diagnostics) > 0 && strings.TrimSpace(agent.Diagnostics[0].Message) != "" {
			message = agent.Diagnostics[0].Message
		}
		return fmt.Errorf("validate Agent %s: %s", agent.Key, message)
	}
	if err := validateViewports(viewport.DefaultRoot(registriesDir)); err != nil {
		return err
	}
	return nil
}

func validateViewports(root string) error {
	registry := viewport.NewRegistry(root)
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".qlc") {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("validate Viewport %s: %w", filePath, err)
		}
		key := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if strings.EqualFold(entry.Name(), "index.qlc") {
			key = filepath.Base(filepath.Dir(filePath))
		}
		if _, found, err := registry.Get(key); err != nil {
			return fmt.Errorf("validate Viewport %s: %w", filePath, err)
		} else if !found {
			return fmt.Errorf("validate Viewport %s: actual loader did not resolve %q", filePath, key)
		}
		return nil
	})
}
