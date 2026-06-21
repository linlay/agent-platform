package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/config"
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

	cfg := config.Config{Auth: config.AuthConfig{Enabled: false}}
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

	create := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/admin/automations/create", map[string]any{
		"name":        "Daily Demo",
		"description": "Demo automation",
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

	list := postAutomationJSON[api.AutomationListResponse](t, fixture.server, "/api/automations", map[string]any{"tag": "ignored"})
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].LastExecution == nil || list.Items[0].LastExecution.Status != "success" {
		t.Fatalf("unexpected list response %#v", list)
	}

	update := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/admin/automations/update", map[string]any{
		"id":          create.ID,
		"description": "Updated automation",
		"query": map[string]any{
			"message": "updated",
		},
	})
	if update.Description != "Updated automation" || update.Query.Message != "updated" {
		t.Fatalf("unexpected update response %#v", update)
	}

	toggled := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/admin/automations/toggle", map[string]any{
		"id":      create.ID,
		"enabled": false,
	})
	if toggled.Enabled || toggled.NextFireTime != nil {
		t.Fatalf("unexpected toggle response %#v", toggled)
	}

	deleted := postAutomationJSON[map[string]any](t, fixture.server, "/api/admin/automations/delete", map[string]any{"id": create.ID})
	if deleted["id"] != create.ID || deleted["deleted"] != true {
		t.Fatalf("unexpected delete response %#v", deleted)
	}

	history := postAutomationJSON[api.AutomationExecutionListResponse](t, fixture.server, "/api/automation/executions", map[string]any{"id": create.ID})
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].ID != executionID {
		t.Fatalf("unexpected history response %#v", history)
	}
}

func TestAutomationWSRuntimeRoutesAndManagementRoutesRejected(t *testing.T) {
	fixture := newAutomationTestServer(t, true)
	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/admin/automations/create", map[string]any{
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
		{id: "admin-create", frameType: "/api/admin/automations/create"},
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
