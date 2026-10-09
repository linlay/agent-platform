package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
	"agent-platform/internal/artifactpusher"
	"agent-platform/internal/automation"
	"agent-platform/internal/builtins"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
	"agent-platform/internal/credentialview"
	"agent-platform/internal/gateway"
	"agent-platform/internal/hostshell"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/kbx"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/llm"
	"agent-platform/internal/lsp"
	"agent-platform/internal/mcp"
	"agent-platform/internal/memory"
	"agent-platform/internal/memoryworker"
	"agent-platform/internal/models"
	"agent-platform/internal/platformcontrol"
	projectpkg "agent-platform/internal/project"
	"agent-platform/internal/reload"
	"agent-platform/internal/runenvops"
	"agent-platform/internal/runops"
	agentruntime "agent-platform/internal/runtime"
	runtimeadapter "agent-platform/internal/runtime/adapter"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	runtimequery "agent-platform/internal/runtime/query"
	"agent-platform/internal/runtime/runstate"
	runtimesession "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/sandbox"
	"agent-platform/internal/server"
	"agent-platform/internal/skills"
	"agent-platform/internal/supportpkg"
	"agent-platform/internal/terminal"
	"agent-platform/internal/toolinteraction"
	"agent-platform/internal/tools"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type App struct {
	nativeConnectorRelease func()
	Config                 config.Config
	RuntimeEnv             runtimeenv.Info
	Router                 *server.Server
	backgroundCancel       context.CancelFunc
	automation             automationStopper
	gateways               *gateway.Registry
	wsHub                  *ws.Hub
	automationExecutions   *automation.ExecutionHistoryService
	lspManager             *lsp.Manager
	mcpClient              *mcp.Client
	knowledgeManager       *kbx.Manager
	knowledgeCenter        *kbasescenter.Service
	memoryWorker           *memoryworker.Worker
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
	if err := httpclient.ConfigureDefault(cfg.HTTPProxy); err != nil {
		return nil, fmt.Errorf("initialize HTTP clients: %w", err)
	}
	if err := hostshell.Configure(&cfg.Bash, hostEnv.GOOS, hostEnv.GOARCH); err != nil {
		return nil, fmt.Errorf("initialize host shell: %w", err)
	}
	if hostshell.Enabled(cfg.Bash, hostEnv.GOOS) {
		log.Printf("verified bundled Git Bash runtime: %s", cfg.Bash.GitBash.RuntimeRoot)
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
		"loaded config in %s (registries=%s agents=%s ru-agents=%s teams=%s skills=%s chats=%s memory=%s)",
		startupElapsed(configStartedAt),
		cfg.Paths.RegistriesDir,
		cfg.Paths.AgentsDir,
		cfg.Paths.RUAgentsDir,
		cfg.Paths.TeamsDir,
		cfg.Paths.SkillsCenterDir,
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

	memoryLocation, err := time.LoadLocation(cfg.Memory.Timezone)
	if err != nil {
		return nil, fmt.Errorf("memory timezone: %w", err)
	}
	memoryStore := memory.NewStore(cfg.Paths.MemoryDir, cfg.Paths.OwnerDir, memoryLocation)
	if err := memoryStore.PrepareSummary(); err != nil {
		return nil, fmt.Errorf("prepare memory summary: %w", err)
	}
	skillCandidateStore, err := skills.NewFileCandidateStore(filepath.Join(cfg.Paths.MemoryDir, "skill-candidates"))
	if err != nil {
		return nil, err
	}

	modelRegistryStartedAt := time.Now()
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		return nil, fmt.Errorf("load model registry (%s): %w", cfg.Paths.RegistriesDir, err)
	}
	log.Printf("model registry ready in %s (root=%s)", startupElapsed(modelRegistryStartedAt), cfg.Paths.RegistriesDir)

	runManager := runstate.NewManager().WithStateRoot(cfg.Paths.StateDir)
	runtimeService := agentruntime.NewService()
	proxyRuntime := runtimeproxy.NewService()
	wsHub := ws.NewHub()
	sandboxClient := sandbox.NewContainerHubSandboxService(cfg.ContainerHub, cfg.Paths)
	runtimeToolExecutor, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, chatStore, skillCandidateStore)
	if err != nil {
		return nil, fmt.Errorf("init runtime tools: %w", err)
	}
	runtimeToolExecutor.WithClientRequestInvoker(wsHub)
	runtimeToolExecutor.WithClientTargetStore(runManager)
	runtimeToolExecutor.WithDesktopMainTargetProvider(wsHub)
	runtimeToolExecutor.WithRuntimeEnv(hostEnv)
	runtimeToolExecutor.WithModelRegistry(modelRegistry)
	var lspManager *lsp.Manager
	if cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics.Enabled {
		lspManager = lsp.NewManager(cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics)
	}
	// artifactPusher 在下面 notifications 就绪后再接入 runtimeToolExecutor，
	// 这样它发出的 push frame 能走到 WS hub，转给网关做 artifact 预告。
	cfg.Paths.BuiltinConnectorsDir, err = builtins.ProcessConnectorsRoot()
	if err != nil {
		return nil, fmt.Errorf("load builtin connectors: %w", err)
	}
	nativeRelease, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		return nil, fmt.Errorf("load embedded Desktop connector: %w", err)
	}
	cleanupNative := true
	defer func() {
		if cleanupNative {
			nativeRelease()
		}
	}()
	mcpRegistry, err := mcp.NewAgentRegistry(cfg.Paths.ConnectorSources())
	if err != nil {
		return nil, fmt.Errorf("load mcp registry: %w", err)
	}
	mcpGate := mcp.NewAvailabilityGate()
	mcpClient := mcp.NewClientWithGate(mcpRegistry, nil, mcpGate).WithIdentityFile(cfg.IdentityFile)
	cleanupMCP := true
	defer func() {
		if cleanupMCP {
			_ = mcpClient.Close()
		}
	}()
	mcpToolSync := mcp.NewToolSync(mcpRegistry, mcpClient)
	runtimeTools, err := tools.LoadRuntimeToolDefinitions(cfg.Paths.ToolsDir)
	if err != nil {
		return nil, fmt.Errorf("load runtime tools: %w", err)
	}
	interactionRegistry := toolinteraction.NewDefaultRegistry()
	toolExecutor, err := tools.NewToolRouter(
		runtimeToolExecutor,
		mcpClient,
		mcpToolSync,
		llm.NewInteractionSubmitCoordinator(interactionRegistry),
		append([]api.ToolDetailResponse(nil), runtimeTools...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize tool router: %w", err)
	}

	registryStartedAt := time.Now()
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		return nil, fmt.Errorf(
			"load catalog registry (agents=%s ru-agents=%s teams=%s skills=%s): %w",
			cfg.Paths.AgentsDir,
			cfg.Paths.RUAgentsDir,
			cfg.Paths.TeamsDir,
			cfg.Paths.SkillsCenterDir,
			err,
		)
	}
	if err := mcpRegistry.BindAgents(registry); err != nil {
		return nil, fmt.Errorf("bind Agent MCP instances: %w", err)
	}
	mcpToolSync.ReconcileRegistry()
	kbaseSource := knowledgeCatalogSource{registry: registry}
	kbxConfig := &kbx.ModelConfigSource{File: filepath.Join(cfg.Paths.StateDir, "kbx", "index.yml"), Registry: modelRegistry, ModelKey: cfg.KBX.Embedding.ModelKey, Prompt: cfg.KBX.Embedding.Prompt}
	if _, err := kbxConfig.Snapshot(); err != nil {
		return nil, fmt.Errorf("configure KBX: %w", err)
	}
	knowledgeManager := kbx.NewManager(kbx.Options{StateDir: cfg.Paths.StateDir, ConfigSource: kbxConfig}, kbaseSource, modelRegistry)
	if lspManager != nil {
		runtimeToolExecutor.WithFileChangeHooks(lspManager)
	}
	log.Printf(
		"catalog registry ready in %s (agents=%d teams=%d skills=%d tools=%d)",
		startupElapsed(registryStartedAt),
		len(registry.Agents("")),
		len(registry.Teams()),
		len(registry.Skills("")),
		len(toolExecutor.Definitions()),
	)
	if err := toolExecutor.RegisterHandler(knowledge.NewToolHandler(knowledgeManager)); err != nil {
		return nil, fmt.Errorf("register KBASE tools: %w", err)
	}

	agentEngine := llm.NewLLMAgentEngineWithHTTPClient(cfg, modelRegistry, toolExecutor, interactionRegistry, sandboxClient, httpclient.NewClient(0))
	var notifications contracts.NotificationSink = wsHub
	// gatewayResolver 在 Registry 构建完成后（server 依赖就绪之后）绑定。
	// pusher 先拿到 resolver 指针，Registry 构建完调用 SetRegistry 就能工作。
	gatewayResolver := &lazyGatewayResolver{chats: chatStore}
	runtimeToolExecutor.WithArtifactPusher(artifactpusher.New(artifactpusher.Config{
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
	legacyRoot := filepath.Join(filepath.Dir(cfg.Paths.KBasesDir), "kbase")
	if entries, readErr := os.ReadDir(legacyRoot); readErr == nil && len(entries) > 0 {
		return nil, fmt.Errorf("legacy Agent indexes found at %s; stop Platform, back up and move this directory outside runtime, configure libraryId bindings and rebuild shared libraries", legacyRoot)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return nil, readErr
	}
	centerEngine := kbx.NewCenterEngineWithSource(kbxConfig)
	kbasesCenter, err := kbasescenter.New(backgroundCtx, cfg.Paths.KBasesDir, cfg.Paths.RUKBasesDir, centerEngine, kbasescenter.Options{ChatsDir: cfg.Paths.ChatsDir, StateDir: cfg.Paths.StateDir, RuntimeDir: filepath.Dir(cfg.Paths.KBasesDir), References: func(id string) []string {
		refs := []string{}
		for _, a := range registry.AdminAgents() {
			binding := contracts.AnyMapNode(a.Definition["kbaseConfig"])
			if binding["libraryId"] == id {
				refs = append(refs, a.Key)
			}
		}
		sort.Strings(refs)
		return refs
	}})
	if err != nil {
		return nil, fmt.Errorf("initialize knowledge base center: %w", err)
	}
	knowledgeManager.BindCenter(kbasesCenter)
	if err := kbasesCenter.Start(); err != nil {
		return nil, err
	}
	cardReporter := gateway.NewAgentCardReporter(backgroundCtx, registry)
	mcpSyncCoordinator := mcp.NewSyncCoordinator(mcpRegistry, mcpToolSync, mcpGate, 10*time.Second, notifications)
	mcpReloader := mcp.NewRegistryReloader(mcpRegistry, mcpToolSync, mcpSyncCoordinator)
	mcpReloader.WatchCredentials(backgroundCtx)
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, mcpReloader, toolExecutor, cfg.Paths.ToolsDir, notifications)
	registry.SetRuntimeReload(func() {
		if backgroundCtx.Err() == nil {
			go func() {
				if err := reloader.Reload(backgroundCtx, "agents"); err != nil {
					log.Printf("[reload] deferred Agent runtime: %v", err)
				}
			}()
		}
	})
	reloader.AddObserver(cardReporter)
	reload.StartBackgroundReloaders(backgroundCtx, cfg, reloader)
	log.Printf("background file watchers started (agents=%s teams=%s skills=%s)",
		cfg.Paths.AgentsDir,
		cfg.Paths.TeamsDir,
		cfg.Paths.SkillsCenterDir,
	)

	var channelReg *channel.Registry
	if len(cfg.Channels) > 0 {
		channelReg = channel.NewRegistry(cfg.Channels)
		log.Printf("channel registry ready (%d channels)", len(cfg.Channels))
	}

	var srv *server.Server
	var automationOrchestrator *automation.Orchestrator
	var automationRegistry *automation.Registry
	var automationExecutionHistory *automation.ExecutionHistoryService
	if cfg.Automation.Enabled {
		automationRegistry = automation.NewRegistry(cfg.Automation.ExternalDir, registry)
		var automationBroadcaster automation.Broadcaster
		if hub, ok := notifications.(*ws.Hub); ok {
			automationBroadcaster = hub
		}
		automationExecutionHistory = automation.NewExecutionHistoryService(
			cfg.Automation.ExternalDir,
			"executions.db",
			automationBroadcaster,
			func(chatID, runID string) (*chat.RunSummary, error) {
				runs, err := chatStore.ListRuns(chatID)
				if errors.Is(err, chat.ErrChatNotFound) {
					return nil, nil
				}
				if err != nil {
					return nil, err
				}
				for i := range runs {
					if runs[i].RunID == runID {
						item := runs[i]
						return &item, nil
					}
				}
				return nil, nil
			},
		)
		dispatcher := automation.NewDispatcher(func(ctx context.Context, req api.QueryRequest, hooks automation.QueryRunHooks) (automation.QueryRunResult, error) {
			if strings.TrimSpace(req.Role) == "" {
				req.Role = api.QueryRoleAutomation
			}
			result, err := runtimeService.ExecuteQueryWithHooks(ctx, runtimeQueryCommand(req), runtimetypes.QueryHooks{OnRunStarted: hooks.OnRunStarted})
			queryResult := automation.QueryRunResult{Completion: result.Completion, ErrorMessage: result.ErrorMessage}
			if err != nil {
				return queryResult, err
			}
			if result.Completion != nil {
				switch strings.ToLower(strings.TrimSpace(result.Completion.FinishReason)) {
				case "error":
					message := firstNonBlankString(result.ErrorMessage, "automation query run failed")
					return queryResult, fmt.Errorf("%s", message)
				case "cancel":
					message := firstNonBlankString(result.ErrorMessage, "automation query run canceled")
					return queryResult, fmt.Errorf("%s", message)
				}
			}
			return queryResult, nil
		}, automationBroadcaster, automationExecutionHistory)
		automationOrchestrator = automation.NewOrchestrator(automationRegistry, dispatcher, cfg.Automation)
	}

	serverStartedAt := time.Now()
	adminSourceService := adminsource.NewService()
	chatResourceService := chatresource.NewService(chatStore)
	terminalManager := terminal.NewManager()
	conversationService := conversation.NewService(chatStore, archiveStore, archiver, runManager)
	conversationService.Notifications = notifications
	conversationService.ControlStateDir = filepath.Join(cfg.Paths.EffectiveStateDir(), "conversation-control")
	if err := toolExecutor.RegisterHandler(runenvops.NewToolHandler(cfg.RunEnv)); err != nil {
		return nil, fmt.Errorf("register run_env tool: %w", err)
	}
	automationService := &automation.Service{Registry: automationRegistry, Orchestrator: automationOrchestrator, History: automationExecutionHistory, DefaultZoneID: cfg.Automation.DefaultZoneID, ReceiptDir: filepath.Join(cfg.Paths.EffectiveStateDir(), "automation-control")}
	controlHandler := platformcontrol.NewToolHandler(cfg, registry, conversationService).ConfigureControl(&adminsource.ControlService{Mutations: adminSourceService, Config: cfg, Registry: registry, Models: modelRegistry, Reload: reloader.Reload, Coordinate: reloader.WithCatalogDirectoryMutation}, conversationService, modelRegistry).ConfigureAutomation(automationService)
	controlHandler.RuntimeSnapshot = func() map[string]any {
		statuses := []map[string]any{}
		for _, server := range mcpRegistry.Servers() {
			if len(statuses) >= 100 {
				break
			}
			status, known := mcpToolSync.ServerStatus(server.Key)
			statuses = append(statuses, map[string]any{"key": server.Key, "known": known, "sync": status})
		}
		return map[string]any{"platform": map[string]any{"runtimeMode": cfg.RuntimeMode, "uptimeSeconds": int64(time.Since(serverStartedAt).Seconds())}, "mcp": map[string]any{"servers": statuses, "count": len(mcpRegistry.Servers()), "cached": true}, "kbase": knowledgeManager.RuntimeSnapshot()}
	}
	if err := toolExecutor.RegisterHandler(controlHandler); err != nil {
		return nil, fmt.Errorf("register platform control tools: %w", err)
	}
	deferredAwaitings := runstate.NewDeferredAwaitingStore()
	var projectHistory contracts.ProjectFileHistoryReader = toolExecutor
	projectService := &projectpkg.Service{
		Registry: registry, Chats: chatStore, History: projectHistory,
		ChatsRoot: cfg.Paths.ChatsDir, MaxReadBytes: cfg.FileTools.MaxReadBytes,
	}
	systemInits := llm.NewSystemInitProfileBuilder(modelRegistry, llm.SystemInitDefaults{
		PlanMaxSteps:             cfg.Defaults.Plan.MaxSteps,
		PlanMaxWorkRoundsPerTask: cfg.Defaults.Plan.MaxWorkRoundsPerTask,
		CoderPlanningMaxSteps:    cfg.Defaults.CoderPlanning.MaxSteps,
		Prompts:                  cfg.Prompts,
	})
	profiles := runtimeadapter.Profiles{Builder: systemInits, Tools: toolExecutor}
	sessions := runtimesession.New(runtimesession.Dependencies{ValidateKnowledge: knowledgeManager.ValidateRun, Config: cfg, Chats: chatStore, Registry: runtimeadapter.Catalog{Registry: registry}, Models: modelRegistry, Runs: runManager, Tools: toolExecutor, Profiles: profiles})
	memoryClient := memoryworker.Client{Root: cfg.Paths.MemoryDir, Timezone: cfg.Memory.Timezone, ConfigDir: filepath.Join(cfg.Paths.StateDir, "memx")}
	memoryWorker := memoryworker.New(cfg.Memory, memoryworker.StateRoot(cfg.Paths.StateDir), chatStore,
		memoryClient,
		&memoryworker.ModelConfigSync{ModelKey: cfg.Memory.Worker.ModelKey, Models: modelRegistry, TimeoutSeconds: cfg.Memory.Worker.TimeoutSeconds, Client: memoryClient},
		func(key string) (string, bool) {
			def, ok := registry.AgentDefinition(key)
			return def.Workspace.ProjectDir(), ok && def.MemoryConfig.Enabled && def.Engine == catalog.AgentEngineNative && def.ProxyConfig == nil && def.Mode != "CHANNEL"
		}).WithArchives(archiveStore)
	srv, err = server.New(server.Dependencies{
		BackgroundContext:      backgroundCtx,
		Config:                 cfg,
		Chats:                  chatStore,
		Archives:               archiveStore,
		Archiver:               archiver,
		Memory:                 memoryStore,
		MemoryMaintenance:      memoryWorker,
		KBase:                  knowledgeManager,
		KBasesCenter:           kbasesCenter,
		Registry:               registry,
		Models:                 modelRegistry,
		Runs:                   runManager,
		Agent:                  agentEngine,
		Tools:                  toolExecutor,
		Sandbox:                sandboxClient,
		MCP:                    mcpClient,
		MCPToolSyncStatus:      mcpToolSync,
		ToolInteractions:       interactionRegistry,
		CatalogReloader:        reloader,
		Notifications:          notifications,
		SkillCandidates:        skillCandidateStore,
		Channels:               channelReg,
		AutomationOrchestrator: automationOrchestrator,
		DeltaMappers:           llm.DeltaMapperFactory{Interactions: interactionRegistry, CredentialPolicy: credentialview.FromConfig(cfg)},
		SystemInits:            systemInits,
		Sessions:               sessions,
		AutomationRegistry:     automationRegistry,
		AutomationService:      automationService,
		AutomationExecutions:   automationExecutionHistory,
		AdminSources:           adminSourceService,
		ChatResources:          chatResourceService,
		Terminals:              terminalManager,
		Runtime:                runtimeService,
		ProxyRuntime:           proxyRuntime,
		Conversation:           conversationService,
		Project:                projectService,
		DeferredAwaitings:      deferredAwaitings,
		GatewayResolver:        gatewayResolver,
		AgentCardStatus:        cardReporter,
		AgentCardRefresh:       cardReporter,
		ChannelSessions:        cardReporter,
	})
	if err != nil {
		if automationExecutionHistory != nil {
			_ = automationExecutionHistory.Close()
		}
		return nil, fmt.Errorf("init server: %w", err)
	}

	queryService := runtimequery.NewService(runtimequery.Dependencies{
		BackgroundContext: backgroundCtx, Config: cfg, Runs: runManager, Chats: chatStore, Registry: registry, Models: modelRegistry, Tools: toolExecutor,
		Agent: runtimeadapter.Engine{AgentEngine: agentEngine}, Profiles: profiles, Sessions: sessions,
		Notifications: notifications, ToolInteractions: interactionRegistry, DeltaMappers: llm.DeltaMapperFactory{Interactions: interactionRegistry, CredentialPolicy: credentialview.FromConfig(cfg)},
		DeferredAwaitings: deferredAwaitings, Proxy: server.RuntimeProxyPort{Server: srv}, ResourceTickets: srv.RuntimeResourceTickets(),
	})
	runtimeService.Bind(queryService)
	runtimeToolExecutor.WithWaitConditionProvider(waitEventProvider{runs: runops.NewToolHandler(runtimeService, runManager), auth: srv})
	if err := queryService.Reconcile(); err != nil {
		return nil, fmt.Errorf("reconcile persisted awaitings: %w", err)
	}

	if err := toolExecutor.RegisterHandler(runops.NewToolHandler(runtimeService, runManager)); err != nil {
		return nil, fmt.Errorf("register run tools: %w", err)
	}
	log.Printf("server dependencies wired in %s", startupElapsed(serverStartedAt))
	mcpSyncCoordinator.Start(backgroundCtx)
	log.Printf("MCP background tool synchronization scheduled")

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
			if automationExecutionHistory != nil {
				_ = automationExecutionHistory.Close()
			}
			return nil, fmt.Errorf("start automation orchestrator: %w", err)
		}
		log.Printf("automation orchestrator started in %s (dir=%s)", startupElapsed(serverStartedAt), cfg.Automation.ExternalDir)
	} else {
		log.Printf("automation orchestrator disabled")
	}
	log.Printf("app dependencies initialized in %s", startupElapsed(appInitStartedAt))
	memoryWorker.Start(backgroundCtx)
	cleanupBackground = false
	cleanupMCP = false
	cleanupNative = false

	return &App{
		nativeConnectorRelease: nativeRelease,
		Config:                 cfg,
		RuntimeEnv:             hostEnv,
		Router:                 srv,
		backgroundCancel:       backgroundCancel,
		automation:             automationOrchestrator,
		gateways:               gwRegistry,
		wsHub:                  wsHub,
		automationExecutions:   automationExecutionHistory,
		lspManager:             lspManager,
		mcpClient:              mcpClient,
		knowledgeManager:       knowledgeManager,
		knowledgeCenter:        kbasesCenter,
		memoryWorker:           memoryWorker,
	}, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	if a.nativeConnectorRelease != nil {
		defer a.nativeConnectorRelease()
	}
	// Cancel startup/reconcile/watcher work before waiting for KBASE refreshes
	// and stopping its sidecar. This prevents an in-flight refresh from
	// restarting the process after shutdown has begun.
	if a.backgroundCancel != nil {
		a.backgroundCancel()
	}
	if a.memoryWorker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := a.memoryWorker.Wait(ctx); err != nil {
			log.Printf("close memory worker: %v", err)
		}
		cancel()
	}
	if a.knowledgeCenter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		if err := a.knowledgeCenter.Close(ctx); err != nil {
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
	return nil
}

