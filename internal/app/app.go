package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/artifactpusher"
	"agent-platform/internal/automation"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/gateway"
	"agent-platform/internal/llm"
	"agent-platform/internal/lsp"
	"agent-platform/internal/mcp"
	"agent-platform/internal/memory"
	"agent-platform/internal/models"
	"agent-platform/internal/observability"
	"agent-platform/internal/reload"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/sandbox"
	"agent-platform/internal/server"
	"agent-platform/internal/skills"
	"agent-platform/internal/supportpkg"
	"agent-platform/internal/tools"
	"agent-platform/internal/viewport"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type App struct {
	Config               config.Config
	RuntimeEnv           runtimeenv.Info
	Router               *server.Server
	backgroundCancel     context.CancelFunc
	automation           automationStopper
	gateways             *gateway.Registry
	wsHub                *ws.Hub
	automationExecutions *automation.ExecutionStore
	lspManager           *lsp.Manager
	mcpClient            *mcp.Client
	kbaseManager         *kbase.Manager
}

type automationStopper interface {
	Stop() context.Context
}

var automationStopTimeout = 3 * time.Second

func New(rootCtx context.Context, configOptions ...config.LoadOptions) (*App, error) {
	appInitStartedAt := time.Now()
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	hostEnv := runtimeenv.Detect()

	configStartedAt := time.Now()
	log.Printf("loading config")
	cfg, err := config.Load(configOptions...)
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
	supportPackages, supportRoot, supportErrors := supportpkg.DiscoverNearExecutable()
	for _, supportErr := range supportErrors {
		log.Printf("support package discovery warning: %v", supportErr)
	}
	if supportPackages != nil && supportPackages.ExecutableCount() > 0 {
		log.Printf("support packages ready (root=%s packages=%d executables=%d)", supportRoot, len(supportPackages.Packages()), supportPackages.ExecutableCount())
		for _, executable := range supportPackages.Executables() {
			log.Printf("support package executable found (name=%s package=%s version=%s path=%s)", executable.Name, executable.PluginID, executable.Version, executable.Path)
		}
	} else {
		log.Printf("support packages not found (root=%s)", supportRoot)
	}
	log.Printf("initializing stores/registries")

	chatStoreStartedAt := time.Now()
	chatStore, err := chat.NewFileStoreAtStartup(cfg.Paths.ChatsDir)
	if err != nil {
		return nil, fmt.Errorf("init chat store (%s): %w", cfg.Paths.ChatsDir, err)
	}
	log.Printf("chat store ready in %s (root=%s)", startupElapsed(chatStoreStartedAt), cfg.Paths.ChatsDir)
	archiveStoreStartedAt := time.Now()
	archiveStore, err := chat.NewArchiveStoreAtStartup(cfg.Paths.ChatsDir)
	if err != nil {
		return nil, fmt.Errorf("init archive store (%s): %w", cfg.Paths.ChatsDir, err)
	}
	archiver := chat.NewArchiver(chatStore, archiveStore)
	log.Printf("archive store ready in %s (root=%s)", startupElapsed(archiveStoreStartedAt), filepath.Join(cfg.Paths.ChatsDir, "archive"))

	var memoryStore memory.Store
	var sqliteMemoryStore *memory.SQLiteStore
	var skillCandidateStore skills.CandidateStore
	if cfg.Memory.Enabled {
		memoryStoreStartedAt := time.Now()
		sqliteMemoryStore, err = memory.NewSQLiteStoreAtStartup(cfg.Paths.MemoryDir, cfg.Memory.DBFileName)
		if err != nil {
			return nil, fmt.Errorf("init memory store (%s): %w", cfg.Paths.MemoryDir, err)
		}
		memoryStore = sqliteMemoryStore
		log.Printf("memory store ready in %s (root=%s)", startupElapsed(memoryStoreStartedAt), cfg.Paths.MemoryDir)
		skillCandidateStore, err = skills.NewFileCandidateStore(filepath.Join(cfg.Paths.MemoryDir, "skill-candidates"))
		if err != nil {
			return nil, fmt.Errorf("init skill candidate store (%s): %w", filepath.Join(cfg.Paths.MemoryDir, "skill-candidates"), err)
		}
		if err := observability.InitMemoryLogger(cfg.Logging.Memory.Enabled, cfg.Logging.Memory.File); err != nil {
			return nil, fmt.Errorf("init memory logger (%s): %w", cfg.Logging.Memory.File, err)
		}
	} else {
		log.Printf("memory system disabled by config")
	}

	modelRegistryStartedAt := time.Now()
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		return nil, fmt.Errorf("load model registry (%s): %w", cfg.Paths.RegistriesDir, err)
	}
	log.Printf("model registry ready in %s (root=%s)", startupElapsed(modelRegistryStartedAt), cfg.Paths.RegistriesDir)

	runManager := contracts.NewInMemoryRunManager()
	sandboxClient := sandbox.NewContainerHubSandboxService(cfg.ContainerHub, cfg.Paths)
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, chatStore, memoryStore, skillCandidateStore)
	if err != nil {
		return nil, fmt.Errorf("init runtime tools: %w", err)
	}
	backendTools.WithRuntimeEnv(hostEnv)
	backendTools.WithModelRegistry(modelRegistry)
	var lspManager *lsp.Manager
	if cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics.Enabled {
		lspManager = lsp.NewManager(cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics)
		backendTools.WithFileChangeHooks(lspManager)
	}
	// artifactPusher 在下面 notifications 就绪后再接入 backendTools，
	// 这样它发出的 push frame 能走到 WS hub，转给网关做 artifact 预告。
	mcpRegistry, err := mcp.NewRegistry(filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers"))
	if err != nil {
		return nil, fmt.Errorf("load mcp registry: %w", err)
	}
	mcpGate := mcp.NewAvailabilityGate()
	mcpClient := mcp.NewClientWithGate(mcpRegistry, nil, mcpGate)
	cleanupMCP := true
	defer func() {
		if cleanupMCP {
			_ = mcpClient.Close()
		}
	}()
	mcpToolSync := mcp.NewToolSync(mcpRegistry, mcpClient)
	if _, err := mcpToolSync.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("load mcp tools: %w", err)
	}
	runtimeTools, err := tools.LoadRuntimeToolDefinitions(cfg.Paths.ToolsDir)
	if err != nil {
		return nil, fmt.Errorf("load runtime tools: %w", err)
	}
	frontendRegistry := frontendtools.NewDefaultRegistry()
	toolExecutor := tools.NewToolRouter(backendTools, mcpClient, mcpToolSync, llm.NewFrontendSubmitCoordinator(frontendRegistry), contracts.NewNoopActionInvoker(), append([]api.ToolDetailResponse(nil), runtimeTools...)...)

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
	if cfg.Memory.Enabled && sqliteMemoryStore != nil {
		sqliteMemoryStore.SetRuntimeResolver(memoryRuntimeResolver(cfg, registry, modelRegistry))
	}
	kbaseManager := kbase.NewManager(kbaseManagerOptions(cfg), kbaseCatalogSource{registry: registry}, modelRegistry).WithSupportPackages(supportPackages)
	if err := kbaseManager.ValidateConfiguration(); err != nil {
		return nil, fmt.Errorf("validate KBASE storage ownership: %w", err)
	}
	startupKBaseFailures := kbaseManager.ValidateAndAdoptStartupStorageContracts()
	startupKBaseKeys := make([]string, 0, len(startupKBaseFailures))
	for key := range startupKBaseFailures {
		startupKBaseKeys = append(startupKBaseKeys, key)
	}
	sort.Strings(startupKBaseKeys)
	for _, key := range startupKBaseKeys {
		cause := startupKBaseFailures[key]
		registry.InvalidateRuntimeAgent(key, "invalid_kbase_storage", cause)
		log.Printf("[catalog][agents] isolate agent=%s: %v", key, cause)
	}
	log.Printf(
		"catalog registry ready in %s (agents=%d teams=%d skills=%d tools=%d)",
		startupElapsed(registryStartedAt),
		len(registry.Agents("")),
		len(registry.Teams()),
		len(registry.Skills("")),
		len(toolExecutor.Definitions()),
	)
	if err := toolExecutor.RegisterHandler(kbase.NewToolHandler(kbaseManager)); err != nil {
		return nil, fmt.Errorf("register KBASE tools: %w", err)
	}

	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, frontendRegistry, sandboxClient)
	wsHub := ws.NewHub()
	var notifications contracts.NotificationSink = wsHub
	// gatewayResolver 在 Registry 构建完成后（server 依赖就绪之后）绑定。
	// pusher 先拿到 resolver 指针，Registry 构建完调用 SetRegistry 就能工作。
	gatewayResolver := &lazyGatewayResolver{chats: chatStore}
	backendTools.WithArtifactPusher(artifactpusher.New(artifactpusher.Config{
		Resolver:      gatewayResolver,
		UploadPath:    config.GatewayUploadPath,
		ChatsDir:      cfg.Paths.ChatsDir,
		Notifications: notifications,
	}))
	backgroundCtx, backgroundCancel := context.WithCancel(rootCtx)
	cleanupBackground := true
	defer func() {
		if cleanupBackground {
			backgroundCancel()
		}
	}()
	cardReporter := gateway.NewAgentCardReporter(backgroundCtx, registry, toolExecutor)
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, mcp.NewRegistryReloader(mcpRegistry, mcpToolSync), toolExecutor, cfg.Paths.ToolsDir, notifications, kbaseManager)
	reloader.AddObserver(cardReporter)
	kbaseManager.Start(backgroundCtx)
	reload.StartBackgroundReloaders(backgroundCtx, cfg, reloader)
	mcp.NewReconnectLoop(mcpRegistry, mcpToolSync, mcpGate, 10*time.Second).Start(backgroundCtx)
	log.Printf("background file watchers started (agents=%s teams=%s skills=%s)",
		cfg.Paths.AgentsDir,
		cfg.Paths.TeamsDir,
		cfg.Paths.SkillsMarketDir,
	)

	var channelReg *channel.Registry
	if len(cfg.Channels) > 0 {
		channelReg = channel.NewRegistry(cfg.Channels)
		log.Printf("channel registry ready (%d channels)", len(cfg.Channels))
	}

	var srv *server.Server
	var automationOrchestrator *automation.Orchestrator
	var automationRegistry *automation.Registry
	var automationExecutionStore *automation.ExecutionStore
	if cfg.Automation.Enabled {
		automationRegistry = automation.NewRegistry(cfg.Automation.ExternalDir, registry)
		automationExecutionStore, err = automation.NewExecutionStore(cfg.Automation.ExternalDir, "executions.db")
		if err != nil {
			return nil, fmt.Errorf("init automation execution store (%s): %w", cfg.Automation.ExternalDir, err)
		}
		var automationBroadcaster automation.Broadcaster
		if hub, ok := notifications.(*ws.Hub); ok {
			automationBroadcaster = hub
		}
		dispatcher := automation.NewDispatcher(func(ctx context.Context, req api.QueryRequest) error {
			if srv == nil {
				return fmt.Errorf("server not initialized")
			}
			if strings.TrimSpace(req.Role) == "" {
				req.Role = api.QueryRoleAutomation
			}
			status, body, err := srv.ExecuteInternalQuery(ctx, req)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("automation query failed with status %d: %s", status, summarizeAutomationErrorBody(body))
			}
			return nil
		}, automationBroadcaster, automationExecutionStore)
		automationOrchestrator = automation.NewOrchestrator(automationRegistry, dispatcher, cfg.Automation)
	}

	serverStartedAt := time.Now()
	srv, err = server.New(server.Dependencies{
		Config:        cfg,
		Chats:         chatStore,
		Archives:      archiveStore,
		Archiver:      archiver,
		Memory:        memoryStore,
		KBase:         kbaseManager,
		Registry:      registry,
		Models:        modelRegistry,
		Runs:          runManager,
		Agent:         agentEngine,
		Tools:         toolExecutor,
		Sandbox:       sandboxClient,
		MCP:           mcpClient,
		FrontendTools: frontendRegistry,
		Viewport: viewport.NewServiceWithServers(
			viewport.NewRegistry(viewport.DefaultRoot(cfg.Paths.RegistriesDir)),
			viewport.NewSyncer(viewport.NewServerRegistry(viewport.DefaultServersRoot(cfg.Paths.RegistriesDir)), nil),
			contracts.NewNoopViewportClient(),
		),
		CatalogReloader:        reloader,
		Notifications:          notifications,
		SkillCandidates:        skillCandidateStore,
		Channels:               channelReg,
		AutomationOrchestrator: automationOrchestrator,
		DeltaMappers:           llm.DeltaMapperFactory{Frontend: frontendRegistry},
		SystemInits: llm.NewSystemInitProfileBuilder(modelRegistry, llm.SystemInitDefaults{
			PlanMaxSteps:             cfg.Defaults.Plan.MaxSteps,
			PlanMaxWorkRoundsPerTask: cfg.Defaults.Plan.MaxWorkRoundsPerTask,
			CoderPlanningMaxSteps:    cfg.Defaults.CoderPlanning.MaxSteps,
			Prompts:                  cfg.Prompts,
		}),
		AutomationRegistry:   automationRegistry,
		AutomationExecutions: automationExecutionStore,
		GatewayResolver:      gatewayResolver,
		AgentCardStatus:      cardReporter,
		AgentCardRefresh:     cardReporter,
	})
	if err != nil {
		if automationExecutionStore != nil {
			_ = automationExecutionStore.Close()
		}
		return nil, fmt.Errorf("init server: %w", err)
	}
	log.Printf("server dependencies wired in %s", startupElapsed(serverStartedAt))

	// Gateway Registry 支持多条反向 WS 连接；configs/channels.yml 只在启动时读取。
	var gwRegistry *gateway.Registry
	if handler := srv.WSHandler(); handler != nil {
		gwRegistry = gateway.New(
			backgroundCtx,
			cfg.WebSocket,
			time.Duration(cfg.SSE.HeartbeatInterval)*time.Second,
			wsHub,
			handler.Dispatch,
			cardReporter,
		)
		for _, entry := range cfg.Gateways {
			if err := gwRegistry.Register(entry); err != nil {
				log.Printf("gateway register %q failed: %v", entry.ID, err)
			} else {
				log.Printf("gateway registered: id=%s channel=%s url=%s", entry.ID, entry.Channel, entry.URL)
			}
		}
		gatewayResolver.SetRegistry(gwRegistry)
		srv.SetChannelStatusProvider(gwRegistry)
	}

	if automationOrchestrator != nil {
		if err := automationOrchestrator.Start(backgroundCtx); err != nil {
			backgroundCancel()
			if automationExecutionStore != nil {
				_ = automationExecutionStore.Close()
			}
			return nil, fmt.Errorf("start automation orchestrator: %w", err)
		}
		log.Printf("automation orchestrator started in %s (dir=%s)", startupElapsed(serverStartedAt), cfg.Automation.ExternalDir)
	} else {
		log.Printf("automation orchestrator disabled")
	}
	log.Printf("app dependencies initialized in %s", startupElapsed(appInitStartedAt))
	cleanupBackground = false
	cleanupMCP = false

	return &App{
		Config:               cfg,
		RuntimeEnv:           hostEnv,
		Router:               srv,
		backgroundCancel:     backgroundCancel,
		automation:           automationOrchestrator,
		gateways:             gwRegistry,
		wsHub:                wsHub,
		automationExecutions: automationExecutionStore,
		lspManager:           lspManager,
		mcpClient:            mcpClient,
		kbaseManager:         kbaseManager,
	}, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	// Cancel startup/reconcile/watcher work before waiting for KBASE refreshes
	// and stopping its sidecar. This prevents an in-flight refresh from
	// restarting the process after shutdown has begun.
	if a.backgroundCancel != nil {
		a.backgroundCancel()
	}
	if a.kbaseManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		if err := a.kbaseManager.Close(ctx); err != nil {
			log.Printf("close KBASE manager: %v", err)
		}
		cancel()
	}
	if a.gateways != nil {
		a.gateways.StopAll()
	}
	if a.wsHub != nil {
		a.wsHub.CloseAll(gws.CloseNormalClosure, "server shutting down")
	}
	if err := observability.CloseMemoryLogger(); err != nil {
		log.Printf("close memory logger: %v", err)
	}
	if a.automation != nil {
		done := a.automation.Stop()
		select {
		case <-done.Done():
		case <-time.After(automationStopTimeout):
			log.Printf("automation stop timed out after %s", automationStopTimeout)
		}
	}
	if a.automationExecutions != nil {
		if err := a.automationExecutions.Close(); err != nil {
			log.Printf("close automation execution store: %v", err)
		}
	}
	if a.lspManager != nil {
		if err := a.lspManager.Close(); err != nil {
			log.Printf("close lsp manager: %v", err)
		}
	}
	if a.mcpClient != nil {
		if err := a.mcpClient.Close(); err != nil {
			log.Printf("close MCP client: %v", err)
		}
	}
	if err := observability.CloseMemoryLogger(); err != nil {
		log.Printf("close memory logger: %v", err)
	}
	return nil
}

