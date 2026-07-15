package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	toolcatalog "agent-platform/internal/tools"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type cardTestCatalog struct {
	agents []api.AgentSummary
	defs   map[string]catalog.AgentDefinition
	skills map[string]catalog.SkillDefinition
}

func (c cardTestCatalog) Agents(string) []api.AgentSummary {
	return append([]api.AgentSummary(nil), c.agents...)
}

func (c cardTestCatalog) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := c.defs[strings.TrimSpace(key)]
	return def, ok
}

func (c cardTestCatalog) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	def, ok := c.skills[strings.TrimSpace(key)]
	return def, ok
}

type cardTestTools map[string]api.ToolDetailResponse

func (t cardTestTools) Tool(name string) (api.ToolDetailResponse, bool) {
	def, ok := t[strings.TrimSpace(name)]
	return def, ok
}

func TestAgentCardBuilderUsesExplicitExportsSkillsKBaseAndPublicTools(t *testing.T) {
	source := cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}, {Key: "hidden"}},
		defs: map[string]catalog.AgentDefinition{
			"support": {
				Key:         "support",
				Name:        "售后数字分身",
				Description: "处理售后问题",
				Mode:        catalog.AgentModeKBase,
				Skills:      []string{"support-kb"},
				Tools:       []string{"z_tool", "private_tool", "a_tool"},
				KBaseConfig: kbase.AgentConfig{Tags: []string{"退款", "售后", "售后"}},
				ChannelConfig: catalog.AgentChannelConfig{Exports: []catalog.AgentChannelExport{{
					ChannelID:        "peer-a",
					ExternalAgentKey: "support-agent",
					Allow:            catalog.AgentChannelAllow{Query: true},
				}}},
			},
			"hidden": {
				Key:  "hidden",
				Name: "Hidden",
				Mode: "REACT",
				ChannelConfig: catalog.AgentChannelConfig{Exports: []catalog.AgentChannelExport{{
					ChannelID: "peer-a",
					Allow:     catalog.AgentChannelAllow{Query: false},
				}}},
			},
		},
		skills: map[string]catalog.SkillDefinition{
			"support-kb": {
				Key:         "support-kb",
				Name:        "售后知识库问答",
				Description: "查询售后资料",
				Metadata:    map[string]any{"tags": []any{"工单", "退款", "工单"}},
			},
		},
	}
	tools := cardTestTools{
		"a_tool": {
			Name:        "a_tool",
			Label:       "A Tool",
			Description: "Runs A",
			Meta:        map[string]any{"tags": []any{"read", "safe"}},
		},
		"z_tool": {
			Name:        "z_tool",
			Description: "Runs Z",
			Meta:        map[string]any{},
		},
		"private_tool": {
			Name: "private_tool",
			Meta: map[string]any{"internalOnly": true},
		},
	}
	reporter := newAgentCardReporter(context.Background(), source, tools, agentCardReporterOptions{})

	cards, failures := reporter.buildCards("peer-a")
	if len(failures) != 0 || len(cards) != 1 {
		t.Fatalf("unexpected card build result cards=%#v failures=%#v", cards, failures)
	}
	card := cards[0]
	if card.agentKey != "support-agent" || card.payload.AgentKey != "support-agent" {
		t.Fatalf("expected external agent key, got %#v", card)
	}
	if got := card.payload.AgentCard.Skills; len(got) != 2 || got[0].ID != "kb.query.support-agent" || got[1].ID != "support-kb" {
		t.Fatalf("unexpected sorted skills %#v", got)
	}
	if got := card.payload.AgentCard.Skills[0].Tags; strings.Join(got, ",") != "售后,退款" {
		t.Fatalf("unexpected KBASE tags %#v", got)
	}
	if got := card.payload.AgentCard.Tools; len(got) != 2 || got[0].ID != "a_tool" || got[0].Name != "A Tool" || got[1].ID != "z_tool" {
		t.Fatalf("unexpected public tools %#v", got)
	}
}