func startupElapsed(startedAt time.Time) time.Duration {
	return time.Since(startedAt).Round(time.Millisecond)
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

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func runtimeQueryCommand(req api.QueryRequest) runtimetypes.QueryCommand {
	references := make([]runtimetypes.Reference, len(req.References))
	for index, reference := range req.References {
		references[index] = runtimetypes.Reference{
			ID: reference.ID, Type: reference.Type, Name: reference.Name, Path: reference.Path,
			MimeType: reference.MimeType, SizeBytes: reference.SizeBytes, URL: reference.URL,
			Text: reference.Text, Annotation: reference.Annotation, AnnotationIndex: reference.AnnotationIndex, SHA256: reference.SHA256, Meta: contracts.CloneMap(reference.Meta),
		}
	}
	var scene *runtimetypes.Scene
	if req.Scene != nil {
		scene = &runtimetypes.Scene{URL: req.Scene.URL, Title: req.Scene.Title}
	}
	var model *runtimetypes.QueryModelOptions
	if req.Model != nil {
		model = &runtimetypes.QueryModelOptions{
			Key: req.Model.Key, ModelID: req.Model.ModelID,
			ReasoningEffort: req.Model.ReasoningEffort, ServiceTier: req.Model.ServiceTier,
		}
	}
	return runtimetypes.QueryCommand{
		RequestID: req.RequestID, RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID,
		Role: req.Role, Hidden: req.Hidden, Message: req.Message, SourceUser: req.SourceUser, References: references,
		Params: contracts.CloneMap(req.Params), Scene: scene, Stream: req.Stream, IncludeUsage: req.IncludeUsage,
		IncludeFullText: req.IncludeFullText, PlanningMode: req.PlanningMode, EditingMode: req.EditingMode,
		MustUseSkills: append([]string(nil), req.MustUseSkills...), AccessLevel: req.AccessLevel, Model: model,
		SyntheticQueryBootstrapped: req.SyntheticQueryBootstrapped, ChatSource: req.ChatSource,
		TrustedQueryMetadata: contracts.CloneMap(req.TrustedQueryMetadata),
	}
}