func startupElapsed(startedAt time.Time) time.Duration {
	return time.Since(startedAt).Round(time.Millisecond)
}

func memoryRuntimeResolver(cfg config.Config, registry catalog.Registry, modelRegistry *models.ModelRegistry) memory.RuntimeResolver {
	var logOnce sync.Map
	return func(agentKey string) memory.RuntimeConfig {
		agent, ok := registry.AgentDefinition(strings.TrimSpace(agentKey))
		if !ok || !agent.MemoryConfig.Enabled {
			return memory.RuntimeConfig{}
		}
		return memory.RuntimeConfig{
			Embedder:   resolveMemoryEmbedder(cfg, agent, modelRegistry, &logOnce),
			Summarizer: resolveMemorySummarizer(cfg, agent, modelRegistry),
		}
	}
}

func resolveMemorySummarizer(cfg config.Config, agent catalog.AgentDefinition, modelRegistry *models.ModelRegistry) memory.RememberSummarizer {
	modelKey := strings.TrimSpace(agent.MemoryConfig.AutoRemember.ModelKey)
	if modelKey == "" {
		return nil
	}
	timeout := agent.MemoryConfig.AutoRemember.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	return memory.NewLLMMemorySummarizer(modelRegistry, modelKey, timeout, cfg.MemoryPrompts)
}

