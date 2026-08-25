package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/config"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type automationTestServer struct {
	server       *Server
	orchestrator *automation.Orchestrator
	executions   *automation.ExecutionStore
}

func newAutomationTestServer(t *testing.T, websocket bool) automationTestServer {
	t.Helper()
	root := t.TempDir()
	registry := automation.NewRegistry(root, nil)
	executions, err := automation.NewExecutionStore(root, "executions.db")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	t.Cleanup(func() { _ = executions.Close() })

	orchestrator := automation.NewOrchestrator(registry, nil, config.AutomationConfig{DefaultZoneID: "UTC", PoolSize: 1})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	t.Cleanup(func() {
		done := orchestrator.Stop()
		select {
		case <-done.Done():
		}
	})

	cfg := config.Config{
		Auth:       config.AuthConfig{Enabled: false},
		Automation: config.AutomationConfig{DefaultZoneID: "UTC"},
	}
	var hub *ws.Hub
	if websocket {
		cfg.WebSocket.WriteQueueSize = 4
		cfg.WebSocket.PingInterval = 30000
		hub = ws.NewHub()
		t.Cleanup(func() { hub.CloseAll(gws.CloseNormalClosure, "test done") })
	}
	deps := Dependencies{
		Config:                 cfg,
		AutomationOrchestrator: orchestrator,
		AutomationRegistry:     registry,
		AutomationExecutions:   executions,
	}
	if hub != nil {
		deps.Notifications = hub
	}
	server, err := New(deps)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return automationTestServer{server: server, orchestrator: orchestrator, executions: executions}
}

