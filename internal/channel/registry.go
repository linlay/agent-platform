package channel

import (
	"sort"
	"strings"

	"agent-platform/internal/config"
)

type Definition struct {
	ID        string
	Name      string
	Mode      config.ChannelMode
	Transport string
	Protocol  string
	Endpoint  config.ChannelEndpointConfig
	Auth      config.ChannelAuthConfig
	Heartbeat config.ChannelHeartbeatConfig
	Reconnect config.ChannelReconnectConfig
}

type Registry struct {
	byID map[string]*Definition
	all  []*Definition
}

func NewRegistry(configs []config.ChannelConfig) *Registry {
	r := &Registry{
		byID: map[string]*Definition{},
		all:  make([]*Definition, 0, len(configs)),
	}
	for _, cfg := range configs {
		def := &Definition{
			ID:        strings.TrimSpace(cfg.ID),
			Name:      strings.TrimSpace(cfg.Name),
			Mode:      cfg.Mode,
			Transport: strings.TrimSpace(cfg.Transport),
			Protocol:  strings.TrimSpace(cfg.Protocol),
			Endpoint:  cfg.Endpoint,
			Auth:      cfg.Auth,
			Heartbeat: cfg.Heartbeat,
			Reconnect: cfg.Reconnect,
		}
		r.byID[def.ID] = def
		r.all = append(r.all, def)
	}
	sort.Slice(r.all, func(i, j int) bool {
		return r.all[i].ID < r.all[j].ID
	})
	return r
}

func (r *Registry) Lookup(channelID string) (*Definition, bool) {
	if r == nil {
		return nil, false
	}
	def, ok := r.byID[strings.TrimSpace(channelID)]
	return def, ok
}

func (r *Registry) All() []*Definition {
	if r == nil {
		return nil
	}
	out := make([]*Definition, 0, len(r.all))
	out = append(out, r.all...)
	return out
}

func ChannelForChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if idx := strings.Index(chatID, "#"); idx > 0 {
		return chatID[:idx]
	}
	return ""
}
