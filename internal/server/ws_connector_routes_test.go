package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func connectorUsageSocket(t *testing.T, server *Server) *gws.Conn {
	t.Helper()
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(host.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	readConnectedPush(t, conn)
	return conn
}

func sendConnectorUsageRequest(t *testing.T, conn *gws.Conn, route, id string, payload any) {
	t.Helper()
	request := ws.RequestFrame{Frame: ws.FrameRequest, Type: route, ID: id}
	if payload != nil {
		request.Payload = marshalPayload(payload)
	}
	if err := conn.WriteJSON(request); err != nil {
		t.Fatal(err)
	}
}

func TestWSConnectorUsageCatalogMatchesHTTPAndConnectionLocale(t *testing.T) {
	f := connectorUsageFixture(t)
	conn := connectorUsageSocket(t, f.server)
	sendConnectorUsageRequest(t, conn, "/api/locale", "zh", map[string]any{"locale": "zh-CN"})
	waitForWebSocketResponseData[map[string]any](t, conn, "zh")
	for _, key := range []string{"", "mock-agent"} {
		id := "catalog-" + key
		payload := map[string]any{}
		path := "/api/connectors"
		if key != "" {
			payload["agentKey"] = key
			path += "?agentKey=" + key
		}
		sendConnectorUsageRequest(t, conn, "/api/connectors", id, payload)
		response := waitForWebSocketResponseData[api.ConnectorOptionsResponse](t, conn, id)
		rec := connectorUsageRequest(f.server, http.MethodGet, path, nil)
		var expected api.ApiResponse[api.ConnectorOptionsResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &expected); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || !reflect.DeepEqual(response, expected.Data) {
			t.Fatalf("HTTP/WS catalog mismatch: %+v %s", response, rec.Body.String())
		}
		encoded, _ := json.Marshal(response)
		for _, forbidden := range []string{"builtin", "readOnly", "auth_mode", "skills", "version"} {
			if strings.Contains(string(encoded), `"`+forbidden+`"`) {
				t.Fatalf("management field leaked: %s", encoded)
			}
		}
	}
	sendConnectorUsageRequest(t, conn, "/api/locale", "en", map[string]any{"locale": "en-US"})
	waitForWebSocketResponseData[map[string]any](t, conn, "en")
	sendConnectorUsageRequest(t, conn, "/api/connectors", "english", nil)
	response := waitForWebSocketResponseData[api.ConnectorOptionsResponse](t, conn, "english")
	for _, item := range response.Connectors {
		if item.ID == connector.PlatformControlConnectorID {
			if item.Name != "Platform Control" {
				t.Fatalf("connection locale ignored: %+v", item)
			}
			return
		}
	}
	t.Fatal("missing non-preset builtin connector")
}

func TestWSAgentConnectorUsageSharesSourceAndPendingPublication(t *testing.T) {
	f := connectorUsageFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Errorf("reload after lease: %v", err)
		}
	})
	conn := connectorUsageSocket(t, f.server)
	read := func(id string, payload any) api.AgentConnectorsResponse {
		t.Helper()
		sendConnectorUsageRequest(t, conn, "/api/agents/connectors", id, payload)
		data := waitForWebSocketResponseData[map[string]json.RawMessage](t, conn, id)
		if len(data) != 3 || data["agentKey"] == nil || data["connectorIds"] == nil || data["reloadPending"] == nil {
			t.Fatalf("usage projection leaked management fields: %+v", data)
		}
		encoded, _ := json.Marshal(data)
		var response api.AgentConnectorsResponse
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		rec := connectorUsageRequest(f.server, http.MethodGet, "/api/agents/connectors?agentKey=mock-agent", nil)
		var expected api.ApiResponse[api.AgentConnectorsResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response, expected.Data) {
			t.Fatalf("HTTP/WS mount mismatch: %+v %+v", response, expected.Data)
		}
		return response
	}
	initial := read("initial", map[string]any{"agentKey": "mock-agent"})
	if !reflect.DeepEqual(initial.ConnectorIDs, []string{"meeting"}) || initial.ReloadPending {
		t.Fatalf("invalid initial state: %+v", initial)
	}
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing runtime")
	}
	t.Cleanup(release)
	disabled := read("disable", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false})
	if disabled.ConnectorIDs == nil || len(disabled.ConnectorIDs) != 0 || !disabled.ReloadPending {
		t.Fatalf("false write or pending state lost: %+v", disabled)
	}
	read("pending", map[string]any{"agentKey": "mock-agent"})
	release()
	if published := read("published", map[string]any{"agentKey": "mock-agent"}); published.ReloadPending {
		t.Fatalf("runtime did not publish: %+v", published)
	}
	enabled := read("enable", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": true})
	if !reflect.DeepEqual(enabled.ConnectorIDs, []string{"meeting"}) || enabled.ReloadPending {
		t.Fatalf("enable not persisted: %+v", enabled)
	}
	if def, ok := registry.AgentDefinition("mock-agent"); !ok || !slices.Contains(def.Connectors, connector.WebControlConnectorID) {
		t.Fatal("usage mutation removed preset runtime capability")
	}
}