func TestAutomationHTTPCRUDAndExecutionHistory(t *testing.T) {
	fixture := newAutomationTestServer(t, false)

	create := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name":        "Daily Demo",
		"description": "Demo automation",
		"cron":        "17 9 * * *",
		"agentKey":    "demo-agent",
		"zoneId":      "Asia/Shanghai",
		"query": map[string]any{
			"message": "hello",
			"params":  map[string]any{"kind": "daily"},
		},
	})
	if create.ID != "daily-demo" || create.Query.Message != "hello" || create.NextFireAt == nil || *create.NextFireAt <= 0 || create.NextFireTime == nil {
		t.Fatalf("unexpected create response %#v", create)
	}
	assertAutomationReadableTimeMatches(t, *create.NextFireTime, *create.NextFireAt, time.UTC)

	executionID, err := fixture.executions.RecordStart(create.ID, create.Name, create.SourceFile, create.AgentKey, create.TeamID, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("record start: %v", err)
	}
	stored, err := fixture.executions.GetExecution(executionID)
	if err != nil || stored == nil {
		t.Fatalf("load started execution: %#v err=%v", stored, err)
	}
	completedAt := stored.StartedAt + 25
	duration := int64(25)
	stored.ChatID = "chat-execution"
	stored.RunID = "run-execution"
	stored.QueryContent = "hello"
	stored.Status = automation.ExecutionStatusSuccess
	stored.FinishReason = "complete"
	stored.ResultContent = "完整助手输出结果"
	stored.RunStartedAt = &stored.StartedAt
	stored.CompletedAt = &completedAt
	stored.DurationMs = &duration
	if err := fixture.executions.Upsert(*stored); err != nil {
		t.Fatalf("record complete: %v", err)
	}

	list := postAutomationJSON[api.AutomationListResponse](t, fixture.server, "/api/automations", map[string]any{"tag": "ignored"})
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].NextFireAt == nil || *list.Items[0].NextFireAt <= 0 || list.Items[0].NextFireTime == nil || list.Items[0].LastExecution == nil || list.Items[0].LastExecution.Status != "success" {
		t.Fatalf("unexpected list response %#v", list)
	}
	if list.Items[0].LastExecution.ZoneID != "Asia/Shanghai" {
		t.Fatalf("expected execution zone snapshot, got %#v", list.Items[0].LastExecution)
	}
	assertAutomationReadableTimeMatches(t, list.Items[0].LastExecution.StartedTime, list.Items[0].LastExecution.StartedAt, time.UTC)
	if list.Items[0].LastExecution.CompletedAt == nil || *list.Items[0].LastExecution.CompletedAt <= 0 || strings.TrimSpace(list.Items[0].LastExecution.CompletedTime) == "" {
		t.Fatalf("expected completed timing on last execution %#v", list.Items[0].LastExecution)
	}
	assertAutomationReadableTimeMatches(t, list.Items[0].LastExecution.CompletedTime, *list.Items[0].LastExecution.CompletedAt, time.UTC)

	update := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/update", map[string]any{
		"id":          create.ID,
		"description": "Updated automation",
		"query": map[string]any{
			"message": "updated",
		},
	})
	if update.Description != "Updated automation" || update.Query.Message != "updated" {
		t.Fatalf("unexpected update response %#v", update)
	}

	toggled := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/toggle", map[string]any{
		"id":      create.ID,
		"enabled": false,
	})
	if toggled.Enabled || toggled.NextFireAt != nil || toggled.NextFireTime != nil {
		t.Fatalf("unexpected toggle response %#v", toggled)
	}

	deleted := postAutomationJSON[map[string]any](t, fixture.server, "/api/automation/delete", map[string]any{"id": create.ID})
	if deleted["id"] != create.ID || deleted["deleted"] != true {
		t.Fatalf("unexpected delete response %#v", deleted)
	}

	history := postAutomationJSON[api.AutomationExecutionListResponse](t, fixture.server, "/api/automation/executions", map[string]any{"id": create.ID})
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].ID != executionID {
		t.Fatalf("unexpected history response %#v", history)
	}
	if history.Items[0].StartedAt <= 0 || strings.TrimSpace(history.Items[0].StartedTime) == "" {
		t.Fatalf("expected started timing on history item %#v", history.Items[0])
	}
	if history.Items[0].ZoneID != "Asia/Shanghai" {
		t.Fatalf("expected persisted execution zone after automation deletion, got %#v", history.Items[0])
	}
	assertAutomationReadableTimeMatches(t, history.Items[0].StartedTime, history.Items[0].StartedAt, time.UTC)
	if history.Items[0].CompletedAt == nil || *history.Items[0].CompletedAt <= 0 || strings.TrimSpace(history.Items[0].CompletedTime) == "" {
		t.Fatalf("expected completed timing on history item %#v", history.Items[0])
	}
	assertAutomationReadableTimeMatches(t, history.Items[0].CompletedTime, *history.Items[0].CompletedAt, time.UTC)
	if history.Items[0].ResultPreview != "完整助手输出结果" || !history.Items[0].HasResult || history.Items[0].ChatID != "chat-execution" || history.Items[0].RunID != "run-execution" {
		t.Fatalf("expected execution result preview and run links, got %#v", history.Items[0])
	}
	detail := postAutomationJSON[api.AutomationExecutionDetailResponse](t, fixture.server, "/api/automation/execution", map[string]any{"executionId": executionID})
	if detail.QueryContent != "hello" || detail.ResultContent != "完整助手输出结果" || detail.FinishReason != "complete" {
		t.Fatalf("unexpected execution detail %#v", detail)
	}
}

func TestAutomationHTTPCreateAllowsOmittedDescriptionAndQueryDefaults(t *testing.T) {
	fixture := newAutomationTestServer(t, false)

	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name":     "Minimal Automation",
		"cron":     "0 9 * * *",
		"agentKey": "demo-agent",
		"query": map[string]any{
			"message": "hello",
		},
	})
	if created.Description != "" || created.ZoneID != "" || created.Query.Role != "" || created.Query.Hidden != nil {
		t.Fatalf("expected optional response fields to remain omitted, got %#v", created)
	}

	definitions, err := fixture.server.deps.AutomationRegistry.Load()
	if err != nil || len(definitions) != 1 {
		t.Fatalf("load automation registry: definitions=%#v err=%v", definitions, err)
	}
	request := definitions[0].ToQueryRequest()
	if request.Role != api.QueryRoleAutomation || request.Hidden == nil || !*request.Hidden {
		t.Fatalf("unexpected execution defaults %#v", request)
	}
}

