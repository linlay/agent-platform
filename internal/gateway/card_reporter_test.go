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

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type cardTestCatalog struct {
	mu     sync.RWMutex
	agents []api.AgentSummary
	defs   map[string]catalog.AgentDefinition
}

func (c *cardTestCatalog) Agents(string) []api.AgentSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]api.AgentSummary(nil), c.agents...)
}

func (c *cardTestCatalog) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	def, ok := c.defs[strings.TrimSpace(key)]
	return def, ok
}

func TestBuildRegistrationsExportsOnlyAndMapsCapabilities(t *testing.T) {
	source := &cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}, {Key: "remote"}},
		defs: map[string]catalog.AgentDefinition{
			"support": {
				Key: "support", Name: "售后助手", Role: "售后支持", Description: "处理售后问题", Mode: "REACT",
				ChannelConfig: catalog.AgentChannelConfig{Exports: []catalog.AgentChannelExport{{
					ChannelID: "peer-a", ExternalAgentKey: "support-agent",
					Allow: catalog.AgentChannelAllow{Query: true, Submit: true, Steer: true, Interrupt: true, FileTransfer: true},
				}}},
			},
			"remote": {Key: "remote", Name: "Remote", Mode: catalog.AgentModeChannel, ChannelConfig: catalog.AgentChannelConfig{ChannelID: "peer-a", RemoteAgentKey: "remote-agent"}},
		},
	}
	reporter := newAgentCardReporter(context.Background(), source, agentCardReporterOptions{})

	items, failures := reporter.buildRegistrations("peer-a", []string{"query", "steer", "interrupt", "hitl"})
	if len(failures) != 0 || len(items) != 1 {
		t.Fatalf("unexpected build result items=%#v failures=%#v", items, failures)
	}
	item := items[0]
	if item.agentKey != "support-agent" || item.payload.Name != "售后助手" || item.payload.Role != "售后支持" || item.payload.Description != "处理售后问题" {
		t.Fatalf("unexpected registration %#v", item)
	}
	if strings.Join(item.payload.Capabilities, ",") != "all" || strings.Join(item.expanded, ",") != "query,steer,interrupt,hitl" {
		t.Fatalf("unexpected capabilities payload=%#v expanded=%#v", item.payload.Capabilities, item.expanded)
	}
	raw, _ := json.Marshal(item.payload)
	for _, forbidden := range []string{"skills", "tools", "tags", "fileTransfer"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("registration leaked %q: %s", forbidden, raw)
		}
	}
}

func TestBuildRegistrationsUsesExplicitCapabilitiesAndRejectsUnsupported(t *testing.T) {
	source := &cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}},
		defs:   map[string]catalog.AgentDefinition{"support": exportedTestAgent("support", catalog.AgentChannelAllow{Query: true, Submit: true})},
	}
	reporter := newAgentCardReporter(context.Background(), source, agentCardReporterOptions{})
	items, failures := reporter.buildRegistrations("peer-a", []string{"query", "hitl", "future"})
	if len(failures) != 0 || len(items) != 1 || strings.Join(items[0].payload.Capabilities, ",") != "query,hitl" {
		t.Fatalf("unexpected explicit capabilities items=%#v failures=%#v", items, failures)
	}
	items, failures = reporter.buildRegistrations("peer-a", []string{"query"})
	if len(items) != 0 || len(failures) != 1 || !strings.Contains(failures[0].err.Error(), "hitl") {
		t.Fatalf("expected unsupported hitl failure, items=%#v failures=%#v", items, failures)
	}
}

func TestValidateCardTextRejectsInvalidControls(t *testing.T) {
	if err := validateCardText("description", "line one\nline two\tvalue", defaultCardDescriptionRunes, false); err != nil {
		t.Fatalf("expected tab/newline to be allowed: %v", err)
	}
	if err := validateCardText("description", "bad\u0001value", defaultCardDescriptionRunes, false); err == nil {
		t.Fatal("expected control character to be rejected")
	}
}

func TestDecodeGatewayResponseClassifiesRetryableAndRejectedFailures(t *testing.T) {
	retrying := decodeGatewayResponse([]byte(`{"frame":"response","type":"agent.list","id":"req-1","code":503,"msg":"unavailable","data":{}}`), "req-1", agentListType)
	if !retrying.retryable || retrying.rejected {
		t.Fatalf("expected retryable 5xx outcome, got %#v", retrying)
	}
	rejected := decodeGatewayResponse([]byte(`{"frame":"response","type":"agent.register","id":"req-2","code":409,"msg":"in use","data":{}}`), "req-2", agentRegisterType)
	if rejected.retryable || !rejected.rejected {
		t.Fatalf("expected non-retryable 4xx rejection, got %#v", rejected)
	}
}