func resolveMemoryEmbedder(_ config.Config, agent catalog.AgentDefinition, modelRegistry *models.ModelRegistry, logOnce *sync.Map) *memory.EmbeddingProvider {
	embeddingCfg := agent.MemoryConfig.Embedding
	providerKey := strings.TrimSpace(embeddingCfg.ProviderKey)
	if providerKey == "" {
		return nil
	}
	provider, err := modelRegistry.GetProvider(providerKey)
	if err != nil {
		logMemoryEmbeddingOnce(logOnce, agent.Key, providerKey, "missing-provider", fmt.Sprintf("[memory][embedding] provider %q not found for agent %s, hybrid search disabled: %v", providerKey, agent.Key, err))
		return nil
	}
	model := firstNonBlank(embeddingCfg.Model, provider.Memory.Embedding.Model)
	dimension := firstPositiveInt(embeddingCfg.Dimension, provider.Memory.Embedding.Dimension)
	timeout := firstPositiveInt(embeddingCfg.Timeout, provider.Memory.Embedding.Timeout, 15)
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" || model == "" || dimension <= 0 {
		logMemoryEmbeddingOnce(logOnce, agent.Key, providerKey, "incomplete", fmt.Sprintf("[memory][embedding] disabled for agent %s: provider %s missing baseURL/model/dimension", agent.Key, providerKey))
		return nil
	}
	return memory.NewEmbeddingProvider(baseURL, provider.APIKey, model, dimension, timeout)
}