func TestAgentCardBuilderRejectsMissingAndSensitiveMetadata(t *testing.T) {
	source := cardTestCatalog{
		agents: []api.AgentSummary{{Key: "missing"}, {Key: "secret"}, {Key: "bad-tags"}},
		defs: map[string]catalog.AgentDefinition{
			"missing":  exportedCardTestAgent("missing", "Missing", "safe", []string{"not-found"}, nil),
			"secret":   exportedCardTestAgent("secret", "Secret", "token=top-secret-value", nil, nil),
			"bad-tags": exportedCardTestAgent("bad-tags", "Bad tags", "safe", []string{"bad-tags"}, nil),
		},
		skills: map[string]catalog.SkillDefinition{
			"bad-tags": {Key: "bad-tags", Name: "Bad tags", Metadata: map[string]any{"tags": []any{"safe", 42}}},
		},
	}
	reporter := newAgentCardReporter(context.Background(), source, cardTestTools{}, agentCardReporterOptions{})

	cards, failures := reporter.buildCards("peer-a")
	if len(cards) != 0 || len(failures) != 3 {
		t.Fatalf("expected both cards to fail closed, cards=%#v failures=%#v", cards, failures)
	}
}

func TestAgentCardReporterInitializesOfflineAndInvalidStatuses(t *testing.T) {
	source := cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}, {Key: "secret"}},
		defs: map[string]catalog.AgentDefinition{
			"support": exportedCardTestAgent("support", "Support", "safe", nil, nil),
			"secret":  exportedCardTestAgent("secret", "Secret", "/Users/example/private.txt", nil, nil),
		},
		skills: map[string]catalog.SkillDefinition{},
	}
	reporter := newAgentCardReporter(context.Background(), source, cardTestTools{}, agentCardReporterOptions{})
	reporter.GatewayRegistered("peer-a", "peer-a")

	if status, ok := reporter.AgentCardStatus("peer-a", "support"); !ok || status.Status != agentCardStatusOffline {
		t.Fatalf("unexpected offline status %#v ok=%v", status, ok)
	}
	if status, ok := reporter.AgentCardStatus("peer-a", "secret"); !ok || status.Status != agentCardStatusError || status.Reason == "" {
		t.Fatalf("unexpected invalid status %#v ok=%v", status, ok)
	}
}

func TestEmbeddedPublicToolDescriptionsAreAgentCardSafe(t *testing.T) {
	defs, err := toolcatalog.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tools: %v", err)
	}
	for _, def := range defs {
		if toolIsPrivate(def.Meta) {
			continue
		}
		if err := validateCardText("tool.description", def.Description, defaultCardDescriptionRunes, false); err != nil {
			t.Errorf("embedded tool %q cannot be reported: %v", def.Name, err)
		}
	}
}

func TestDecodeAgentCardResponse(t *testing.T) {
	accepted := true
	data, _ := json.Marshal(api.GatewayAgentCardAck{AgentKey: "support", Accepted: &accepted})
	raw, _ := json.Marshal(ws.ResponseFrame{Frame: ws.FrameResponse, Type: agentCardUpdateType, ID: "card_1", Code: 0, Msg: "success", Data: json.RawMessage(data)})
	if outcome := decodeAgentCardResponse(raw, "card_1", "support"); !outcome.accepted {
		t.Fatalf("expected accepted outcome, got %#v", outcome)
	}

	rejected := false
	data, _ = json.Marshal(api.GatewayAgentCardAck{AgentKey: "support", Accepted: &rejected, Reason: "invalid card"})
	raw, _ = json.Marshal(ws.ResponseFrame{Frame: ws.FrameResponse, Type: agentCardUpdateType, ID: "card_2", Code: 0, Data: json.RawMessage(data)})
	if outcome := decodeAgentCardResponse(raw, "card_2", "support"); !outcome.rejected || outcome.retryable {
		t.Fatalf("expected terminal rejection, got %#v", outcome)
	}

	raw, _ = json.Marshal(ws.ErrorFrame{Frame: ws.FrameError, Type: "unavailable", ID: "card_3", Code: 503, Msg: "retry"})
	if outcome := decodeAgentCardResponse(raw, "card_3", "support"); !outcome.retryable {
		t.Fatalf("expected retryable 5xx error, got %#v", outcome)
	}
}

