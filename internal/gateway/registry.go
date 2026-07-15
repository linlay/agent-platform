// Package gateway 管理多条反向 WS 连接（wecom / feishu / ding / ...），
// 每条对应一个 channel 插件。Registry 是 connector 的索引：
//   - 按 Channel 做路由（artifact 外推、文件下载按 chatId 前缀选择 gateway）
package gateway

import (
	"context"
	"fmt"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/channel"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
	"agent-platform/internal/ws/gatewayclient"
)

// Entry 是 Registry 里一条 gateway 的运行态快照。
type Entry struct {
	ID            string
	Channel       string
	SourceChannel string
	SourcePrefix  string
	URL           string
	BaseURL       string
	Token         string // 返回给调用方时一般不暴露，仅内部使用
	client        *gatewayclient.Client
}

// Registry 线程安全。
type Registry struct {
	mu              sync.RWMutex
	entries         map[string]*Entry // id → entry
	byChannel       map[string]string // user channel id → id
	bySourceChannel map[string]string // full source channel (wecom:xiaozhai) → id
	bySourcePrefix  map[string]string // source/chatId prefix → id only when unambiguous

	// 依赖：新建 connector 时需要这些
	wsCfg     config.WebSocketConfig
	heartbeat time.Duration
	hub       *ws.Hub
	dispatch  ws.RouteHandler
	observers []ConnectionObserver

	rootCtx context.Context
}

type ConnectionObserver interface {
	GatewayConnected(gatewayID string, channelID string, conn *ws.Conn)
	GatewayDisconnected(gatewayID string, channelID string, conn *ws.Conn)
}

type registrationObserver interface {
	GatewayRegistered(gatewayID string, channelID string)
}

// New 创建 Registry。rootCtx 用于给每个 connector.Start 传递；Registry 自己不起 goroutine。
func New(rootCtx context.Context, wsCfg config.WebSocketConfig, heartbeat time.Duration, hub *ws.Hub, dispatch ws.RouteHandler, observers ...ConnectionObserver) *Registry {
	return &Registry{
		entries:         map[string]*Entry{},
		byChannel:       map[string]string{},
		bySourceChannel: map[string]string{},
		bySourcePrefix:  map[string]string{},
		wsCfg:           wsCfg,
		heartbeat:       heartbeat,
		hub:             hub,
		dispatch:        dispatch,
		rootCtx:         rootCtx,
		observers:       append([]ConnectionObserver(nil), observers...),
	}
}

// Register 启动一个 gateway connector 并加入 Registry。
// 若 id 已存在返回 ErrDuplicateID；URL/Token 为空返回 ErrInvalidConfig。
func (r *Registry) Register(entry config.GatewayEntry) error {
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return ErrInvalidConfig
	}
	if strings.TrimSpace(entry.URL) == "" {
		return ErrInvalidConfig
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[id]; exists {
		return ErrDuplicateID
	}
	channelKey := strings.TrimSpace(entry.Channel)
	sourceChannel := strings.TrimSpace(entry.SourceChannel)
	if sourceChannel == "" {
		sourceChannel = deriveSourceChannelFromURL(entry.URL)
	}
	sourcePrefix := strings.TrimSpace(entry.SourcePrefix)
	if sourcePrefix == "" {
		sourcePrefix = sourcePrefixFromChannel(sourceChannel)
	}
	if channelKey != "" {
		if _, exists := r.byChannel[channelKey]; exists {
			return ErrDuplicateChannel
		}
	}
	if sourceChannel != "" {
		if _, exists := r.bySourceChannel[sourceChannel]; exists {
			return ErrDuplicateChannel
		}
	}

	client := gatewayclient.New(
		gatewayclient.Config{
			ID:               id,
			Channel:          channelKey,
			URL:              strings.TrimSpace(entry.URL),
			BaseURL:          strings.TrimSpace(entry.BaseURL),
			Token:            strings.TrimSpace(entry.JwtToken),
			HandshakeTimeout: time.Duration(entry.HandshakeTimeout) * time.Second,
			ReconnectMin:     time.Duration(entry.ReconnectMin) * time.Second,
			ReconnectMax:     time.Duration(entry.ReconnectMax) * time.Second,
			OnConnected: func(conn *ws.Conn) {
				for _, observer := range r.observers {
					if observer != nil {
						observer.GatewayConnected(id, channelKey, conn)
					}
				}
			},
			OnDisconnected: func(conn *ws.Conn) {
				for _, observer := range r.observers {
					if observer != nil {
						observer.GatewayDisconnected(id, channelKey, conn)
					}
				}
			},
		},
		r.wsCfg,
		r.heartbeat,
		r.hub,
		r.dispatch,
	)
	for _, observer := range r.observers {
		if registered, ok := observer.(registrationObserver); ok {
			registered.GatewayRegistered(id, channelKey)
		}
	}
	client.Start(r.rootCtx)

	e := &Entry{
		ID:            id,
		Channel:       channelKey,
		SourceChannel: sourceChannel,
		SourcePrefix:  sourcePrefix,
		URL:           strings.TrimSpace(entry.URL),
		BaseURL:       strings.TrimSpace(entry.BaseURL),
		Token:         strings.TrimSpace(entry.JwtToken),
		client:        client,
	}
	r.entries[id] = e
	if e.Channel != "" {
		r.byChannel[e.Channel] = id
	}
	if e.SourceChannel != "" {
		r.bySourceChannel[e.SourceChannel] = id
	}
	r.rebuildSourcePrefixIndexLocked()
	return nil
}