func TestAutomationHistoryUnavailableDoesNotBreakConfigurationAPI(t *testing.T) {
	root := t.TempDir()
	registry := automation.NewRegistry(root, nil)
	if err := registry.Persist(automation.Definition{
		ID:       "daily",
		Name:     "Daily",
		Enabled:  true,
		Cron:     "0 9 * * *",
		AgentKey: "agent-a",
		Query:    automation.Query{Message: "hello"},
	}); err != nil {
		t.Fatalf("persist automation: %v", err)
	}
	server, err := New(Dependencies{
		Config:             config.Config{Auth: config.AuthConfig{Enabled: false}, Automation: config.AutomationConfig{DefaultZoneID: "UTC"}},
		AutomationRegistry: registry,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	list := postAutomationJSON[api.AutomationListResponse](t, server, "/api/automations", map[string]any{})
	if list.Total != 1 || list.ExecutionHistory.Available || list.ExecutionHistory.State != string(automation.ExecutionHistoryUnavailable) {
		t.Fatalf("configuration list must remain available with explicit history status: %#v", list)
	}
	if status := postAutomationStatus(t, server, "/api/automation/executions", map[string]any{"id": "daily"}); status != http.StatusServiceUnavailable {
		t.Fatalf("history endpoint returned %d, want 503", status)
	}
}

func TestAutomationHTTPCreateAndUpdatePreserveQueryMessageExactly(t *testing.T) {
	fixture := newAutomationTestServer(t, false)
	createMessage := "  initial automation message  "
	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name":        "Weekly Report Reminder",
		"description": "Preserve message automation",
		"cron":        "0 17 * * 5",
		"agentKey":    "zenmi",
		"query": map[string]any{
			"message": createMessage,
			"role":    "automation",
		},
	})
	if got := created.Query.Message; got != createMessage {
		t.Fatalf("create message changed, want %q got %q", createMessage, got)
	}

	updateMessage := "请提醒主人现在是周五下午 5 点，该开始撰写周报了。\n气温气温气温"
	updated := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/update", map[string]any{
		"id": created.ID,
		"query": map[string]any{
			"message": updateMessage,
			"role":    "automation",
		},
	})
	if got := updated.Query.Message; got != updateMessage {
		t.Fatalf("update message changed, want %q got %q", updateMessage, got)
	}

	detail := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation", map[string]any{"id": created.ID})
	if got := detail.Query.Message; got != updateMessage {
		t.Fatalf("reloaded message changed, want %q got %q", updateMessage, got)
	}
	definitions, err := fixture.server.deps.AutomationRegistry.Load()
	if err != nil {
		t.Fatalf("load automation registry: %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("expected one automation definition, got %d", len(definitions))
	}
	if got := definitions[0].ToQueryRequest().Message; got != updateMessage {
		t.Fatalf("execution query message changed, want %q got %q", updateMessage, got)
	}
}

func TestMapAutomationSummaryUsesPlatformDisplayZoneAndKeepsNextFireEpochPrecision(t *testing.T) {
	server := &Server{deps: Dependencies{Config: config.Config{Automation: config.AutomationConfig{DefaultZoneID: "UTC"}}}}
	next := time.Date(2026, time.January, 2, 3, 4, 5, 123_456_000, time.FixedZone("UTC+8", 8*60*60))
	response, err := server.mapAutomationSummary(automation.Definition{ID: "precision", Name: "Precision", Enabled: true}, &next)
	if err != nil {
		t.Fatalf("map automation summary: %v", err)
	}
	if response.NextFireAt == nil || response.NextFireTime == nil {
		t.Fatalf("expected paired next fire values, got %#v", response)
	}
	if *response.NextFireAt != next.UnixMilli() {
		t.Fatalf("nextFireAt changed: got %d want %d", *response.NextFireAt, next.UnixMilli())
	}
	if got, want := *response.NextFireTime, "2026-01-01 19:04:05"; got != want {
		t.Fatalf("nextFireTime = %q, want platform display %q", got, want)
	}
}