func TestSanitizeCardReasonRedactsCredentialsAndPaths(t *testing.T) {
	reason := sanitizeCardReason("rejected token=top-secret-value at /Users/example/private.txt")
	if strings.Contains(reason, "top-secret-value") || strings.Contains(reason, "/Users/example") {
		t.Fatalf("reason was not redacted: %q", reason)
	}
}

func TestAgentCardReporterReportsOnConnectRetriesAndDebouncesRefresh(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	requests := make(chan ws.RequestFrame, 8)
	connections := make(chan *gws.Conn, 4)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		for {
			var frame ws.RequestFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Type != agentCardUpdateType {
				continue
			}
			requests <- frame
			mu.Lock()
			requestCount++
			attempt := requestCount
			mu.Unlock()
			if attempt == 1 {
				_ = conn.WriteJSON(ws.ErrorFrame{Frame: ws.FrameError, Type: "unavailable", ID: frame.ID, Code: 503, Msg: "retry"})
				continue
			}
			var payload api.GatewayAgentCardUpdatePayload
			_ = json.Unmarshal(frame.Payload, &payload)
			accepted := true
			_ = conn.WriteJSON(ws.ResponseFrame{
				Frame: ws.FrameResponse,
				Type:  agentCardUpdateType,
				ID:    frame.ID,
				Code:  0,
				Msg:   "success",
				Data:  api.GatewayAgentCardAck{AgentKey: payload.AgentKey, Accepted: &accepted},
			})
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}},
		defs: map[string]catalog.AgentDefinition{
			"support": exportedCardTestAgent("support", "Support", "safe", nil, nil),
		},
		skills: map[string]catalog.SkillDefinition{},
	}
	reporter := newAgentCardReporter(ctx, source, cardTestTools{}, agentCardReporterOptions{
		Debounce:      20 * time.Millisecond,
		AckTimeout:    500 * time.Millisecond,
		RetryDelays:   []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		MaxConcurrent: 1,
	})
	hub := ws.NewHub()
	registry := New(ctx, config.WebSocketConfig{
		MaxMessageSizeBytes: 1 << 20,
		PingInterval:        1,
		WriteTimeout:        1,
		WriteQueueSize:      8,
		MaxObservesPerConn:  4,
	}, 50*time.Millisecond, hub, func(context.Context, *ws.Conn, ws.RequestFrame) {}, reporter)
	defer registry.StopAll()
	if err := registry.Register(config.GatewayEntry{
		ID:               "peer-a",
		Channel:          "peer-a",
		URL:              "ws" + strings.TrimPrefix(server.URL, "http"),
		HandshakeTimeout: 1,
		ReconnectMin:     1,
		ReconnectMax:     1,
	}); err != nil {
		t.Fatalf("register gateway: %v", err)
	}

	firstConnection := waitGatewayConnection(t, connections)
	firstRequest := waitCardRequest(t, requests)
	if firstRequest.Frame != ws.FrameRequest || firstRequest.Type != agentCardUpdateType || !strings.HasPrefix(firstRequest.ID, "card_") {
		t.Fatalf("unexpected agent card request frame %#v", firstRequest)
	}
	if strings.Contains(string(firstRequest.Payload), "requestId") || !strings.Contains(string(firstRequest.Payload), `"skills":[]`) || !strings.Contains(string(firstRequest.Payload), `"tools":[]`) {
		t.Fatalf("unexpected agent card payload %s", firstRequest.Payload)
	}
	waitCardRequest(t, requests)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusAccepted)
	reporter.ScheduleRefresh()
	reporter.ScheduleRefresh()
	waitCardRequest(t, requests)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusAccepted)
	select {
	case extra := <-requests:
		t.Fatalf("expected debounced refresh to send once, got extra request %#v", extra)
	case <-time.After(80 * time.Millisecond):
	}
	if err := firstConnection.Close(); err != nil {
		t.Fatalf("close first gateway connection: %v", err)
	}
	waitGatewayConnection(t, connections)
	waitCardRequest(t, requests)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusAccepted)
}

