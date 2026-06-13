package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
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

func TestHandleAgentsIgnoresChannelAndChannelsRouteIsRemoved(t *testing.T) {
	server, chats := newServerForChannelTests(t)
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
	if len(agentsResp.Data) != 4 {
		t.Fatalf("expected 4 unfiltered agents, got %#v", agentsResp.Data)
	}
	if got := []string{agentsResp.Data[0].Key, agentsResp.Data[1].Key, agentsResp.Data[2].Key, agentsResp.Data[3].Key}; !reflect.DeepEqual(got, []string{"assistant", "code-helper", "customer-service", "team-agent"}) {
		t.Fatalf("unexpected agent keys: %#v", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/agents?tag=does-not-filter", nil)
	server.handleAgents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleAgents with ignored tag code = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	if len(agentsResp.Data) != 4 {
		t.Fatalf("expected tag to be ignored, got %#v", agentsResp.Data)
	}

	if _, _, err := chats.EnsureChat("chat-a-old", "assistant", "", "old"); err != nil {
		t.Fatalf("ensure old chat: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-a-old", RunID: "loyw3v20", UpdatedAtMillis: 1000}); err != nil {
		t.Fatalf("complete old chat: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-a-new", "assistant", "", "new"); err != nil {
		t.Fatalf("ensure new chat: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-a-new", RunID: "loyw3v28", UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("complete new chat: %v", err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/agents?includeChats=1", nil)
	server.handleAgents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleAgents includeChats code = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	if len(agentsResp.Data[0].Chats) != 1 || agentsResp.Data[0].Chats[0].ChatID != "chat-a-new" {
		t.Fatalf("expected most recent assistant chat, got %#v", agentsResp.Data[0].Chats)
	}

	for _, path := range []string{"/api/agents?includeChats=abc", "/api/agents?includeChats=-1", "/api/agents?includeChats=51"} {
		rec = httptest.NewRecorder()
		server.handleAgents(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected bad request for %s, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	fixture := newTestFixture(t)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("channels route should be removed, got %d body=%s", rec.Code, rec.Body.String())
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
		ticketService: NewResourceTicketService(config.ResourceTicketConfig{}),
	}
	return server, chats
}
