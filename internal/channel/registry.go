package channel

import (
	"sort"
	"strings"

	"agent-platform-runner-go/internal/config"
)

type Definition struct {
	ID           string
	Name         string
	Type         config.ChannelType
	DefaultAgent string
	AllAgents    bool
	agents       map[string]struct{}
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
			ID:           strings.TrimSpace(cfg.ID),
			Name:         strings.TrimSpace(cfg.Name),
			Type:         cfg.Type,
			DefaultAgent: strings.TrimSpace(cfg.DefaultAgent),
			AllAgents:    cfg.AllAgents,
			agents:       map[string]struct{}{},
		}
		for _, agentKey := range cfg.Agents {
			agentKey = strings.TrimSpace(agentKey)
			if agentKey == "" {
				continue
			}
			def.agents[agentKey] = struct{}{}
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

func (r *Registry) IsAgentAllowed(channelID, agentKey string) bool {
	def, ok := r.Lookup(channelID)
	if !ok {
		return true
	}
	if def.AllAgents {
		return true
	}
	_, allowed := def.agents[strings.TrimSpace(agentKey)]
	return allowed
}

func (r *Registry) DefaultAgent(channelID string) string {
	def, ok := r.Lookup(channelID)
	if !ok {
		return ""
	}
	return def.DefaultAgent
}

func (r *Registry) AllowedAgentKeys(channelID string) []string {
	def, ok := r.Lookup(channelID)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(def.agents))
	for agentKey := range def.agents {
		keys = append(keys, agentKey)
	}
	sort.Strings(keys)
	return keys
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
