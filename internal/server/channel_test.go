package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	channelpkg "agent-platform-runner-go/internal/channel"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
)

type channelTestCatalogRegistry struct {
	defaultAgent string
	agents       []api.AgentSummary
	defs         map[string]catalog.AgentDefinition
	teams        map[string]catalog.TeamDefinition
}

func (r channelTestCatalogRegistry) Agents(string) []api.AgentSummary {
	return append([]api.AgentSummary(nil), r.agents...)
}

func (r channelTestCatalogRegistry) Teams() []api.TeamSummary { return nil }

func (r channelTestCatalogRegistry) Skills(string) []api.SkillSummary { return nil }

func (r channelTestCatalogRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}

func (r channelTestCatalogRegistry) Tools(string, string) []api.ToolSummary { return nil }

func (r channelTestCatalogRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}

func (r channelTestCatalogRegistry) DefaultAgentKey() string { return r.defaultAgent }

func (r channelTestCatalogRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := r.defs[key]
	return def, ok
}

func (r channelTestCatalogRegistry) TeamDefinition(teamID string) (catalog.TeamDefinition, bool) {
	def, ok := r.teams[teamID]
	return def, ok
}

func (r channelTestCatalogRegistry) Reload(_ context.Context, _ string) error {
	return nil
}

type channelStatusStub map[string]bool

func (s channelStatusStub) Connected(channelID string) bool { return s[channelID] }

func TestPrepareQueryUsesChannelDefaultAgentOnlyFromGlobalDefault(t *testing.T) {
	server, chats := newServerForChannelTests(t)
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{
		{
			ID:           "wecom",
			Name:         "WeCom",
			Type:         config.ChannelTypeBridge,
			DefaultAgent: "customer-service",
			AllAgents:    true,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#single#u1","message":"hello"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}
	if prepared.req.AgentKey != "customer-service" {
		t.Fatalf("expected channel default agent, got %q", prepared.req.AgentKey)
	}

	if _, _, err := chats.EnsureChat("wecom#existing#u1", "assistant", "", "seed"); err != nil {
		t.Fatalf("seed existing chat: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#existing#u1","message":"hello again"}`))
	prepared, err = server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery existing chat: %v", err)
	}
	if prepared.req.AgentKey != "assistant" {
		t.Fatalf("expected existing chat agent to win, got %q", prepared.req.AgentKey)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#team#u1","teamId":"team-a","message":"team route"}`))
	prepared, err = server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery team default: %v", err)
	}
	if prepared.req.AgentKey != "team-agent" {
		t.Fatalf("expected team default agent to win, got %q", prepared.req.AgentKey)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#explicit#u1","agentKey":"assistant","message":"explicit"}`))
	prepared, err = server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery explicit agent: %v", err)
	}
	if prepared.req.AgentKey != "assistant" {
		t.Fatalf("expected explicit agent to win, got %q", prepared.req.AgentKey)
	}
}

func TestPrepareQueryRejectsDisallowedAgentOnChannel(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{
		{
			ID:        "feishu",
			Name:      "Feishu",
			Type:      config.ChannelTypeBridge,
			AllAgents: false,
			Agents:    []string{"assistant"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"feishu#p2p#u1","agentKey":"customer-service","message":"hello"}`))
	_, err := server.prepareQuery(req)
	statusErr, ok := err.(*statusError)
	if !ok {
		t.Fatalf("expected statusError, got %v", err)
	}
	if statusErr.status != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", statusErr.status)
	}
}

func TestHandleAgentsFiltersByChannelAndHandleChannelsShowsConnected(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Channels = channelpkg.NewRegistry([]config.ChannelConfig{
		{
			ID:           "wecom",
			Name:         "WeCom",
			Type:         config.ChannelTypeBridge,
			DefaultAgent: "assistant",
			AllAgents:    false,
			Agents:       []string{"assistant", "code-helper"},
		},
		{
			ID:        "mobile",
			Name:      "Mobile",
			Type:      config.ChannelTypeGateway,
			AllAgents: true,
		},
	})
	server.deps.ChannelStatus = channelStatusStub{
		"wecom":  true,
		"mobile": false,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents?channel=wecom", nil)
	server.handleAgents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleAgents code = %d body=%s", rec.Code, rec.Body.String())
	}
	var agentsResp api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	if len(agentsResp.Data) != 2 {
		t.Fatalf("expected 2 filtered agents, got %#v", agentsResp.Data)
	}
	if got := []string{agentsResp.Data[0].Key, agentsResp.Data[1].Key}; !reflect.DeepEqual(got, []string{"assistant", "code-helper"}) {
		t.Fatalf("unexpected filtered agent keys: %#v", got)
	}

	rec = httptest.NewRecorder()
	server.handleChannels(rec, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handleChannels code = %d body=%s", rec.Code, rec.Body.String())
	}
	var channelsResp api.ApiResponse[[]api.ChannelSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &channelsResp); err != nil {
		t.Fatalf("decode channels response: %v", err)
	}
	if len(channelsResp.Data) != 2 {
		t.Fatalf("expected 2 channels, got %#v", channelsResp.Data)
	}
	byID := map[string]api.ChannelSummary{}
	for _, item := range channelsResp.Data {
		byID[item.ID] = item
	}
	if !byID["wecom"].Connected || byID["mobile"].Connected {
		t.Fatalf("unexpected connected states: %#v", byID)
	}
	if !reflect.DeepEqual(byID["wecom"].Agents, []string{"assistant", "code-helper"}) {
		t.Fatalf("unexpected wecom agents: %#v", byID["wecom"].Agents)
	}
	if !reflect.DeepEqual(byID["mobile"].Agents, []string{"assistant", "code-helper", "customer-service", "team-agent"}) {
		t.Fatalf("unexpected mobile agents: %#v", byID["mobile"].Agents)
	}
}

func newServerForChannelTests(t *testing.T) (*Server, *chat.FileStore) {
	t.Helper()
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	registry := channelTestCatalogRegistry{
		defaultAgent: "global-default",
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
			{Key: "code-helper", Name: "Code Helper"},
			{Key: "customer-service", Name: "Customer Service"},
			{Key: "team-agent", Name: "Team Agent"},
		},
		defs: map[string]catalog.AgentDefinition{
			"global-default":   {Key: "global-default", Name: "Global Default", ModelKey: "mock-model"},
			"assistant":        {Key: "assistant", Name: "Assistant", ModelKey: "mock-model"},
			"code-helper":      {Key: "code-helper", Name: "Code Helper", ModelKey: "mock-model"},
			"customer-service": {Key: "customer-service", Name: "Customer Service", ModelKey: "mock-model"},
			"team-agent":       {Key: "team-agent", Name: "Team Agent", ModelKey: "mock-model"},
		},
		teams: map[string]catalog.TeamDefinition{
			"team-a": {TeamID: "team-a", DefaultAgentKey: "team-agent"},
		},
	}
	server := &Server{
		deps: Dependencies{
			Config: config.Config{
				ChatStorage: config.ChatStorageConfig{K: 20},
			},
			Chats:    chats,
			Registry: registry,
		},
		ticketService: NewResourceTicketService(config.ChatImageTokenConfig{}),
	}
	return server, chats
}
