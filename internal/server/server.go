package server

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/catalog"
	"agent-platform/internal/catalogorder"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
	"agent-platform/internal/kbase"
	"agent-platform/internal/memory"
	"agent-platform/internal/models"
	projectpkg "agent-platform/internal/project"
	runtimeproxy "agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/skills"
	terminalpkg "agent-platform/internal/terminal"
	"agent-platform/internal/toolinteraction"
	"agent-platform/internal/ws"
)

// KBaseService is the HTTP-facing KBASE surface. The concrete manager remains
// an application facade, while server depends only on the operations it uses.
type KBaseService interface {
	ValidateAgent(agentKey string) error
	Status(agentKey string) (kbase.Status, error)
	Refresh(ctx context.Context, agentKey string, options kbase.RefreshOptions) (kbase.RefreshResult, error)
	ProbeSidecar(ctx context.Context) (required bool, state kbase.LanceEngineState, err error)
	ReconcileWatchers(ctx context.Context)
}

type MCPToolSyncStatusProvider interface {
	ServerStatus(serverKey string) (api.MCPServerToolSyncStatus, bool)
}

// QueryRuntime is the narrow application boundary used by the transport
// adapters. Server has no access to runtime assembly or executor internals.
type QueryRuntime interface {
	StartQuery(context.Context, runtimetypes.QueryCommand) (runtimetypes.RunHandle, error)
	AttachRun(context.Context, runtimetypes.RunRef, int64) (*runtimetypes.Subscription, error)
	Submit(context.Context, runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error)
	Steer(context.Context, runtimetypes.SteerCommand) (runtimetypes.SteerResult, error)
	Interrupt(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error)
	SetAccessLevel(context.Context, runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error)
}

type Dependencies struct {
	BackgroundContext      context.Context
	Config                 config.Config
	Chats                  chat.Store
	Archives               *chat.ArchiveStore
	Archiver               *chat.Archiver
	Memory                 memory.Store
	KBase                  KBaseService
	Registry               catalog.Registry
	Models                 *models.ModelRegistry
	Runs                   contracts.RunManager
	Agent                  contracts.AgentEngine
	Tools                  contracts.ToolExecutor
	Sandbox                contracts.SandboxClient
	MCP                    contracts.McpClient
	MCPToolSyncStatus      MCPToolSyncStatusProvider
	Viewport               contracts.ViewportClient
	ToolInteractions       *toolinteraction.Registry
	CatalogReloader        contracts.CatalogReloader
	Notifications          contracts.NotificationSink
	SkillCandidates        skills.CandidateStore
	Channels               ChannelRegistry
	ChannelStatus          ChannelStatusProvider
	AutomationOrchestrator *automation.Orchestrator
	AutomationRegistry     *automation.Registry
	AutomationExecutions   automation.ExecutionHistoryReader
	DeltaMappers           contracts.StreamDeltaMapperFactory
	SystemInits            contracts.SystemInitBuilder
	AdminSources           *adminsource.Service
	ChatResources          *chatresource.Service
	Terminals              *terminalpkg.Manager
	Runtime                QueryRuntime
	ProxyRuntime           *runtimeproxy.Service
	Conversation           *conversation.Service
	Project                *projectpkg.Service
	DeferredAwaitings      DeferredAwaitingStore
	// GatewayResolver 按 chatId 查对应 gateway 的 BaseURL/Token。
	GatewayResolver  GatewayResolver
	AgentCardStatus  AgentCardStatusProvider
	AgentCardRefresh AgentCardRefreshScheduler
	ChannelSessions  ChannelSessionObserver
}

// GatewayResolver 是 ws_routes 下载时用来按 chatId 选对应 gateway 的只读视图，
// 由 internal/gateway.Registry 提供实现；放在 server 包避免 server → gateway 的直接 import。
type GatewayResolver interface {
	Resolve(chatID string) (baseURL string, token string, ok bool)
}

type AgentCardStatusProvider interface {
	AgentCardStatus(channelID string, externalAgentKey string) (api.GatewayAgentCardReportStatus, bool)
}

type AgentCardRefreshScheduler interface {
	ScheduleRefresh()
}

type ChannelSessionObserver interface {
	ChannelConnected(channelID string, conn *ws.Conn, handshakeTimeout time.Duration)
	ChannelPush(channelID string, conn *ws.Conn, push ws.PushFrame)
	ChannelDisconnected(channelID string, conn *ws.Conn)
}

type ChannelRegistry interface {
	Lookup(channelID string) (*channel.Definition, bool)
	All() []*channel.Definition
}

type ChannelStatusProvider interface {
	Connected(channelID string) bool
}

type ChannelConnectionProvider interface {
	GatewayConnection(channelID string) (*ws.Conn, bool)
}

type ChannelConnectionSnapshotProvider interface {
	GatewayConnections(channelID string) []ws.MonitorConnection
}

