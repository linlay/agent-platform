package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
)

type channelConnectionSnapshotStub map[string][]ws.MonitorConnection

func (s channelConnectionSnapshotStub) Broadcast(string, map[string]any) {}

func (s channelConnectionSnapshotStub) GatewayConnections(channelID string) []ws.MonitorConnection {
	return append([]ws.MonitorConnection(nil), s[strings.TrimSpace(channelID)]...)
}

func TestAdminChannelsReturnsEmptyListWithoutChannelRegistry(t *testing.T) {
	server := &Server{}
	rec := httptest.NewRecorder()
	server.handleAdminChannels(rec, httptest.NewRequest(http.MethodGet, "/api/admin/channels", nil))

	response := decodeAdminChannelsResponse(t, rec)
	if response.Data.Total != 0 || len(response.Data.Items) != 0 {
		t.Fatalf("expected empty channel list, got %#v", response.Data)
	}
}

func TestAdminChannelsReportsClientStatusAndRedactsSecrets(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{{
		ID:        "peer-a",
		Name:      "Peer A",
		Type:      config.ChannelTypeGateway,
		Mode:      config.ChannelModeClient,
		Transport: config.ChannelTransportWebSocket,
		Protocol:  config.ChannelProtocolPlatformWS,
		Endpoint: config.ChannelEndpointConfig{
			URL:      "ws://peer.example/ws/channel",
			Token:    "endpoint-secret",
			TokenEnv: "PEER_TOKEN",
		},
		Auth:      config.ChannelAuthConfig{Type: "jwt"},
		Heartbeat: config.ChannelHeartbeatConfig{Interval: 10},
		Reconnect: config.ChannelReconnectConfig{
			HandshakeTimeout: 7,
			Min:              2,
			Max:              40,
		},
		Gateway: config.ChannelGatewayConfig{
			URL:      "ws://legacy.example/ws",
			JwtToken: "legacy-secret",
		},
	}})
	server.deps.ChannelStatus = channelStatusStub{"peer-a": true}

	rec := httptest.NewRecorder()
	server.handleAdminChannels(rec, httptest.NewRequest(http.MethodGet, "/api/admin/channels", nil))
	response := decodeAdminChannelsResponse(t, rec)
	if response.Data.Total != 1 {
		t.Fatalf("expected one channel, got %#v", response.Data)
	}
	item := response.Data.Items[0]
	if item.ID != "peer-a" || item.Status != "connected" || !item.Connection.Connected {
		t.Fatalf("unexpected channel status: %#v", item)
	}
	if item.Config.EndpointURL != "ws://peer.example/ws/channel" || item.Config.AuthType != "jwt" {
		t.Fatalf("unexpected config summary: %#v", item.Config)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"endpoint-secret", "PEER_TOKEN", "legacy-secret", "jwtToken", "tokenEnv"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestAdminChannelsReportsServerConnectionsAndAgentRelations(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Notifications = channelConnectionSnapshotStub{
		"public-entry": {{
			SessionID:   "ws_2",
			Active:      true,
			ConnectedAt: 1_700_000_000_000,
			LastSeenAt:  1_700_000_001_000,
		}},
	}
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{{
		ID:        "public-entry",
		Name:      "Public Entry",
		Type:      config.ChannelTypeGateway,
		Mode:      config.ChannelModeServer,
		Transport: config.ChannelTransportWebSocket,
		Protocol:  config.ChannelProtocolPlatformWS,
		AllAgents: true,
		Endpoint:  config.ChannelEndpointConfig{Path: "/ws/channel"},
	}})
	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "imported-agent", Name: "Imported Agent"},
			{Key: "local-agent", Name: "Local Agent"},
			{Key: "plain-agent", Name: "Plain Agent"},
		},
		defs: map[string]catalog.AgentDefinition{
			"imported-agent": {
				Key:  "imported-agent",
				Name: "Imported Agent",
				Mode: "CHANNEL",
				ChannelConfig: catalog.AgentChannelConfig{
					ChannelID:      "public-entry",
					RemoteAgentKey: "remote-coder",
				},
			},
			"local-agent": {
				Key:  "local-agent",
				Name: "Local Agent",
				Mode: "REACT",
				ChannelConfig: catalog.AgentChannelConfig{
					Exports: []catalog.AgentChannelExport{{
						ChannelID:        "public-entry",
						ExternalAgentKey: "local-ext",
						Allow: catalog.AgentChannelAllow{
							Query:        true,
							FileTransfer: true,
						},
					}},
				},
			},
			"plain-agent": {Key: "plain-agent", Name: "Plain Agent", Mode: "REACT"},
		},
	}

	rec := httptest.NewRecorder()
	server.handleMonitorChannels(rec, httptest.NewRequest(http.MethodGet, "/api/monitor/channels", nil))
	response := decodeAdminChannelsResponse(t, rec)
	if response.Data.Total != 1 {
		t.Fatalf("expected one channel, got %#v", response.Data)
	}
	item := response.Data.Items[0]
	if item.Status != "connected" || item.Connection.ActiveCount != 1 || item.Connection.LatestSessionID != "ws_2" {
		t.Fatalf("unexpected server connection summary: %#v", item.Connection)
	}
	if !item.Agents.AllowedAllAgents || item.Agents.AllowedCount != 3 {
		t.Fatalf("unexpected allowed agents: %#v", item.Agents)
	}
	if item.Agents.ImportCount != 1 || item.Agents.Imports[0].RemoteAgentKey != "remote-coder" {
		t.Fatalf("unexpected imports: %#v", item.Agents.Imports)
	}
	if item.Agents.ExportCount != 1 || item.Agents.Exports[0].ExternalAgentKey != "local-ext" ||
		!item.Agents.Exports[0].Allow.Query || !item.Agents.Exports[0].Allow.FileTransfer {
		t.Fatalf("unexpected exports: %#v", item.Agents.Exports)
	}
}

