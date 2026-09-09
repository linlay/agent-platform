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

func TestSkillOrderHTTPUserIsolationAndValidation(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	request := func(user, method, body string, status int) api.SkillOrderResponse {
		t.Helper()
		req := httptest.NewRequest(method, "/api/skills/order?userKey=someone-else", strings.NewReader(body))
		req = req.WithContext(WithPrincipal(context.Background(), &Principal{Subject: user}))
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s %s: %d %s", method, body, recorder.Code, recorder.Body.String())
		}
		var response api.ApiResponse[api.SkillOrderResponse]
		if status == 200 {
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return response.Data
	}
	request("alice", "PUT", `{"key":"center-extra","pinned":true}`, 200)
	state := request("alice", "GET", "", 200)
	if !reflect.DeepEqual(state.Order, []string{"center-extra"}) || state.UpdatedAt == nil {
		t.Fatalf("order: %#v", state)
	}
	if other := request("bob", "GET", "", 200); len(other.Order) != 0 {
		t.Fatalf("cross-user leak: %#v", other)
	}
	for _, body := range []string{`{}`, `{"key":"mock-skill"}`, `{"key":"../escape","pinned":true}`, `{"key":"mock-skill","pinned":"yes"}`} {
		request("alice", "PUT", body, 400)
	}
	request("alice", "PUT", `{"key":"missing","pinned":true}`, 404)
	request("alice", "PUT", `{"key":"private-skill","pinned":true}`, 200)
	request("alice", "PUT", `{"key":"center-extra","pinned":false}`, 200)
	if got := request("alice", "GET", "", 200); !reflect.DeepEqual(got.Order, []string{"private-skill"}) {
		t.Fatalf("unpin: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "order.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillOrderWebSocketUsesSameStore(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, true)
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills/order", ID: "pin", Payload: marshalPayload(map[string]any{"key": "center-extra", "pinned": true})}); err != nil {
		t.Fatal(err)
	}
	response := waitForWebSocketResponseData[api.SkillOrderResponse](t, conn, "pin")
	if !reflect.DeepEqual(response.Order, []string{"center-extra"}) {
		t.Fatalf("pin: %#v", response)
	}
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/skills/order", nil))
	var httpResponse api.ApiResponse[api.SkillOrderResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &httpResponse); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, httpResponse.Data) {
		t.Fatalf("HTTP/WS mismatch: %#v %#v", response, httpResponse.Data)
	}
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills/order", ID: "read", Payload: marshalPayload(map[string]any{})}); err != nil {
		t.Fatal(err)
	}
	read := waitForWebSocketResponseData[api.SkillOrderResponse](t, conn, "read")
	if !reflect.DeepEqual(read, response) {
		t.Fatalf("WS read: %#v", read)
	}
}

func TestSkillOrderAllowsInvalidInstalledCenterSkill(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	path := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "broken-skill")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: [broken\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/skills/order", strings.NewReader(`{"key":"broken-skill","pinned":true}`)))
	if rec.Code != 200 {
		t.Fatalf("pin installed invalid skill: %d %s", rec.Code, rec.Body.String())
	}
}