func TestAgentCardReporterStopsAfterThreeTimeouts(t *testing.T) {
	requests := make(chan ws.RequestFrame, 4)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var frame ws.RequestFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Type == agentCardUpdateType {
				requests <- frame
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}},
		defs: map[string]catalog.AgentDefinition{
			"support": exportedCardTestAgent("support", "Support", "safe", nil, nil),
		},
		skills: map[string]catalog.SkillDefinition{},
	}
	reporter := newAgentCardReporter(ctx, source, cardTestTools{}, agentCardReporterOptions{
		AckTimeout:    15 * time.Millisecond,
		RetryDelays:   []time.Duration{time.Millisecond, time.Millisecond},
		MaxConcurrent: 1,
	})
	hub := ws.NewHub()
	registry := New(ctx, config.WebSocketConfig{
		MaxMessageSizeBytes: 1 << 20,
		PingInterval:        1,
		WriteTimeout:        1,
		WriteQueueSize:      8,
		MaxObservesPerConn:  4,
	}, 50*time.Millisecond, hub, func(context.Context, *ws.Conn, ws.RequestFrame) {}, reporter)
	defer registry.StopAll()
	if err := registry.Register(config.GatewayEntry{
		ID:               "peer-a",
		Channel:          "peer-a",
		URL:              "ws" + strings.TrimPrefix(server.URL, "http"),
		HandshakeTimeout: 1,
		ReconnectMin:     1,
		ReconnectMax:     1,
	}); err != nil {
		t.Fatalf("register gateway: %v", err)
	}

	waitCardRequest(t, requests)
	waitCardRequest(t, requests)
	waitCardRequest(t, requests)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusError)
	status, _ := reporter.AgentCardStatus("peer-a", "support")
	if status.Attempt != 3 {
		t.Fatalf("attempt = %d, want 3", status.Attempt)
	}
	select {
	case extra := <-requests:
		t.Fatalf("unexpected fourth timeout retry %#v", extra)
	case <-time.After(60 * time.Millisecond):
	}
}

func exportedCardTestAgent(key, name, description string, skills, tools []string) catalog.AgentDefinition {
	return catalog.AgentDefinition{
		Key:         key,
		Name:        name,
		Description: description,
		Mode:        "REACT",
		Skills:      append([]string(nil), skills...),
		Tools:       append([]string(nil), tools...),
		ChannelConfig: catalog.AgentChannelConfig{Exports: []catalog.AgentChannelExport{{
			ChannelID: "peer-a",
			Allow:     catalog.AgentChannelAllow{Query: true},
		}}},
	}
}

func waitCardRequest(t *testing.T, requests <-chan ws.RequestFrame) ws.RequestFrame {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent card request")
		return ws.RequestFrame{}
	}
}

func waitGatewayConnection(t *testing.T, connections <-chan *gws.Conn) *gws.Conn {
	t.Helper()
	select {
	case conn := <-connections:
		return conn
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for gateway connection")
		return nil
	}
}

func waitForCardStatus(t *testing.T, reporter *AgentCardReporter, channelID, agentKey, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, ok := reporter.AgentCardStatus(channelID, agentKey); ok && status.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := reporter.AgentCardStatus(channelID, agentKey)
	t.Fatalf("timed out waiting for card status %q, got %#v", want, status)
}