func TestAdminChannelsReturnsEffectiveExternalAgentKeyForOmittedAlias(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Notifications = channelConnectionSnapshotStub{}
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{{
		ID:        "public-entry",
		Name:      "Public Entry",
		Type:      config.ChannelTypeGateway,
		Mode:      config.ChannelModeServer,
		Transport: config.ChannelTransportWebSocket,
		Protocol:  config.ChannelProtocolPlatformWS,
		AllAgents: true,
	}})
	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "kbaseOrchestrator", Name: "KB Orchestrator"},
		},
		defs: map[string]catalog.AgentDefinition{
			"kbaseOrchestrator": {
				Key:      "kbaseOrchestrator",
				Name:     "KB Orchestrator",
				Mode:     "KBASE",
				ModelKey: "mock-model",
				ChannelConfig: catalog.AgentChannelConfig{
					Exports: []catalog.AgentChannelExport{{
						ChannelID: "public-entry",
						Allow:     catalog.AgentChannelAllow{Query: true},
					}},
				},
			},
		},
	}

	rec := httptest.NewRecorder()
	server.handleMonitorChannels(rec, httptest.NewRequest(http.MethodGet, "/api/monitor/channels", nil))
	response := decodeAdminChannelsResponse(t, rec)
	if response.Data.Total != 1 {
		t.Fatalf("expected one channel, got %#v", response.Data)
	}
	item := response.Data.Items[0]
	if item.Agents.ExportCount != 1 {
		t.Fatalf("expected 1 export, got %d", item.Agents.ExportCount)
	}
	exp := item.Agents.Exports[0]
	if exp.ExternalAgentKey != "kbaseOrchestrator" {
		t.Fatalf("expected effective key kbaseOrchestrator, got %q", exp.ExternalAgentKey)
	}
	if exp.AgentKey != "kbaseOrchestrator" {
		t.Fatalf("expected local agent key kbaseOrchestrator, got %q", exp.AgentKey)
	}
}

func decodeAdminChannelsResponse(t *testing.T, rec *httptest.ResponseRecorder) api.ApiResponse[api.AdminChannelListResponse] {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AdminChannelListResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 {
		t.Fatalf("unexpected envelope: %#v", response)
	}
	return response
}
