package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	platformws "agent-platform/internal/ws"
)

type channelTestCatalogRegistry struct {
	defaultAgent string
	agents       []api.AgentSummary
	defs         map[string]catalog.AgentDefinition
	teams        map[string]catalog.TeamDefinition
}

type snapshotChannelTestCatalogRegistry struct {
	channelTestCatalogRegistry
	snapshots map[string]catalog.TeamSnapshot
}

func (r snapshotChannelTestCatalogRegistry) ResolveTeam(teamID string) (catalog.TeamSnapshot, bool) {
	snapshot, ok := r.snapshots[strings.TrimSpace(teamID)]
	return snapshot, ok
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
	prepared, err := prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest: %v", err)
	}
	if prepared.req.AgentKey != "customer-service" {
		t.Fatalf("expected channel default agent, got %q", prepared.req.AgentKey)
	}

	if _, _, err := chats.EnsureChat("wecom#existing#u1", "assistant", "", "seed"); err != nil {
		t.Fatalf("seed existing chat: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#existing#u1","message":"hello again"}`))
	prepared, err = prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest existing chat: %v", err)
	}
	if prepared.req.AgentKey != "assistant" {
		t.Fatalf("expected existing chat agent to win, got %q", prepared.req.AgentKey)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#team#u1","teamId":"team-a","message":"team route"}`))
	prepared, err = prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest team default: %v", err)
	}
	if prepared.req.AgentKey != "team-agent" {
		t.Fatalf("expected team default agent to win, got %q", prepared.req.AgentKey)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"wecom#explicit#u1","agentKey":"assistant","message":"explicit"}`))
	prepared, err = prepareQueryForTest(server, req)
	if err != nil {
		t.Fatalf("prepareQueryForTest explicit agent: %v", err)
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
	_, err := prepareQueryForTest(server, req)
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

func TestRewriteChannelRequestPayloadMapsExportedAgentAndChecksAllow(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
		},
		defs: map[string]catalog.AgentDefinition{
			"assistant": {
				Key:      "assistant",
				Name:     "Assistant",
				Mode:     catalog.AgentModeProxy,
				ModelKey: "mock-model",
				ChannelConfig: catalog.AgentChannelConfig{
					Exports: []catalog.AgentChannelExport{{
						ChannelID:        "public-entry",
						ExternalAgentKey: "assistant-ext",
						Allow:            catalog.AgentChannelAllow{Query: true},
					}},
				},
			},
		},
	}
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})

	rewritten, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/query", json.RawMessage(`{"externalAgentKey":"assistant-ext","message":"hello"}`))
	if statusErr != nil {
		t.Fatalf("rewrite query payload: %v", statusErr)
	}
	var body map[string]any
	if err := json.Unmarshal(rewritten, &body); err != nil {
		t.Fatalf("decode rewritten payload: %v", err)
	}
	if body["agentKey"] != "assistant" {
		t.Fatalf("expected local agent key assistant, got %#v", body)
	}
	if _, ok := body["externalAgentKey"]; ok {
		t.Fatalf("expected externalAgentKey to be removed, got %#v", body)
	}

	_, statusErr = server.rewriteChannelRequestPayload(ctx, "/api/submit", json.RawMessage(`{"agentKey":"assistant-ext","runId":"run-1"}`))
	if statusErr == nil || statusErr.status != http.StatusForbidden {
		t.Fatalf("expected submit to be forbidden, got %#v", statusErr)
	}
}

func TestRewriteChannelRequestPayloadRejectsMissingExport(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
		},
		defs: map[string]catalog.AgentDefinition{
			"assistant": {
				Key:      "assistant",
				Name:     "Assistant",
				Mode:     "REACT",
				ModelKey: "mock-model",
			},
		},
	}
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})

	_, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/query", json.RawMessage(`{"externalAgentKey":"assistant","message":"hello"}`))
	if statusErr == nil || statusErr.status != http.StatusForbidden || statusErr.message != "agent is not exported on channel" {
		t.Fatalf("expected missing export to be forbidden, got %#v", statusErr)
	}
}

func TestRewriteChannelFileTransferRequiresExportAllow(t *testing.T) {
	server, chats := newServerForChannelTests(t)
	if _, _, err := chats.EnsureChat("chat-export", "assistant", "", "seed"); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
		},
		defs: map[string]catalog.AgentDefinition{
			"assistant": {
				Key:  "assistant",
				Name: "Assistant",
				Mode: "REACT",
				ChannelConfig: catalog.AgentChannelConfig{
					Exports: []catalog.AgentChannelExport{{
						ChannelID:        "public-entry",
						ExternalAgentKey: "assistant-ext",
						Allow:            catalog.AgentChannelAllow{Query: true},
					}},
				},
			},
		},
	}
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})
	_, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/upload", json.RawMessage(`{"chatId":"chat-export"}`))
	if statusErr == nil || statusErr.status != http.StatusForbidden {
		t.Fatalf("expected upload without fileTransfer allow to be forbidden, got %#v", statusErr)
	}

	server.deps.Registry = channelTestCatalogRegistry{
		agents: []api.AgentSummary{
			{Key: "assistant", Name: "Assistant"},
		},
		defs: map[string]catalog.AgentDefinition{
			"assistant": {
				Key:  "assistant",
				Name: "Assistant",
				Mode: "REACT",
				ChannelConfig: catalog.AgentChannelConfig{
					Exports: []catalog.AgentChannelExport{{
						ChannelID:        "public-entry",
						ExternalAgentKey: "assistant-ext",
						Allow: catalog.AgentChannelAllow{
							Query:        true,
							FileTransfer: true,
						},
					}},
				},
			},
		},
	}
	_, statusErr = server.rewriteChannelRequestPayload(ctx, "/api/upload", json.RawMessage(`{"chatId":"chat-export"}`))
	if statusErr != nil {
		t.Fatalf("expected upload with fileTransfer allow to pass, got %#v", statusErr)
	}
}

func TestRewriteChannelRequestPayloadOmittedAliasMatchesLocalKey(t *testing.T) {
	server, _ := newServerForChannelTests(t)
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
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})

	// Match via externalAgentKey with local agent key
	rewritten, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/query", json.RawMessage(`{"externalAgentKey":"kbaseOrchestrator","message":"hello"}`))
	if statusErr != nil {
		t.Fatalf("rewrite query payload: %v", statusErr)
	}
	var body map[string]any
	if err := json.Unmarshal(rewritten, &body); err != nil {
		t.Fatalf("decode rewritten payload: %v", err)
	}
	if body["agentKey"] != "kbaseOrchestrator" {
		t.Fatalf("expected local agent key kbaseOrchestrator, got %#v", body)
	}
	if _, ok := body["externalAgentKey"]; ok {
		t.Fatalf("expected externalAgentKey to be removed, got %#v", body)
	}
}

func TestRewriteChannelRequestPayloadFallbackAgentKeyMatches(t *testing.T) {
	server, _ := newServerForChannelTests(t)
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
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})

	// Match via agentKey (fallback) without externalAgentKey
	rewritten, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/query", json.RawMessage(`{"agentKey":"kbaseOrchestrator","message":"hello"}`))
	if statusErr != nil {
		t.Fatalf("rewrite query payload via agentKey: %v", statusErr)
	}
	var body map[string]any
	if err := json.Unmarshal(rewritten, &body); err != nil {
		t.Fatalf("decode rewritten payload: %v", err)
	}
	if body["agentKey"] != "kbaseOrchestrator" {
		t.Fatalf("expected local agent key kbaseOrchestrator, got %#v", body)
	}
}

func TestRewriteChannelRequestPayloadNonMatchingExternalKeyForbidden(t *testing.T) {
	server, _ := newServerForChannelTests(t)
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
	ctx := platformws.WithGatewayContext(context.Background(), platformws.GatewayContext{Channel: "public-entry"})

	_, statusErr := server.rewriteChannelRequestPayload(ctx, "/api/query", json.RawMessage(`{"externalAgentKey":"nonexistent","message":"hello"}`))
	if statusErr == nil || statusErr.status != http.StatusForbidden {
		t.Fatalf("expected 403 for non-matching external key, got %#v", statusErr)
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
			"team-a": {TeamID: "team-a", AgentKeys: []string{"team-agent", "assistant"}, DefaultAgentKey: "team-agent"},
		},
	}
	server := &Server{
		deps: Dependencies{
			Chats:    chats,
			Registry: registry,
		},
		ticketService: NewResourceTicketService(config.ResourceTicketConfig{}),
	}
	return server, chats
}

func TestPrepareQueryTeamAdmissionIsStrictAndTeamIsFixed(t *testing.T) {
	server, chats := newServerForChannelTests(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "unknown team", body: `{"chatId":"new-unknown","teamId":"missing","message":"hello"}`, wantStatus: http.StatusBadRequest},
		{name: "agent outside team", body: `{"chatId":"new-outside","teamId":"team-a","agentKey":"code-helper","message":"hello"}`, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(tt.body))
			_, err := server.prepareQueryAdmission(req, true)
			var statusErr *statusError
			if !errors.As(err, &statusErr) || statusErr.status != tt.wantStatus {
				t.Fatalf("error = %#v, want status %d", err, tt.wantStatus)
			}
		})
	}

	if _, _, err := chats.EnsureChat("plain-chat", "assistant", "", "seed"); err != nil {
		t.Fatalf("seed plain chat: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"plain-chat","teamId":"team-a","message":"hello"}`))
	_, err := server.prepareQueryAdmission(req, true)
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusConflict {
		t.Fatalf("empty-team chat adoption error = %#v, want 409", err)
	}

	if _, _, err := chats.EnsureChat("team-chat", "team-agent", "team-a", "seed"); err != nil {
		t.Fatalf("seed team chat: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"team-chat","teamId":"team-b","message":"hello"}`))
	_, err = server.prepareQueryAdmission(req, true)
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusConflict {
		t.Fatalf("team replacement error = %#v, want 409", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"team-chat","agentKey":"assistant","message":"switch"}`))
	admission, err := server.prepareQueryAdmission(req, true)
	if err != nil {
		t.Fatalf("same-team member switch: %v", err)
	}
	if admission.req.TeamID != "team-a" || admission.req.AgentKey != "assistant" || admission.teamSnapshot == nil {
		t.Fatalf("unexpected switched admission: %#v", admission)
	}
}

func TestPrepareQueryTeamAdmissionReturnsUnavailableForInvalidDefaultAndDrift(t *testing.T) {
	server, chats := newServerForChannelTests(t)
	registry := server.deps.Registry.(channelTestCatalogRegistry)
	registry.teams["invalid-default"] = catalog.TeamDefinition{
		TeamID:          "invalid-default",
		AgentKeys:       []string{"missing-agent"},
		DefaultAgentKey: "missing-agent",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"invalid-default-chat","teamId":"invalid-default","message":"hello"}`))
	_, err := server.prepareQueryAdmission(req, true)
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusServiceUnavailable {
		t.Fatalf("invalid default error = %#v, want 503", err)
	}

	if _, _, err := chats.EnsureChat("drifted-chat", "removed-agent", "team-a", "seed"); err != nil {
		t.Fatalf("seed drifted chat: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"drifted-chat","message":"hello"}`))
	_, err = server.prepareQueryAdmission(req, true)
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusServiceUnavailable {
		t.Fatalf("drifted current agent error = %#v, want 503", err)
	}
}

func TestPrepareQueryTeamAdmissionUsesFrozenMemberDefinition(t *testing.T) {
	server, _ := newServerForChannelTests(t)
	registry := server.deps.Registry.(channelTestCatalogRegistry)
	team := registry.teams["team-a"]
	snapshot := catalog.NewTeamSnapshot(team, map[string]catalog.AgentDefinition{
		"team-agent": {Key: "team-agent", Name: "Frozen Team Agent", ModelKey: "mock-model", Mode: "REACT"},
		"assistant":  registry.defs["assistant"],
	})
	delete(registry.defs, "team-agent")
	server.deps.Registry = snapshotChannelTestCatalogRegistry{
		channelTestCatalogRegistry: registry,
		snapshots:                  map[string]catalog.TeamSnapshot{"team-a": snapshot},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"snapshot-chat","teamId":"team-a","message":"hello"}`))
	admission, err := server.prepareQueryAdmission(req, true)
	if err != nil {
		t.Fatalf("prepare admission from frozen snapshot: %v", err)
	}
	if admission.agentDef.Name != "Frozen Team Agent" || admission.agentDef.Mode != "REACT" {
		t.Fatalf("admission did not use frozen team member definition: %#v", admission.agentDef)
	}
}

func TestCompleteQueryPreparationRechecksFixedTeamAfterConcurrentChatCreation(t *testing.T) {
	server, chats := newServerForChannelTests(t)
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"raced-chat","teamId":"team-a","message":"hello"}`))
	admission, err := server.prepareQueryAdmission(req, true)
	if err != nil {
		t.Fatalf("prepare admission: %v", err)
	}
	if _, _, err := chats.EnsureChat("raced-chat", "assistant", "", "other creator"); err != nil {
		t.Fatalf("seed raced chat: %v", err)
	}
	_, err = server.completeQueryPreparation(req.Context(), admission, nil)
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusConflict {
		t.Fatalf("complete error = %#v, want 409", err)
	}
}