func TestWSConnectorUsageRejectsInvalidRequestsAndPreservesErrors(t *testing.T) {
	f := connectorUsageFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	before, _ := registry.ReadEditableAgentSource("mock-agent")
	conn := connectorUsageSocket(t, f.server)
	for index, tc := range []struct {
		route   string
		payload any
		status  int
		code    string
	}{
		{"/api/connectors", map[string]any{"agentKey": "missing"}, 404, "not_found"},
		{"/api/connectors", map[string]any{"agentKey": "mock-agent", "locale": "en-US"}, 400, "invalid_request"},
		{"/api/connectors", json.RawMessage("null"), 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "missing"}, 404, "not_found"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting"}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "enabled": false}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "enabled": nil}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "Enabled": nil}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "Enabled": false}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": nil}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": "false"}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false, "activeConnectorIds": []string{}}, 400, "invalid_request"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": connector.WebControlConnectorID, "enabled": false}, 403, "preset_connector_readonly"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": connector.WebControlConnectorID, "enabled": true}, 403, "preset_connector_readonly"},
		{"/api/agents/connectors", map[string]any{"agentKey": "mock-agent", "connectorId": "mail", "enabled": true}, 400, "connector_selection_conflict"},
	} {
		id := fmt.Sprintf("invalid-%d", index)
		sendConnectorUsageRequest(t, conn, tc.route, id, tc.payload)
		frame := readConnectorUsageError(t, conn, id)
		if frame.Code != tc.status || frame.Type != tc.code {
			t.Fatalf("%s: %+v", id, frame)
		}
		if tc.code == "connector_selection_conflict" {
			encoded, _ := json.Marshal(frame.Data)
			if !strings.Contains(string(encoded), "conflictingConnectorIds") || !strings.Contains(string(encoded), "meeting") {
				t.Fatalf("conflict details lost: %s", encoded)
			}
		}
	}
	f.server.deps.CatalogReloader = &recordingServerCatalogReloader{err: errors.New("reload failed")}
	sendConnectorUsageRequest(t, conn, "/api/agents/connectors", "rollback", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false})
	if frame := readConnectorUsageError(t, conn, "rollback"); frame.Code != 500 {
		t.Fatalf("reload failure not reported: %+v", frame)
	}
	after, _ := registry.ReadEditableAgentSource("mock-agent")
	if before.Content != after.Content {
		t.Fatal("rejected or failed WS mutation changed source")
	}
}

func readConnectorUsageError(t *testing.T, conn *gws.Conn, id string) ws.ErrorFrame {
	t.Helper()
	raw := waitForWebSocketFrame(t, conn, func(raw []byte) bool {
		var frame ws.ErrorFrame
		return json.Unmarshal(raw, &frame) == nil && frame.ID == id
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Frame != ws.FrameError {
		t.Fatalf("expected error frame: %s (%v)", raw, err)
	}
	return frame
}

func TestWSAgentConnectorsRespectsDisabledInteraction(t *testing.T) {
	f := connectorUsageFixture(t)
	path := filepath.Join(f.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, []byte("\ninteractionConfig:\n  connectors: false\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn := connectorUsageSocket(t, f.server)
	sendConnectorUsageRequest(t, conn, "/api/agents/connectors", "disabled", map[string]any{"agentKey": "mock-agent", "connectorId": "meeting", "enabled": false})
	if frame := readConnectorUsageError(t, conn, "disabled"); frame.Code != 400 || frame.Type != "interaction_disabled" {
		t.Fatalf("disabled interaction accepted: %+v", frame)
	}
}
