package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestConnectorOrderHTTPUserIsolationAndValidation(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	writeMCPConnectorForTest(t, fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "demo")
	fixture.server.deps.Config.Paths.BuiltinConnectorsDir = t.TempDir()
	writeMCPConnectorForTest(t, fixture.server.deps.Config.Paths.BuiltinConnectorsDir, "builtin.demo")
	request := func(user, method, body string, status int) api.ConnectorOrderResponse {
		t.Helper()
		req := httptest.NewRequest(method, "/api/connectors/order?userKey=someone-else", strings.NewReader(body))
		req = req.WithContext(WithPrincipal(context.Background(), &Principal{Subject: user}))
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s %s: %d %s", method, body, recorder.Code, recorder.Body.String())
		}
		var response api.ApiResponse[api.ConnectorOrderResponse]
		if status == 200 {
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return response.Data
	}
	request("alice", "PUT", `{"key":"demo","pinned":true}`, 200)
	state := request("alice", "GET", "", 200)
	if !reflect.DeepEqual(state.Order, []string{"demo"}) || state.UpdatedAt == nil {
		t.Fatalf("order: %#v", state)
	}
	if other := request("bob", "GET", "", 200); len(other.Order) != 0 {
		t.Fatalf("cross-user leak: %#v", other)
	}
	for _, body := range []string{`{}`, `{"key":"demo"}`, `{"key":"../escape","pinned":true}`, `{"key":"demo","pinned":"yes"}`} {
		request("alice", "PUT", body, 400)
	}
	request("alice", "PUT", `{"key":"missing","pinned":true}`, 404)
	request("alice", "PUT", `{"key":"builtin.demo","pinned":true}`, 200)
	// Preference writes never change a read-only built-in package or the skill order.
	if skills, err := fixture.server.skillOrder.Read("user:alice"); err != nil || len(skills.Order) != 0 {
		t.Fatalf("skill order changed: %#v %v", skills, err)
	}
	if err := os.RemoveAll(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "demo")); err != nil {
		t.Fatal(err)
	}
	request("alice", "PUT", `{"key":"demo","pinned":false}`, 200)
	if got := request("alice", "GET", "", 200); !reflect.DeepEqual(got.Order, []string{"builtin.demo"}) {
		t.Fatalf("unpin: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "order.json")); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorOrderWebSocketUsesSameStore(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, true)
	writeMCPConnectorForTest(t, fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "demo")
	fixture.server.deps.Config.Paths.BuiltinConnectorsDir = t.TempDir()
	writeMCPConnectorForTest(t, fixture.server.deps.Config.Paths.BuiltinConnectorsDir, "builtin.demo")
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/connectors/order", ID: "pin", Payload: marshalPayload(map[string]any{"key": "demo", "pinned": true})}); err != nil {
		t.Fatal(err)
	}
	response := waitForWebSocketResponseData[api.ConnectorOrderResponse](t, conn, "pin")
	if !reflect.DeepEqual(response.Order, []string{"demo"}) {
		t.Fatalf("pin: %#v", response)
	}
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/connectors/order", nil))
	var httpResponse api.ApiResponse[api.ConnectorOrderResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &httpResponse); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, httpResponse.Data) {
		t.Fatalf("HTTP/WS mismatch: %#v %#v", response, httpResponse.Data)
	}
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/connectors/order", ID: "read", Payload: marshalPayload(map[string]any{})}); err != nil {
		t.Fatal(err)
	}
	read := waitForWebSocketResponseData[api.ConnectorOrderResponse](t, conn, "read")
	if !reflect.DeepEqual(read, response) {
		t.Fatalf("WS read: %#v", read)
	}
}

func TestConnectorOrderRequiresServerIdentity(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	fixture.server.deps.Config.Auth.Enabled = true
	if _, err := fixture.server.readConnectorOrder(context.Background()); err == nil {
		t.Fatal("accepted missing identity")
	}
	pinned := true
	if _, err := fixture.server.updateConnectorOrder(context.Background(), api.UpdateConnectorOrderRequest{Key: "demo", Pinned: &pinned}); err == nil {
		t.Fatal("accepted missing identity")
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "order.json")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized request touched order file: %v", err)
	}
}

func TestConnectorCatalogRemainsReadableAfterPinning(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	root := fixture.cfg.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	catalog := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/connectors", nil))
		if rec.Code != 200 {
			t.Fatalf("connector list: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	before := catalog()
	for _, pinned := range []string{"true", "false"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/connectors/order", strings.NewReader(`{"key":"demo","pinned":`+pinned+`}`)))
		if rec.Code != 200 {
			t.Fatalf("pin update: %d %s", rec.Code, rec.Body.String())
		}
		if after := catalog(); after != before {
			t.Fatalf("pin preference changed connector catalog: %s", after)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "order.json")); err != nil {
		t.Fatal(err)
	}
}
