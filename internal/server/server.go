package server

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/catalog"
	"agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/memory"
	"agent-platform/internal/models"
	"agent-platform/internal/skills"
	terminalpkg "agent-platform/internal/terminal"
	"agent-platform/internal/ws"
)

type Dependencies struct {
	Config                 config.Config
	Chats                  chat.Store
	Archives               *chat.ArchiveStore
	Archiver               *chat.Archiver
	Memory                 memory.Store
	KBase                  *kbase.Manager
	Registry               catalog.Registry
	Models                 *models.ModelRegistry
	Runs                   contracts.RunManager
	Agent                  contracts.AgentEngine
	Tools                  contracts.ToolExecutor
	Sandbox                contracts.SandboxClient
	MCP                    contracts.McpClient
	Viewport               contracts.ViewportClient
	FrontendTools          *frontendtools.Registry
	CatalogReloader        contracts.CatalogReloader
	Notifications          contracts.NotificationSink
	SkillCandidates        skills.CandidateStore
	Channels               ChannelRegistry
	ChannelStatus          ChannelStatusProvider
	AutomationOrchestrator *automation.Orchestrator
	AutomationRegistry     *automation.Registry
	AutomationExecutions   *automation.ExecutionStore
	DeltaMappers           contracts.StreamDeltaMapperFactory
	SystemInits            contracts.SystemInitBuilder
	// GatewayResolver 按 chatId 查对应 gateway 的 BaseURL/Token。
	GatewayResolver  GatewayResolver
	AgentCardStatus  AgentCardStatusProvider
	AgentCardRefresh AgentCardRefreshScheduler
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
	deferredAwaitings *DeferredAwaitingStore
	uploadMu          sync.Mutex
	adminSourceMu     sync.Mutex
	proxyMu           sync.RWMutex
	proxyRuns         map[string]*proxyRunRoute
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
	s := &Server{
		router:            http.NewServeMux(),
		deps:              deps,
		authVerifier:      authVerifier,
		ticketService:     NewResourceTicketService(deps.Config.ResourceTicket),
		terminals:         terminalpkg.NewManager(),
		deferredAwaitings: NewDeferredAwaitingStore(),
		proxyRuns:         map[string]*proxyRunRoute{},
	}
	s.hydrateDeferredAwaitings()
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