func TestAgentRegistrationReconcilesListRegisterAndUnregister(t *testing.T) {
	source := &cardTestCatalog{
		agents: []api.AgentSummary{{Key: "support"}},
		defs:   map[string]catalog.AgentDefinition{"support": exportedTestAgent("support", catalog.AgentChannelAllow{Query: true, Submit: true, Steer: true, Interrupt: true})},
	}
	requests := make(chan ws.RequestFrame, 16)
	var mu sync.Mutex
	owned := map[string]api.GatewayRegisteredAgent{
		"stale":   {GatewayAgentRegistration: api.GatewayAgentRegistration{AgentKey: "stale", Name: "Stale", Capabilities: []string{"query"}}, OwnedByCurrentSession: true},
		"foreign": {GatewayAgentRegistration: api.GatewayAgentRegistration{AgentKey: "foreign", Name: "Foreign", Capabilities: []string{"query"}}, OwnedByCurrentSession: false},
	}
	server, connections := newRegistrationGateway(t, func(conn *gws.Conn, req ws.RequestFrame) {
		requests <- req
		mu.Lock()
		defer mu.Unlock()
		switch req.Type {
		case agentListType:
			agents := make([]api.GatewayRegisteredAgent, 0, len(owned))
			ownedCount := 0
			for _, agent := range owned {
				agents = append(agents, agent)
				if agent.OwnedByCurrentSession {
					ownedCount++
				}
			}
			_ = conn.WriteJSON(ws.ResponseFrame{Frame: ws.FrameResponse, Type: req.Type, ID: req.ID, Code: 0, Msg: "success", Data: api.GatewayAgentListResult{PlatformKey: "platform-a", Count: len(agents), CurrentSessionOwnedCount: ownedCount, Agents: agents}})
		case agentUnregisterType:
			var payload api.GatewayAgentUnregisterPayload
			_ = json.Unmarshal(req.Payload, &payload)
			delete(owned, payload.AgentKey)
			accepted := true
			_ = conn.WriteJSON(ws.ResponseFrame{Frame: ws.FrameResponse, Type: req.Type, ID: req.ID, Code: 0, Msg: "success", Data: api.GatewayAgentUnregisterResult{Accepted: &accepted, AgentKey: payload.AgentKey, Status: "NOT_REGISTERED"}})
		case agentRegisterType:
			var payload api.GatewayAgentRegistration
			_ = json.Unmarshal(req.Payload, &payload)
			expanded := []string{"query", "steer", "interrupt", "hitl"}
			owned[payload.AgentKey] = api.GatewayRegisteredAgent{GatewayAgentRegistration: api.GatewayAgentRegistration{AgentKey: payload.AgentKey, Name: payload.Name, Role: payload.Role, Description: payload.Description, Capabilities: expanded}, OwnedByCurrentSession: true}
			accepted := true
			_ = conn.WriteJSON(ws.ResponseFrame{Frame: ws.FrameResponse, Type: req.Type, ID: req.ID, Code: 0, Msg: "success", Data: api.GatewayAgentRegisterResult{Accepted: &accepted, AgentKey: payload.AgentKey, RegistrationID: "reg-1", Status: "REGISTERED", Capabilities: expanded}})
		}
	})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter := newAgentCardReporter(ctx, source, agentCardReporterOptions{Debounce: 10 * time.Millisecond, AckTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond}, MaxConcurrent: 2})
	registry := New(ctx, testWebSocketConfig(), 50*time.Millisecond, ws.NewHub(), func(context.Context, *ws.Conn, ws.RequestFrame) {}, reporter)
	defer registry.StopAll()
	if err := registry.Register(config.GatewayEntry{ID: "peer-a", Channel: "peer-a", URL: "ws" + strings.TrimPrefix(server.URL, "http"), HandshakeTimeout: 1, ReconnectMin: 1, ReconnectMax: 1}); err != nil {
		t.Fatal(err)
	}
	peer := <-connections
	writeConnected(t, peer, "platform-a", agentRegistrationModeMulti)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusAccepted)

	gotTypes := make([]string, 0, 4)
	deadline := time.After(2 * time.Second)
	for len(gotTypes) < 4 {
		select {
		case req := <-requests:
			gotTypes = append(gotTypes, req.Type)
		case <-deadline:
			t.Fatalf("timed out waiting for reconciliation requests: %#v", gotTypes)
		}
	}
	if strings.Join(gotTypes, ",") != "agent.list,agent.unregister,agent.register,agent.list" {
		t.Fatalf("unexpected reconciliation sequence %#v", gotTypes)
	}
	mu.Lock()
	_, foreignPreserved := owned["foreign"]
	mu.Unlock()
	if !foreignPreserved {
		t.Fatal("reconciliation must not unregister another session's agent")
	}
}

