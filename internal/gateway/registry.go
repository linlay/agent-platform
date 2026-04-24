// Package gateway 管理多条反向 WS 连接（wecom / feishu / ding / ...），
// 每条对应一个 channel 插件。Registry 是 connector 的索引：
//   - 按 ID 做增删查（Admin API 场景）
//   - 按 Channel 做路由（artifact 外推、文件下载按 chatId 前缀选择 gateway）
//
// legacy 单 gateway 部署是"一条 channel 空串的 entry"的特例，路由退化为总命中它。
//
// Registry 本身不决定 gateway 生命周期——StartAll 从 config 初始化首批，
// Admin API 之后运行时可 Register / Unregister。
package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"
	"agent-platform-runner-go/internal/ws/gatewayclient"
)

// Entry 是 Registry 里一条 gateway 的运行态快照，给 Admin API List 用。
type Entry struct {
	ID       string
	Channel  string
	URL      string
	BaseURL  string
	Token    string // 返回给调用方时一般不暴露，仅内部使用
	client   *gatewayclient.Client
}

// Registry 线程安全。
type Registry struct {
	mu        sync.RWMutex
	entries   map[string]*Entry // id → entry
	byChannel map[string]string // channel → id (单 channel 单 gateway；后续多同 channel 再扩)

	// 依赖：新建 connector 时需要这些
	wsCfg     config.WebSocketConfig
	heartbeat time.Duration
	hub       *ws.Hub
	dispatch  ws.RouteHandler

	rootCtx context.Context
}

// New 创建 Registry。rootCtx 用于给每个 connector.Start 传递；Registry 自己不起 goroutine。
func New(rootCtx context.Context, wsCfg config.WebSocketConfig, heartbeat time.Duration, hub *ws.Hub, dispatch ws.RouteHandler) *Registry {
	return &Registry{
		entries:   map[string]*Entry{},
		byChannel: map[string]string{},
		wsCfg:     wsCfg,
		heartbeat: heartbeat,
		hub:       hub,
		dispatch:  dispatch,
		rootCtx:   rootCtx,
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

	client := gatewayclient.New(
		gatewayclient.Config{
			URL:              strings.TrimSpace(entry.URL),
			Token:            strings.TrimSpace(entry.JwtToken),
			HandshakeTimeout: time.Duration(entry.HandshakeTimeoutMs) * time.Millisecond,
			ReconnectMin:     time.Duration(entry.ReconnectMinMs) * time.Millisecond,
			ReconnectMax:     time.Duration(entry.ReconnectMaxMs) * time.Millisecond,
		},
		r.wsCfg,
		r.heartbeat,
		r.hub,
		r.dispatch,
	)
	client.Start(r.rootCtx)

	e := &Entry{
		ID:      id,
		Channel: strings.TrimSpace(entry.Channel),
		URL:     strings.TrimSpace(entry.URL),
		BaseURL: strings.TrimSpace(entry.BaseURL),
		Token:   strings.TrimSpace(entry.JwtToken),
		client:  client,
	}
	r.entries[id] = e
	if e.Channel != "" {
		// 同 channel 重复注册允许但后者覆盖；真实场景里 admin 层面应拦
		r.byChannel[e.Channel] = id
	}
	return nil
}

// Unregister 停止 connector 并从 Registry 移除。不存在时返回 ErrNotFound。
func (r *Registry) Unregister(id string) error {
	r.mu.Lock()
	entry, ok := r.entries[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	delete(r.entries, id)
	if entry.Channel != "" && r.byChannel[entry.Channel] == id {
		delete(r.byChannel, entry.Channel)
	}
	r.mu.Unlock()

	return entry.client.Stop()
}

// LookupByID 查 entry；主要给 Admin API 和测试用。
func (r *Registry) LookupByID(id string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	return e, ok
}

// LookupByChatID 按 chatId 前缀（例如 "wecom#..." → channel "wecom"）查 entry。
// 路由策略：
//  1. 提取 chatId 第一个 '#' 前的 channel 前缀
//  2. Registry 里有匹配 channel 的 entry 就返回
//  3. 没匹配到时，若 Registry 只有一条 entry（典型 legacy 单 gateway），返回它作为兜底
//  4. 多条 entry 且无匹配则返回 nil，false
func (r *Registry) LookupByChatID(chatID string) (*Entry, bool) {
	channel := channelFromChatID(chatID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if channel != "" {
		if id, ok := r.byChannel[channel]; ok {
			return r.entries[id], true
		}
	}
	if len(r.entries) == 1 {
		for _, e := range r.entries {
			return e, true
		}
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

// All 返回当前所有 entries 的快照。调用方不应修改 slice 元素。
func (r *Registry) All() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// StopAll 停所有 connector；通常只在 App.Close 调用。
func (r *Registry) StopAll() {
	r.mu.Lock()
	entries := r.entries
	r.entries = map[string]*Entry{}
	r.byChannel = map[string]string{}
	r.mu.Unlock()
	for _, e := range entries {
		_ = e.client.Stop()
	}
}

func channelFromChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if idx := strings.Index(chatID, "#"); idx > 0 {
		return chatID[:idx]
	}
	return ""
}

var (
	ErrDuplicateID   = fmt.Errorf("gateway: duplicate id")
	ErrNotFound      = fmt.Errorf("gateway: id not found")
	ErrInvalidConfig = fmt.Errorf("gateway: invalid config (id/url required)")
)