func TestAutomationTimeContractErrorsUse422(t *testing.T) {
	server := &Server{}
	violation := timecontract.ValidateEpochMillis(0, "startedAt", "automation.test")
	recorder := httptest.NewRecorder()
	server.writeAutomationHTTPResponse(recorder, nil, violation)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "time_contract_violation") {
		t.Fatalf("expected HTTP 422 time contract violation, got %d %s", recorder.Code, recorder.Body.String())
	}

}

func TestAutomationAdminManagementRoutesRemoved(t *testing.T) {
	fixture := newAutomationTestServer(t, false)

	for _, path := range []string{
		"/api/admin/automations/create",
		"/api/admin/automations/update",
		"/api/admin/automations/delete",
		"/api/admin/automations/toggle",
	} {
		if status := postAutomationStatus(t, fixture.server, path, map[string]any{}); status != http.StatusNotFound {
			t.Fatalf("%s returned %d, want %d", path, status, http.StatusNotFound)
		}
	}
}

func TestAutomationWSRuntimeRoutesAndManagementRoutesRejected(t *testing.T) {
	fixture := newAutomationTestServer(t, true)
	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name":        "WS Demo",
		"description": "Demo automation",
		"cron":        "17 9 * * *",
		"agentKey":    "demo-agent",
		"query":       map[string]any{"message": "hello"},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readAutomationConnectedPush(t, conn)

	for _, removed := range []struct {
		id        string
		frameType string
	}{
		{id: "automation-create", frameType: "/api/automation/create"},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{
			Frame: ws.FrameRequest,
			Type:  removed.frameType,
			ID:    removed.id,
		}); err != nil {
			t.Fatalf("write removed route request: %v", err)
		}
		var errFrame ws.ErrorFrame
		if err := conn.ReadJSON(&errFrame); err != nil {
			t.Fatalf("read removed route response: %v", err)
		}
		if errFrame.Frame != ws.FrameError || errFrame.Type != "invalid_request" || errFrame.ID != removed.id || errFrame.Code != http.StatusBadRequest ||
			!strings.Contains(errFrame.Msg, "unknown type: "+removed.frameType) {
			t.Fatalf("unexpected removed route frame for %s: %#v", removed.frameType, errFrame)
		}
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/automations",
		ID:      "list",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write list request: %v", err)
	}
	var listFrame ws.ResponseFrame
	if err := conn.ReadJSON(&listFrame); err != nil {
		t.Fatalf("read list response: %v", err)
	}
	list, err := marshalAutomationResponseData[api.AutomationListResponse](listFrame.Data)
	if err != nil {
		t.Fatalf("decode list data: %v", err)
	}
	if listFrame.Frame != ws.FrameResponse || listFrame.ID != "list" || list.Total != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("unexpected list frame %#v data=%#v", listFrame, list)
	}
}

func readAutomationConnectedPush(t *testing.T, conn *gws.Conn) {
	t.Helper()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read initial ws frame: %v", err)
	}
	var push ws.PushFrame
	if err := json.Unmarshal(raw, &push); err != nil {
		t.Fatalf("decode initial ws frame: %v", err)
	}
	if push.Frame != ws.FramePush || push.Type != "connected" {
		t.Fatalf("unexpected initial ws frame: %s", string(raw))
	}
}

func marshalAutomationResponseData[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func postAutomationJSON[T any](t *testing.T, server *Server, path string, payload any) T {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var parsed api.ApiResponse[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if parsed.Code != 0 {
		t.Fatalf("unexpected api response %#v", parsed)
	}
	return parsed.Data
}

func assertAutomationReadableTimeMatches(t *testing.T, value string, epochMillis int64, loc *time.Location) {
	t.Helper()
	if strings.TrimSpace(value) == "" {
		t.Fatal("expected readable time")
	}
	if got, want := value, time.UnixMilli(epochMillis).In(loc).Format("2006-01-02 15:04:05"); got != want {
		t.Fatalf("readable time = %q, want %q", got, want)
	}
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", value, loc); err != nil {
		t.Fatalf("expected automation display time, got %q: %v", value, err)
	}
}

func postAutomationStatus(t *testing.T, server *Server, path string, payload any) int {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	server.ServeHTTP(rec, req)
	return rec.Code
}