type Server struct {
	router            *http.ServeMux
	deps              Dependencies
	authVerifier      *JWTVerifier
	ticketService     *ResourceTicketService
	wsHandler         *ws.Handler
	terminals         *terminalpkg.Manager
	deferredAwaitings DeferredAwaitingStore
	adminSources      *adminsource.Service
	chatResources     *chatresource.Service
	proxyRuntime      *runtimeproxy.Service
	project           *projectpkg.Service
	backgroundCtx     context.Context
	backgroundCancel  context.CancelFunc
	shutdownHookOnce  sync.Once
	connectorAuth     *connectorauth.Manager
	skillOrder        *catalogorder.FileOrderStore
	connectorOrder    *catalogorder.FileOrderStore
}

type syncQueryContextKey struct{}
type chatSourceContextKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Locale() string {
	if provider, ok := r.ResponseWriter.(localeProvider); ok {
		return provider.Locale()
	}
	return ""
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func New(deps Dependencies) (*Server, error) {
	if deps.AdminSources == nil {
		deps.AdminSources = adminsource.NewService()
	}
	if deps.ChatResources == nil {
		deps.ChatResources = chatresource.NewService(deps.Chats)
	}
	if deps.Terminals == nil {
		deps.Terminals = terminalpkg.NewManager()
	}
	if deps.Conversation == nil {
		deps.Conversation = conversation.NewService(deps.Chats, deps.Archives, deps.Archiver, deps.Runs)
	}
	if deps.ProxyRuntime == nil {
		deps.ProxyRuntime = runtimeproxy.NewService()
	}
	if deps.Project == nil {
		reader, _ := deps.Tools.(contracts.ProjectFileHistoryReader)
		deps.Project = &projectpkg.Service{
			Registry: deps.Registry, Chats: deps.Chats, History: reader,
			ChatsRoot: deps.Config.Paths.ChatsDir, MaxReadBytes: deps.Config.FileTools.MaxReadBytes,
		}
	}
	if deps.DeferredAwaitings == nil {
		deps.DeferredAwaitings = runstate.NewDeferredAwaitingStore()
	}
	authVerifier := NewJWTVerifier(deps.Config.Auth)
	if deps.Config.Auth.Enabled {
		if err := authVerifier.ValidateConfiguration(); err != nil {
			return nil, fmt.Errorf("validate auth config: %w", err)
		}
		switch authVerifier.Mode() {
		case "local-public-key":
			log.Printf("auth enabled: mode=local-public-key public_key=%s", deps.Config.Auth.LocalPublicKeyFile)
		case "jwks":
			log.Printf("auth enabled: mode=jwks jwks_uri=%s", deps.Config.Auth.JWKSURI)
		}
	} else {
		log.Printf("auth disabled")
	}
	if deps.Notifications == nil {
		deps.Notifications = contracts.NewNoopNotificationSink()
	}
	backgroundCtx := deps.BackgroundContext
	var backgroundCancel context.CancelFunc
	if backgroundCtx == nil {
		backgroundCtx, backgroundCancel = context.WithCancel(context.Background())
	}
	s := &Server{
		router:            http.NewServeMux(),
		deps:              deps,
		authVerifier:      authVerifier,
		ticketService:     NewResourceTicketService(deps.Config.ResourceTicket),
		terminals:         deps.Terminals,
		deferredAwaitings: deps.DeferredAwaitings,
		adminSources:      deps.AdminSources,
		chatResources:     deps.ChatResources,
		proxyRuntime:      deps.ProxyRuntime,
		project:           deps.Project,
		backgroundCtx:     backgroundCtx,
		backgroundCancel:  backgroundCancel,
	}
	s.skillOrder = catalogorder.NewFileOrderStore(deps.Config.Paths.SkillsCenterDir)
	s.connectorOrder = catalogorder.NewFileOrderStore(deps.Config.Paths.EffectiveConnectorsCenterDir())
	s.connectorAuth = connectorauth.New(backgroundCtx, s.connectorSources(), func(ctx context.Context, _ string) error {
		if s.deps.CatalogReloader != nil {
			return s.deps.CatalogReloader.Reload(ctx, "connectors")
		}
		return nil
	}).WithIdentityFile(s.deps.Config.IdentityFile)
	if s.deps.Runtime == nil {
		// Compatibility for direct package tests and small embedders. app.New
		// always supplies the assembled Runtime service.
		s.deps.Runtime = &runtimeCompatibilityAdapter{server: s}
	}
	if err := s.hydrateDeferredAwaitings(); err != nil {
		return nil, fmt.Errorf("reconcile persisted awaitings: %w", err)
	}
	if hub, ok := deps.Notifications.(*ws.Hub); ok {
		s.wsHandler = s.newWSHandler(hub)
	}
	s.routes()
	return s, nil
}

func (s *Server) SetChannelStatusProvider(provider ChannelStatusProvider) {
	if s == nil {
		return
	}
	s.deps.ChannelStatus = provider
}

// ExecuteInternalQuery reuses the normal query handling pipeline for
// in-process callers such as the automation orchestrator, while intentionally
// bypassing the outer HTTP auth gate enforced by ServeHTTP.
