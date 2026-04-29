package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/schedule"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

type scheduleTestServer struct {
	server       *Server
	orchestrator *schedule.Orchestrator
	executions   *schedule.ExecutionStore
}

func newScheduleTestServer(t *testing.T, websocket bool) scheduleTestServer {
	t.Helper()
	root := t.TempDir()
	registry := schedule.NewRegistry(root, nil)
	executions, err := schedule.NewExecutionStore(root, "executions.db")
	if err != nil {
		t.Fatalf("new execution store: %v", err)
	}
	t.Cleanup(func() { _ = executions.Close() })

	orchestrator := schedule.NewOrchestrator(registry, nil, config.ScheduleConfig{DefaultZoneID: "UTC", PoolSize: 1})
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	t.Cleanup(func() {
		done := orchestrator.Stop()
		select {
		case <-done.Done():
		}
	})

	cfg := config.Config{Auth: config.AuthConfig{Enabled: false}}
	var hub *ws.Hub
	if websocket {
		cfg.WebSocket.Enabled = true
		cfg.WebSocket.WriteQueueSize = 4
		cfg.WebSocket.PingIntervalMs = 30000
		hub = ws.NewHub()
		t.Cleanup(func() { hub.CloseAll(gws.CloseNormalClosure, "test done") })
	}
	deps := Dependencies{
		Config:               cfg,
		ScheduleOrchestrator: orchestrator,
		ScheduleRegistry:     registry,
		ScheduleExecutions:   executions,
	}
	if hub != nil {
		deps.Notifications = hub
	}
	server, err := New(deps)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return scheduleTestServer{server: server, orchestrator: orchestrator, executions: executions}
}

func TestScheduleHTTPCRUDAndExecutionHistory(t *testing.T) {
	fixture := newScheduleTestServer(t, false)

	create := postScheduleJSON[api.ScheduleDetailResponse](t, fixture.server, "/api/schedule-create", map[string]any{
		"name":        "Daily Demo",
		"description": "Demo schedule",
		"cron":        "17 9 * * *",
		"agentKey":    "demo-agent",
		"query": map[string]any{
			"message": "hello",
			"params":  map[string]any{"kind": "daily"},
		},
	})
	if create.ID != "daily-demo" || create.Query.Message != "hello" || create.NextFireTime == nil {
		t.Fatalf("unexpected create response %#v", create)
	}

	executionID, err := fixture.executions.RecordStart(create.ID, create.Name, create.SourceFile, create.AgentKey, create.TeamID)
	if err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := fixture.executions.RecordComplete(executionID, nil); err != nil {
		t.Fatalf("record complete: %v", err)
	}

	list := postScheduleJSON[api.ScheduleListResponse](t, fixture.server, "/api/schedules", map[string]any{"tag": "ignored"})
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].LastExecution == nil || list.Items[0].LastExecution.Status != "success" {
		t.Fatalf("unexpected list response %#v", list)
	}

	update := postScheduleJSON[api.ScheduleDetailResponse](t, fixture.server, "/api/schedule-update", map[string]any{
		"id":          create.ID,
		"description": "Updated schedule",
		"query": map[string]any{
			"message": "updated",
		},
	})
	if update.Description != "Updated schedule" || update.Query.Message != "updated" {
		t.Fatalf("unexpected update response %#v", update)
	}

	toggled := postScheduleJSON[api.ScheduleDetailResponse](t, fixture.server, "/api/schedule-toggle", map[string]any{
		"id":      create.ID,
		"enabled": false,
	})
	if toggled.Enabled || toggled.NextFireTime != nil {
		t.Fatalf("unexpected toggle response %#v", toggled)
	}

	deleted := postScheduleJSON[map[string]any](t, fixture.server, "/api/schedule-delete", map[string]any{"id": create.ID})
	if deleted["id"] != create.ID || deleted["deleted"] != true {
		t.Fatalf("unexpected delete response %#v", deleted)
	}

	history := postScheduleJSON[api.ScheduleExecutionListResponse](t, fixture.server, "/api/schedule-executions", map[string]any{"id": create.ID})
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].ID != executionID {
		t.Fatalf("unexpected history response %#v", history)
	}
}

func TestScheduleWSRoutesMirrorHTTP(t *testing.T) {
	fixture := newScheduleTestServer(t, true)
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readScheduleConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/schedule-create",
		ID:    "create",
		Payload: ws.MarshalPayload(map[string]any{
			"name":        "WS Demo",
			"description": "Demo schedule",
			"cron":        "17 9 * * *",
			"agentKey":    "demo-agent",
			"query":       map[string]any{"message": "hello"},
		}),
	}); err != nil {
		t.Fatalf("write create request: %v", err)
	}
	var createFrame ws.ResponseFrame
	if err := conn.ReadJSON(&createFrame); err != nil {
		t.Fatalf("read create response: %v", err)
	}
	created, err := marshalScheduleResponseData[api.ScheduleDetailResponse](createFrame.Data)
	if err != nil {
		t.Fatalf("decode create data: %v", err)
	}
	if createFrame.Frame != ws.FrameResponse || createFrame.ID != "create" || created.ID != "ws-demo" {
		t.Fatalf("unexpected create frame %#v data=%#v", createFrame, created)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/schedules",
		ID:      "list",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write list request: %v", err)
	}
	var listFrame ws.ResponseFrame
	if err := conn.ReadJSON(&listFrame); err != nil {
		t.Fatalf("read list response: %v", err)
	}
	list, err := marshalScheduleResponseData[api.ScheduleListResponse](listFrame.Data)
	if err != nil {
		t.Fatalf("decode list data: %v", err)
	}
	if listFrame.Frame != ws.FrameResponse || listFrame.ID != "list" || list.Total != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("unexpected list frame %#v data=%#v", listFrame, list)
	}
}

func readScheduleConnectedPush(t *testing.T, conn *gws.Conn) {
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

func marshalScheduleResponseData[T any](value any) (T, error) {
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

func postScheduleJSON[T any](t *testing.T, server *Server, path string, payload any) T {
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
