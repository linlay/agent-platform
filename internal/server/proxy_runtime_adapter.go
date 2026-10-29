package server

import (
	"encoding/json"

	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/ws"
)

// The remaining Server boundary provides existing channel sockets and public
// unread notifications, not Proxy execution or completion decisions.
func (s *Server) proxyExecutor() *runexec.ProxyExecutor {
	var channels proxy.ChannelConnections
	if provider, ok := s.deps.Notifications.(ChannelConnectionProvider); ok {
		channels = proxyChannelConnections{provider}
	}
	return &runexec.ProxyExecutor{
		BackgroundContext: s.backgroundCtx, Chats: s.deps.Chats, Runs: s.deps.Runs,
		Models: s.deps.Models, Billing: s.deps.Config.Billing, Routes: s.proxyRuntime,
		Driver:        &proxy.Driver{Chats: s.deps.Chats, Tickets: s.ticketService, Channels: channels},
		Notifications: s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			count, err := s.agentUnreadCount(summary.AgentKey)
			if err == nil {
				s.broadcastChatReadState("chat.unread", summary, count)
			}
		},
	}
}

type proxyChannelConnections struct{ provider ChannelConnectionProvider }

func (p proxyChannelConnections) Connection(id string) (proxy.ChannelConnection, bool) {
	conn, ok := p.provider.GatewayConnection(id)
	if !ok || conn == nil {
		return nil, false
	}
	return proxyChannelConnection{conn}, true
}

type proxyChannelConnection struct{ *ws.Conn }

func (c proxyChannelConnection) OpenRequest(payload map[string]any) (<-chan []byte, func(), error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	var frame ws.RequestFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, nil, err
	}
	return c.OpenOutboundRequest(frame)
}
