package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/artifactpusher"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/channel"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/gateway"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/mcp"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/observability"
	"agent-platform-runner-go/internal/reload"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/sandbox"
	"agent-platform-runner-go/internal/schedule"
	"agent-platform-runner-go/internal/server"
	"agent-platform-runner-go/internal/skills"
	"agent-platform-runner-go/internal/tools"
	"agent-platform-runner-go/internal/viewport"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

type App struct {
	Config           config.Config
	Router           *server.Server
	backgroundCancel context.CancelFunc
	scheduler        schedulerStopper
	gateways         *gateway.Registry
	wsHub            *ws.Hub
}

type schedulerStopper interface {
	Stop() context.Context
}

var schedulerStopTimeout = 3 * time.Second

func New(rootCtx context.Context) (*App, error) {
	appInitStartedAt := time.Now()
	if rootCtx == nil {
		rootCtx = context.Background()
	}

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
	if err := observability.InitMemoryLogger(cfg.Logging.Memory.Enabled, cfg.Logging.Memory.File); err != nil {
		return nil, fmt.Errorf("init memory logger (%s): %w", cfg.Logging.Memory.File, err)
	}
	log.Printf("initializing stores/registries")

	chatStoreStartedAt := time.Now()
	chatStore, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		return nil, fmt.Errorf("init chat store (%s): %w", cfg.Paths.ChatsDir, err)
	}
	log.Printf("chat store ready in %s (root=%s)", startupElapsed(chatStoreStartedAt), cfg.Paths.ChatsDir)

	var memoryStore memory.Store
	var sqliteMemoryStore *memory.SQLiteStore
	var skillCandidateStore skills.CandidateStore
	if cfg.Memory.Enabled {
		memoryStoreStartedAt := time.Now()
		sqliteMemoryStore, err = memory.NewSQLiteStore(cfg.Paths.MemoryDir, cfg.Memory.DBFileName)
		if err != nil {
			return nil, fmt.Errorf("init memory store (%s): %w", cfg.Paths.MemoryDir, err)
		}
		memoryStore = sqliteMemoryStore
		log.Printf("memory store ready in %s (root=%s)", startupElapsed(memoryStoreStartedAt), cfg.Paths.MemoryDir)
		skillCandidateStore, err = skills.NewFileCandidateStore(filepath.Join(cfg.Paths.MemoryDir, "skill-candidates"))
		if err != nil {
			return nil, fmt.Errorf("init skill candidate store (%s): %w", filepath.Join(cfg.Paths.MemoryDir, "skill-candidates"), err)
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

	if cfg.Memory.Enabled {
		if providerKey := strings.TrimSpace(cfg.Memory.EmbeddingProviderKey); providerKey != "" {
			if provider, err := modelRegistry.GetProvider(providerKey); err == nil {
				baseURL := strings.TrimRight(provider.BaseURL, "/")
				model := cfg.Memory.EmbeddingModel
				if model == "" {
					model = "text-embedding-3-small"
				}
				ep := memory.NewEmbeddingProvider(baseURL, provider.APIKey, model, cfg.Memory.EmbeddingDimension, cfg.Memory.EmbeddingTimeoutMs)
				sqliteMemoryStore.SetEmbedder(ep)
				log.Printf("memory embedding provider ready (provider=%s model=%s dim=%d)", providerKey, model, ep.Dimension)
			} else {
				log.Printf("[memory][embedding] provider %q not found in model registry, hybrid search disabled: %v", providerKey, err)
			}
		}
		if modelKey := strings.TrimSpace(cfg.Memory.RememberModelKey); modelKey != "" {
			if summarizer := memory.NewLLMMemorySummarizer(modelRegistry, modelKey, cfg.Memory.RememberTimeoutMs); summarizer != nil {
				sqliteMemoryStore.SetRememberSummarizer(summarizer)
				log.Printf("memory remember summarizer ready (model=%s timeout=%dms)", modelKey, cfg.Memory.RememberTimeoutMs)
			}
		}
	}

	runManager := runctl.NewInMemoryRunManager()
	sandboxClient := sandbox.NewContainerHubSandboxService(cfg.ContainerHub, cfg.Paths)
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, chatStore, memoryStore, skillCandidateStore)
	if err != nil {
		return nil, fmt.Errorf("init runtime tools: %w", err)
	}
	// artifactPusher 在下面 notifications 就绪后再接入 backendTools，
	// 这样它发出的 push frame 能走到 WS hub，转给网关做 artifact 预告。
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
	log.Printf(
		"catalog registry ready in %s (agents=%d teams=%d skills=%d tools=%d)",
		startupElapsed(registryStartedAt),
		len(registry.Agents("")),
		len(registry.Teams()),
		len(registry.Skills("")),
		len(toolExecutor.Definitions()),
	)

	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, frontendRegistry, sandboxClient)
	notifications := contracts.NewNoopNotificationSink()
	var wsHub *ws.Hub
	if cfg.WebSocket.Enabled {
		wsHub = ws.NewHub()
		notifications = wsHub
	}
	// gatewayResolver 在 Registry 构建完成后（server 依赖就绪之后）绑定。
	// pusher 先拿到 resolver 指针，Registry 构建完调用 SetRegistry 就能工作。
	gatewayResolver := &lazyGatewayResolver{}
	backendTools.WithArtifactPusher(artifactpusher.New(artifactpusher.Config{
		Resolver:      gatewayResolver,
		UploadPath:    config.GatewayUploadPath,
		ChatsDir:      cfg.Paths.ChatsDir,
		Notifications: notifications,
	}))
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, mcp.NewRegistryReloader(mcpRegistry, mcpToolSync), notifications)
	backgroundCtx, backgroundCancel := context.WithCancel(rootCtx)
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

	var channelReg *channel.Registry
	if len(cfg.Channels) > 0 {
		channelReg = channel.NewRegistry(cfg.Channels)
		log.Printf("channel registry ready (%d channels)", len(cfg.Channels))
	}

	serverStartedAt := time.Now()
	srv, err := server.New(server.Dependencies{
		Config:        cfg,
		Chats:         chatStore,
		Memory:        memoryStore,
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
		CatalogReloader: reloader,
		Notifications:   notifications,
		SkillCandidates: skillCandidateStore,
		Channels:        channelReg,
		GatewayResolver: gatewayResolver,
		GatewayAdmin:    &gatewayAdminAdapter{resolver: gatewayResolver},
	})
	if err != nil {
		return nil, fmt.Errorf("init server: %w", err)
	}
	log.Printf("server dependencies wired in %s", startupElapsed(serverStartedAt))

	// Gateway Registry 支持多条反向 WS 连接。legacy 单 gateway 部署在 config.normalize()
	// 阶段被合成为 Gateways[0]，这里只需按列表启动即可；行为与单 gateway 模式一致。
	var gwRegistry *gateway.Registry
	if cfg.WebSocket.Enabled && len(cfg.Gateways) > 0 {
		if hub, ok := notifications.(*ws.Hub); ok {
			if handler := srv.WSHandler(); handler != nil {
				gwRegistry = gateway.New(
					backgroundCtx,
					cfg.WebSocket,
					time.Duration(cfg.SSE.HeartbeatIntervalMs)*time.Millisecond,
					hub,
					handler.Dispatch,
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
		}
	}

	var scheduler *schedule.Orchestrator
	if cfg.Schedule.Enabled {
		scheduleRegistry := schedule.NewRegistry(cfg.Paths.SchedulesDir, registry)
		var scheduleBroadcaster schedule.Broadcaster
		if hub, ok := notifications.(*ws.Hub); ok {
			scheduleBroadcaster = hub
		}
		dispatcher := schedule.NewDispatcher(func(ctx context.Context, req api.QueryRequest) error {
			// schedule 触发的 run 标记为 hidden：
			// chat 不记录伪造的"用户发消息"，chat.created 也不广播，
			// webclient 仍能看到 assistant 侧输出，但不会渲染成"用户→agent"对话。
			hiddenTrue := true
			req.Hidden = &hiddenTrue
			status, body, err := srv.ExecuteInternalQuery(ctx, req)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("scheduled query failed with status %d: %s", status, summarizeScheduleErrorBody(body))
			}
			return nil
		}, scheduleBroadcaster)
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
		gateways:         gwRegistry,
		wsHub:            wsHub,
	}, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	if a.backgroundCancel != nil {
		a.backgroundCancel()
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
	if a.scheduler != nil {
		done := a.scheduler.Stop()
		select {
		case <-done.Done():
		case <-time.After(schedulerStopTimeout):
			log.Printf("scheduler stop timed out after %s", schedulerStopTimeout)
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

// gatewayAdminAdapter 把 gateway.Registry 桥接到 server.GatewayAdmin 接口，
// 避免 server 包直接 import gateway 包。Registry nil 时 List 返回空、增删返回 ErrNotConfigured。
type gatewayAdminAdapter struct {
	resolver *lazyGatewayResolver
}

func (a *gatewayAdminAdapter) AdminRegister(entry server.GatewayAdminEntry) error {
	reg := a.resolver.Registry()
	if reg == nil {
		return fmt.Errorf("gateway registry not initialized")
	}
	return reg.Register(config.GatewayEntry{
		ID:                 entry.ID,
		Channel:            entry.Channel,
		URL:                entry.URL,
		JwtToken:           entry.Token,
		BaseURL:            entry.BaseURL,
		HandshakeTimeoutMs: entry.HandshakeTimeoutMs,
		ReconnectMinMs:     entry.ReconnectMinMs,
		ReconnectMaxMs:     entry.ReconnectMaxMs,
	})
}

func (a *gatewayAdminAdapter) AdminUnregister(id string) error {
	reg := a.resolver.Registry()
	if reg == nil {
		return fmt.Errorf("gateway registry not initialized")
	}
	return reg.Unregister(id)
}

func (a *gatewayAdminAdapter) AdminList() []server.GatewayAdminEntry {
	reg := a.resolver.Registry()
	if reg == nil {
		return nil
	}
	entries := reg.All()
	out := make([]server.GatewayAdminEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, server.GatewayAdminEntry{
			ID:      e.ID,
			Channel: e.Channel,
			URL:     e.URL,
			BaseURL: e.BaseURL,
		})
	}
	return out
}

// lazyGatewayResolver 把 artifactpusher 的 resolver 和 Registry 构建解耦。
// pusher 在 Registry 就绪前先创建；Registry 就绪后 SetRegistry 绑定实际实现。
// Registry 未绑定时 Resolve 返回 ok=false（等同于 gateway 未配置），pusher 会跳过上传。
type lazyGatewayResolver struct {
	mu  sync.RWMutex
	reg *gateway.Registry
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
	l.mu.RUnlock()
	if r == nil {
		return "", "", false
	}
	return r.Resolve(chatID)
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
