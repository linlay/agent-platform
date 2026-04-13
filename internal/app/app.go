package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/hitl"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/mcp"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/reload"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/sandbox"
	"agent-platform-runner-go/internal/schedule"
	"agent-platform-runner-go/internal/server"
	"agent-platform-runner-go/internal/tools"
	"agent-platform-runner-go/internal/viewport"
)

type App struct {
	Config           config.Config
	Router           *server.Server
	backgroundCancel context.CancelFunc
	scheduler        *schedule.Orchestrator
}

func New() (*App, error) {
	appInitStartedAt := time.Now()

	configStartedAt := time.Now()
	log.Printf("loading config")
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.ContainerHub.Enabled {
		runtimeInfo := sandbox.NewContainerHubClient(cfg.ContainerHub).GetRuntimeInfo()
		if runtimeInfo.OK {
			cfg.ContainerHub.ResolvedEngine = runtimeInfo.Engine
			log.Printf("container-hub runtime info resolved (engine=%s)", strings.TrimSpace(runtimeInfo.Engine))
		} else {
			log.Printf("container-hub runtime info unavailable; falling back to container path prompts")
		}
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
	memoryStore, err := memory.NewSQLiteStore(cfg.Paths.MemoryDir, cfg.Memory.DBFileName)
	if err != nil {
		return nil, fmt.Errorf("init memory store (%s): %w", cfg.Paths.MemoryDir, err)
	}
	log.Printf("memory store ready in %s (root=%s)", startupElapsed(memoryStoreStartedAt), cfg.Paths.MemoryDir)

	modelRegistryStartedAt := time.Now()
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		return nil, fmt.Errorf("load model registry (%s): %w", cfg.Paths.RegistriesDir, err)
	}
	log.Printf("model registry ready in %s (root=%s)", startupElapsed(modelRegistryStartedAt), cfg.Paths.RegistriesDir)

	runManager := runctl.NewInMemoryRunManager()
	sandboxClient := sandbox.NewContainerHubSandboxService(cfg.ContainerHub, cfg.Paths)
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, memoryStore)
	if err != nil {
		return nil, fmt.Errorf("init runtime tools: %w", err)
	}
	mcpRegistry, err := mcp.NewRegistry(filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers"))
	if err != nil {
		return nil, fmt.Errorf("load mcp registry: %w", err)
	}
	mcpGate := mcp.NewAvailabilityGate()
	mcpClient := mcp.NewClientWithGate(mcpRegistry, nil, mcpGate)
	mcpToolSync := mcp.NewToolSync(mcpRegistry, mcpClient)
	if _, err := mcpToolSync.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("load mcp tools: %w", err)
	}
	runtimeTools, err := tools.LoadRuntimeToolDefinitions(cfg.Paths.ToolsDir)
	if err != nil {
		return nil, fmt.Errorf("load runtime tools: %w", err)
	}
	toolExecutor := tools.NewToolRouter(backendTools, mcpClient, mcpToolSync, llm.NewFrontendSubmitCoordinator(), contracts.NewNoopActionInvoker(), append([]api.ToolDetailResponse(nil), runtimeTools...)...)

	var hitlRegistry *hitl.Registry
	if cfg.BashHITL.Enabled {
		hitlRoot := filepath.Join(cfg.Paths.RegistriesDir, "bash-hitl")
		hitlRegistry, err = hitl.NewRegistry(hitlRoot)
		if err != nil {
			return nil, fmt.Errorf("init hitl registry (%s): %w", hitlRoot, err)
		}
		log.Printf("bash HITL registry ready (%d rules)", len(hitlRegistry.Rules()))
	}

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

	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, sandboxClient, hitlRegistry)
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, mcp.NewRegistryReloader(mcpRegistry, mcpToolSync), hitlRegistry)
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	cleanupBackground := true
	defer func() {
		if cleanupBackground {
			backgroundCancel()
		}
	}()
	reload.StartBackgroundReloaders(backgroundCtx, cfg, reloader)
	mcp.NewReconnectLoop(mcpRegistry, mcpToolSync, mcpGate, 10*time.Second).Start(backgroundCtx)
	log.Printf("background file watchers started (agents=%s teams=%s skills=%s)",
		cfg.Paths.AgentsDir,
		cfg.Paths.TeamsDir,
		cfg.Paths.SkillsMarketDir,
	)

	serverStartedAt := time.Now()
	srv, err := server.New(server.Dependencies{
		Config:   cfg,
		Chats:    chatStore,
		Memory:   memoryStore,
		Registry: registry,
		Models:   modelRegistry,
		Runs:     runManager,
		Agent:    agentEngine,
		Tools:    toolExecutor,
		Sandbox:  sandboxClient,
		MCP:      mcpClient,
		HITL:     hitlRegistry,
		Viewport: viewport.NewServiceWithServers(
			viewport.NewRegistry(viewport.DefaultRoot(cfg.Paths.RegistriesDir)),
			viewport.NewSyncer(viewport.NewServerRegistry(viewport.DefaultServersRoot(cfg.Paths.RegistriesDir)), nil),
			contracts.NewNoopViewportClient(),
		),
		CatalogReloader: reloader,
	})
	if err != nil {
		return nil, fmt.Errorf("init server: %w", err)
	}
	log.Printf("server dependencies wired in %s", startupElapsed(serverStartedAt))

	var scheduler *schedule.Orchestrator
	if cfg.Schedule.Enabled {
		scheduleRegistry := schedule.NewRegistry(cfg.Paths.SchedulesDir, registry)
		dispatcher := schedule.NewDispatcher(func(ctx context.Context, req api.QueryRequest) error {
			status, body, err := srv.ExecuteInternalQuery(ctx, req)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("scheduled query failed with status %d: %s", status, summarizeScheduleErrorBody(body))
			}
			return nil
		})
		scheduler = schedule.NewOrchestrator(scheduleRegistry, dispatcher, cfg.Schedule)
		if err := scheduler.Start(backgroundCtx); err != nil {
			backgroundCancel()
			return nil, fmt.Errorf("start schedule orchestrator: %w", err)
		}
		log.Printf("schedule orchestrator started in %s (dir=%s)", startupElapsed(serverStartedAt), cfg.Paths.SchedulesDir)
	} else {
		log.Printf("schedule orchestrator disabled")
	}
	log.Printf("app dependencies initialized in %s", startupElapsed(appInitStartedAt))
	cleanupBackground = false

	return &App{
		Config:           cfg,
		Router:           srv,
		backgroundCancel: backgroundCancel,
		scheduler:        scheduler,
	}, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	if a.backgroundCancel != nil {
		a.backgroundCancel()
	}
	if a.scheduler != nil {
		done := a.scheduler.Stop()
		<-done.Done()
	}
	return nil
}

func startupElapsed(startedAt time.Time) time.Duration {
	return time.Since(startedAt).Round(time.Millisecond)
}

func summarizeScheduleErrorBody(body string) string {
	body = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	if body == "" {
		return "<empty body>"
	}
	const maxLen = 240
	if len(body) > maxLen {
		return body[:maxLen] + "..."
	}
	return body
}
