package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/engine"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/server"
)

type App struct {
	Config config.Config
	Router *server.Server
}

func New() (*App, error) {
	appInitStartedAt := time.Now()

	configStartedAt := time.Now()
	log.Printf("loading config")
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	log.Printf(
		"loaded config in %s (registries=%s agents=%s teams=%s skills=%s chats=%s memory=%s)",
		startupElapsed(configStartedAt),
		cfg.Paths.RegistriesDir,
		cfg.Paths.AgentsDir,
		cfg.Paths.TeamsDir,
		cfg.Paths.SkillsMarketDir,
		cfg.Paths.ChatsDir,
		cfg.Paths.MemoryDir,
	)
	log.Printf("initializing stores/registries")

	chatStoreStartedAt := time.Now()
	chatStore, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		return nil, fmt.Errorf("init chat store (%s): %w", cfg.Paths.ChatsDir, err)
	}
	log.Printf("chat store ready in %s (root=%s)", startupElapsed(chatStoreStartedAt), cfg.Paths.ChatsDir)

	memoryStoreStartedAt := time.Now()
	memoryStore, err := memory.NewFileStore(cfg.Paths.MemoryDir)
	if err != nil {
		return nil, fmt.Errorf("init memory store (%s): %w", cfg.Paths.MemoryDir, err)
	}
	log.Printf("memory store ready in %s (root=%s)", startupElapsed(memoryStoreStartedAt), cfg.Paths.MemoryDir)

	modelRegistryStartedAt := time.Now()
	modelRegistry, err := engine.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		return nil, fmt.Errorf("load model registry (%s): %w", cfg.Paths.RegistriesDir, err)
	}
	log.Printf("model registry ready in %s (root=%s)", startupElapsed(modelRegistryStartedAt), cfg.Paths.RegistriesDir)

	runManager := engine.NewInMemoryRunManager()
	sandbox := engine.NewContainerHubSandboxService(cfg.ContainerHub, cfg.Paths)
	toolExecutor := engine.NewRuntimeToolExecutor(cfg, sandbox)

	registryStartedAt := time.Now()
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		return nil, fmt.Errorf(
			"load catalog registry (agents=%s teams=%s skills=%s): %w",
			cfg.Paths.AgentsDir,
			cfg.Paths.TeamsDir,
			cfg.Paths.SkillsMarketDir,
			err,
		)
	}
	log.Printf(
		"catalog registry ready in %s (agents=%d teams=%d skills=%d tools=%d)",
		startupElapsed(registryStartedAt),
		len(registry.Agents("")),
		len(registry.Teams()),
		len(registry.Skills("")),
		len(toolExecutor.Definitions()),
	)

	agentEngine := engine.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, sandbox)
	reloader := engine.NewRuntimeCatalogReloader(registry, modelRegistry)
	engine.StartBackgroundReloaders(context.Background(), cfg, reloader)
	log.Printf("background reloaders started (agents=%dms teams=%dms skills=%dms models=%dms providers=%dms)",
		cfg.Agents.RefreshIntervalMs,
		cfg.Teams.RefreshIntervalMs,
		cfg.Skills.RefreshIntervalMs,
		cfg.Models.RefreshIntervalMs,
		cfg.Providers.RefreshIntervalMs,
	)

	serverStartedAt := time.Now()
	srv := server.New(server.Dependencies{
		Config:          cfg,
		Chats:           chatStore,
		Memory:          memoryStore,
		Registry:        registry,
		Runs:            runManager,
		Agent:           agentEngine,
		Tools:           toolExecutor,
		Sandbox:         sandbox,
		MCP:             engine.NewNoopMcpClient(),
		Viewport:        engine.NewNoopViewportClient(),
		CatalogReloader: reloader,
	})
	log.Printf("server dependencies wired in %s", startupElapsed(serverStartedAt))
	log.Printf("app dependencies initialized in %s", startupElapsed(appInitStartedAt))

	return &App{
		Config: cfg,
		Router: srv,
	}, nil
}

func startupElapsed(startedAt time.Time) time.Duration {
	return time.Since(startedAt).Round(time.Millisecond)
}