// LookupByChatID 按 chatId 前缀（例如 "wecom#..." → channel "wecom"）查 entry。
func (r *Registry) LookupByChatID(chatID string) (*Entry, bool) {
	channelID := channel.ChannelForChatID(chatID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if channelID != "" {
		if id, ok := r.byChannel[channelID]; ok {
			return r.entries[id], true
		}
		if id, ok := r.bySourcePrefix[channelID]; ok {
			return r.entries[id], true
		}
	}
	return nil, false
}

func (r *Registry) LookupBySourceChannel(sourceChannel string) (*Entry, bool) {
	sourceChannel = strings.TrimSpace(sourceChannel)
	if sourceChannel == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.bySourceChannel[sourceChannel]; ok {
		return r.entries[id], true
	}
	return nil, false
}

// Resolver 是按 chatId 查 gateway 的只读视图。artifactpusher / ws_routes 通过它解耦 Registry 内部。
type Resolver interface {
	Resolve(chatID string) (baseURL string, token string, ok bool)
}

// Resolve 按 chatId 前缀查对应 gateway 的 BaseURL 和 Token（路由 artifact 外推 / 文件下载）。
// 查不到时 ok=false，调用方按"无对应 gateway"处理（pusher 跳过，download 返回错误）。
func (r *Registry) Resolve(chatID string) (string, string, bool) {
	entry, ok := r.LookupByChatID(chatID)
	if !ok {
		return "", "", false
	}
	return entry.BaseURL, entry.Token, true
}

func (r *Registry) ResolveSourceChannel(sourceChannel string) (string, string, bool) {
	entry, ok := r.LookupBySourceChannel(sourceChannel)
	if !ok {
		return "", "", false
	}
	return entry.BaseURL, entry.Token, true
}

func (r *Registry) rebuildSourcePrefixIndexLocked() {
	counts := map[string]int{}
	owner := map[string]string{}
	for id, entry := range r.entries {
		prefix := strings.TrimSpace(entry.SourcePrefix)
		if prefix == "" {
			prefix = sourcePrefixFromChannel(entry.SourceChannel)
		}
		if prefix == "" {
			continue
		}
		counts[prefix]++
		owner[prefix] = id
	}
	r.bySourcePrefix = map[string]string{}
	for prefix, count := range counts {
		if count == 1 {
			r.bySourcePrefix[prefix] = owner[prefix]
		}
	}
}

func sourcePrefixFromChannel(sourceChannel string) string {
	sourceChannel = strings.TrimSpace(sourceChannel)
	if sourceChannel == "" {
		return ""
	}
	if idx := strings.Index(sourceChannel, ":"); idx > 0 {
		return sourceChannel[:idx]
	}
	return sourceChannel
}

func deriveSourceChannelFromURL(raw string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("channel"))
}

// Connected 返回指定 channel 当前对应的 gateway 反向 WS 是否在线。
func (r *Registry) Connected(channelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byChannel[strings.TrimSpace(channelID)]
	if !ok {
		return false
	}
	entry, ok := r.entries[id]
	if !ok || entry == nil || entry.client == nil {
		return false
	}
	return entry.client.Connected()
}

// StopAll 停所有 connector；通常只在 App.Close 调用。
func (r *Registry) StopAll() {
	r.mu.Lock()
	entries := r.entries
	r.entries = map[string]*Entry{}
	r.byChannel = map[string]string{}
	r.bySourceChannel = map[string]string{}
	r.bySourcePrefix = map[string]string{}
	r.mu.Unlock()
	for _, e := range entries {
		_ = e.client.Stop()
	}
}

var (
	ErrDuplicateID      = fmt.Errorf("gateway: duplicate id")
	ErrDuplicateChannel = fmt.Errorf("gateway: duplicate channel")
	ErrInvalidConfig    = fmt.Errorf("gateway: invalid config (id/url required)")
)