func TestInitialListFailureRetriesWithoutMutatingRegistrations(t *testing.T) {
	source := &cardTestCatalog{agents: []api.AgentSummary{{Key: "support"}}, defs: map[string]catalog.AgentDefinition{"support": exportedTestAgent("support", catalog.AgentChannelAllow{Query: true})}}
	requests := make(chan ws.RequestFrame, 8)
	server, connections := newRegistrationGateway(t, func(conn *gws.Conn, req ws.RequestFrame) {
		requests <- req
		_ = conn.WriteJSON(ws.ResponseFrame{Frame: ws.FrameResponse, Type: req.Type, ID: req.ID, Code: http.StatusServiceUnavailable, Msg: "temporarily unavailable", Data: map[string]any{}})
	})
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter := newAgentCardReporter(ctx, source, agentCardReporterOptions{AckTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond, time.Millisecond}})
	registry := New(ctx, testWebSocketConfig(), 50*time.Millisecond, ws.NewHub(), func(context.Context, *ws.Conn, ws.RequestFrame) {}, reporter)
	defer registry.StopAll()
	if err := registry.Register(config.GatewayEntry{ID: "peer-a", Channel: "peer-a", URL: "ws" + strings.TrimPrefix(server.URL, "http"), HandshakeTimeout: 1, ReconnectMin: 1, ReconnectMax: 1}); err != nil {
		t.Fatal(err)
	}
	peer := <-connections
	writeConnected(t, peer, "platform-a", agentRegistrationModeMulti)
	waitForCardStatus(t, reporter, "peer-a", "support", agentCardStatusError)
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case req := <-requests:
			if req.Type != agentListType {
				t.Fatalf("initial list failure must not mutate registrations, got %s", req.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for list attempt %d", attempt+1)
		}
	}
	select {
	case req := <-requests:
		t.Fatalf("expected exactly three initial list attempts, got extra %s", req.Type)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestAgentRegistrationWaitsForConnectedAndAggregatesSessions(t *testing.T) {
	source := &cardTestCatalog{agents: []api.AgentSummary{{Key: "support"}}, defs: map[string]catalog.AgentDefinition{"support": exportedTestAgent("support", catalog.AgentChannelAllow{Query: true})}}
	reporter := newAgentCardReporter(context.Background(), source, agentCardReporterOptions{AckTimeout: 20 * time.Millisecond, RetryDelays: []time.Duration{time.Millisecond}})
	connA := ws.NewConn(nil, nil, testWebSocketConfig(), time.Second, ws.AuthSession{})
	connB := ws.NewConn(nil, nil, testWebSocketConfig(), time.Second, ws.AuthSession{})
	reporter.ChannelConnected("peer-a", connA, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	status, ok := reporter.AgentCardStatus("peer-a", "support")
	if !ok || status.Status != agentCardStatusError {
		t.Fatalf("expected handshake timeout error, got %#v ok=%v", status, ok)
	}
	reporter.ChannelConnected("peer-a", connB, time.Second)
	reporter.mu.Lock()
	reporter.sessions[connA].statuses["support"] = api.GatewayAgentCardReportStatus{Status: agentCardStatusAccepted, UpdatedAt: 1}
	reporter.sessions[connB].statuses["support"] = api.GatewayAgentCardReportStatus{Status: agentCardStatusRejected, UpdatedAt: 2}
	reporter.mu.Unlock()
	status, _ = reporter.AgentCardStatus("peer-a", "support")
	if status.Status != agentCardStatusRejected {
		t.Fatalf("expected rejected aggregate, got %#v", status)
	}
	reporter.ChannelDisconnected("peer-a", connB)
	status, _ = reporter.AgentCardStatus("peer-a", "support")
	if status.Status != agentCardStatusAccepted {
		t.Fatalf("expected surviving accepted session, got %#v", status)
	}
}

func TestConflictingConnectedDeclarationStopsSessionReconciliation(t *testing.T) {
	source := &cardTestCatalog{agents: []api.AgentSummary{{Key: "support"}}, defs: map[string]catalog.AgentDefinition{"support": exportedTestAgent("support", catalog.AgentChannelAllow{Query: true})}}
	reporter := newAgentCardReporter(context.Background(), source, agentCardReporterOptions{})
	conn := ws.NewConn(nil, nil, testWebSocketConfig(), time.Second, ws.AuthSession{})
	reporter.ChannelConnected("peer-a", conn, time.Second)
	reporter.mu.Lock()
	reporter.sessions[conn].connected = &api.GatewayAgentConnectedData{
		SessionID: "remote-session", PlatformKey: "platform-a", RegistrationMode: agentRegistrationModeMulti,
		AgentRegistration: api.GatewayAgentRegistrationSupport{Version: agentRegistrationVersion, MaxAgentsPerPlatformChannel: 100, SupportedCapabilities: []string{"query"}},
	}
	reporter.mu.Unlock()
	reporter.ChannelPush("peer-a", conn, ws.PushFrame{Frame: ws.FramePush, Type: agentConnectedPushType, Data: api.GatewayAgentConnectedData{
		SessionID: "remote-session", PlatformKey: "platform-b", RegistrationMode: agentRegistrationModeMulti,
		AgentRegistration: api.GatewayAgentRegistrationSupport{Version: agentRegistrationVersion, MaxAgentsPerPlatformChannel: 100, SupportedCapabilities: []string{"query"}},
	}})
	status, ok := reporter.AgentCardStatus("peer-a", "support")
	if !ok || status.Status != agentCardStatusError || !strings.Contains(status.Reason, "conflicting") {
		t.Fatalf("expected terminal connected conflict, got %#v ok=%v", status, ok)
	}
	reporter.mu.Lock()
	original := *reporter.sessions[conn].connected
	reporter.mu.Unlock()
	reporter.ChannelPush("peer-a", conn, ws.PushFrame{Frame: ws.FramePush, Type: agentConnectedPushType, Data: original})
	reporter.mu.Lock()
	conflicted := reporter.sessions[conn].conflicted
	reporter.mu.Unlock()
	if !conflicted {
		t.Fatal("later connected declaration must not recover a conflicted session")
	}
}

func TestSingleEmployeeRejectsMultipleExportsWithoutMutation(t *testing.T) {
	source := &cardTestCatalog{
		agents: []api.AgentSummary{{Key: "a"}, {Key: "b"}},
		defs: map[string]catalog.AgentDefinition{
			"a": exportedTestAgent("a", catalog.AgentChannelAllow{Query: true}),
			"b": exportedTestAgent("b", catalog.AgentChannelAllow{Query: true}),
		},
	}
	requests := make(chan ws.RequestFrame, 4)
	server, connections := newRegistrationGateway(t, func(_ *gws.Conn, req ws.RequestFrame) { requests <- req })
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reporter := newAgentCardReporter(ctx, source, agentCardReporterOptions{AckTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond}})
	registry := New(ctx, testWebSocketConfig(), 50*time.Millisecond, ws.NewHub(), func(context.Context, *ws.Conn, ws.RequestFrame) {}, reporter)
	defer registry.StopAll()
	if err := registry.Register(config.GatewayEntry{ID: "peer-a", Channel: "peer-a", URL: "ws" + strings.TrimPrefix(server.URL, "http"), HandshakeTimeout: 1, ReconnectMin: 1, ReconnectMax: 1}); err != nil {
		t.Fatal(err)
	}
	peer := <-connections
	writeConnected(t, peer, "platform-a", agentRegistrationModeSingle)
	waitForCardStatus(t, reporter, "peer-a", "a", agentCardStatusError)
	select {
	case req := <-requests:
		t.Fatalf("did not expect gateway mutation/list request, got %#v", req)
	case <-time.After(50 * time.Millisecond):
	}
}

func exportedTestAgent(key string, allow catalog.AgentChannelAllow) catalog.AgentDefinition {
	return catalog.AgentDefinition{Key: key, Name: strings.ToUpper(key), Role: "assistant", Description: "safe", Mode: "REACT", ChannelConfig: catalog.AgentChannelConfig{Exports: []catalog.AgentChannelExport{{ChannelID: "peer-a", Allow: allow}}}}
}

func newRegistrationGateway(t *testing.T, handler func(*gws.Conn, ws.RequestFrame)) (*httptest.Server, <-chan *gws.Conn) {
	t.Helper()
	connections := make(chan *gws.Conn, 4)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		connections <- conn
		go func() {
			defer conn.Close()
			for {
				var frame ws.RequestFrame
				if err := conn.ReadJSON(&frame); err != nil {
					return
				}
				handler(conn, frame)
			}
		}()
	}))
	return server, connections
}

func writeConnected(t *testing.T, conn *gws.Conn, platformKey, mode string) {
	t.Helper()
	if err := conn.WriteJSON(ws.PushFrame{Frame: ws.FramePush, Type: agentConnectedPushType, Data: api.GatewayAgentConnectedData{
		SessionID: "remote-session", PlatformKey: platformKey, RegistrationMode: mode,
		AgentRegistration: api.GatewayAgentRegistrationSupport{Version: agentRegistrationVersion, MaxAgentsPerPlatformChannel: 100, SupportedCapabilities: []string{"query", "steer", "interrupt", "hitl"}},
	}}); err != nil {
		t.Fatal(err)
	}
}

func testWebSocketConfig() config.WebSocketConfig {
	return config.WebSocketConfig{MaxMessageSizeBytes: 1 << 20, PingInterval: 1, WriteTimeout: 1, WriteQueueSize: 32, MaxObservesPerConn: 4}
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