func logMemoryEmbeddingOnce(logOnce *sync.Map, agentKey string, providerKey string, reason string, message string) {
	if logOnce == nil {
		log.Print(message)
		return
	}
	key := strings.TrimSpace(agentKey) + "|" + strings.TrimSpace(providerKey) + "|" + strings.TrimSpace(reason)
	if _, loaded := logOnce.LoadOrStore(key, struct{}{}); !loaded {
		log.Print(message)
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// lazyGatewayResolver 把 artifactpusher 的 resolver 和 Registry 构建解耦。
// pusher 在 Registry 就绪前先创建；Registry 就绪后 SetRegistry 绑定实际实现。
// Registry 未绑定时 Resolve 返回 ok=false（等同于 gateway 未配置），pusher 会跳过上传。
type lazyGatewayResolver struct {
	mu    sync.RWMutex
	reg   *gateway.Registry
	chats interface {
		SourceChannel(chatID string) (string, error)
	}
}

func (l *lazyGatewayResolver) SetRegistry(r *gateway.Registry) {
	l.mu.Lock()
	l.reg = r
	l.mu.Unlock()
}

func (l *lazyGatewayResolver) Registry() *gateway.Registry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.reg
}

func (l *lazyGatewayResolver) Resolve(chatID string) (string, string, bool) {
	l.mu.RLock()
	r := l.reg
	chats := l.chats
	l.mu.RUnlock()
	if r == nil {
		return "", "", false
	}
	if chats != nil {
		if sourceChannel, err := chats.SourceChannel(chatID); err == nil && strings.TrimSpace(sourceChannel) != "" {
			if baseURL, token, ok := r.ResolveSourceChannel(sourceChannel); ok {
				return baseURL, token, true
			}
		}
	}
	return r.Resolve(chatID)
}

func summarizeAutomationErrorBody(body string) string {
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
