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

func TestAgentSkillPinsHTTPUserIsolationAndValidation(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	request := func(user, method, body string, status int) api.AgentSkillsResponse {
		t.Helper()
		req := httptest.NewRequest(method, "/api/skills?userKey=someone-else", strings.NewReader(body))
		req = req.WithContext(WithPrincipal(context.Background(), &Principal{Subject: user}))
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s %s: %d %s", method, body, recorder.Code, recorder.Body.String())
		}
		var response api.ApiResponse[api.AgentSkillsResponse]
		if status == 200 {
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return response.Data
	}
	request("alice", "PUT", `{"key":"center-extra","pinned":true}`, 200)
	state := request("alice", "GET", "", 200)
	if !reflect.DeepEqual(state.Pinned, []string{"center-extra"}) {
		t.Fatalf("order: %#v", state)
	}
	if other := request("bob", "GET", "", 200); len(other.Pinned) != 0 {
		t.Fatalf("cross-user leak: %#v", other)
	}
	for _, body := range []string{`{}`, `{"key":"mock-skill"}`, `{"key":"../escape","pinned":true}`, `{"key":"mock-skill","pinned":"yes"}`} {
		request("alice", "PUT", body, 400)
	}
	request("alice", "PUT", `{"key":"missing","pinned":true}`, 404)
	request("alice", "PUT", `{"key":"private-skill","pinned":true}`, 200)
	request("alice", "PUT", `{"key":"center-extra","pinned":false}`, 200)
	if got := request("alice", "GET", "", 200); !reflect.DeepEqual(got.Pinned, []string{"private-skill"}) {
		t.Fatalf("unpin: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "order.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSkillPinsWebSocketUsesSameStore(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, true)
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: "pin", Payload: marshalPayload(map[string]any{"key": "center-extra", "pinned": true})}); err != nil {
		t.Fatal(err)
	}
	response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "pin")
	if !reflect.DeepEqual(response.Pinned, []string{"center-extra"}) {
		t.Fatalf("pin: %#v", response)
	}
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/skills", nil))
	var httpResponse api.ApiResponse[api.AgentSkillsResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &httpResponse); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Pinned, httpResponse.Data.Pinned) {
		t.Fatalf("HTTP/WS mismatch: %#v %#v", response, httpResponse.Data)
	}
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: "read", Payload: marshalPayload(map[string]any{})}); err != nil {
		t.Fatal(err)
	}
	read := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "read")
	if !reflect.DeepEqual(read, httpResponse.Data) {
		t.Fatalf("WS read: %#v", read)
	}
}

func TestAgentSkillPinsAllowsInvalidInstalledCenterSkill(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)
	path := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "broken-skill")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: [broken\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/skills", strings.NewReader(`{"key":"broken-skill","pinned":true}`)))
	if rec.Code != 200 {
		t.Fatalf("pin installed invalid skill: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentSkillPinsMethodsValidationAndGlobalScope(t *testing.T) {
	f := newAgentSkillsTestFixture(t, true)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/skills", "", 405},
		{"GET", "/api/skills/order", "", 404},
		{"PUT", "/api/skills", `{"key":"","pinned":true}`, 400},
		{"PUT", "/api/skills", `{"key":"mock-skill","pinned":null}`, 400},
		{"PUT", "/api/skills", `{`, 400},
		{"PUT", "/api/skills", `{"key":"missing","pinned":false}`, 200},
		{"PUT", "/api/skills?agentKey=missing", `{"key":"center-extra","pinned":true,"agentKey":"missing"}`, 200},
		{"PUT", "/api/skills", `{"key":"mock-skill","pinned":true}`, 200},
		{"PUT", "/api/skills", `{"key":"center-extra","pinned":true}`, 200},
	} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if rec.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, rec.Code, rec.Body.String())
		}
		if tc.status == 405 && rec.Header().Get("Allow") != "GET, PUT" {
			t.Fatal("missing Allow")
		}
	}
	for _, path := range []string{"/api/skills", "/api/skills?agentKey=mock-agent"} {
		got := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", path, nil)
		if !reflect.DeepEqual(got.Pinned, []string{"mock-skill", "center-extra"}) {
			t.Fatalf("pins: %#v", got)
		}
	}
	f.server.deps.Config.Auth.Enabled = true
	for _, method := range []string{"GET", "PUT"} {
		rec := httptest.NewRecorder()
		f.server.handleAgentSkills(rec, httptest.NewRequest(method, "/api/skills", strings.NewReader(`{"key":"mock-skill","pinned":true}`)))
		if rec.Code != 401 {
			t.Fatalf("auth %s: %d", method, rec.Code)
		}
	}
}

func TestAgentSkillPinsWebSocketRejectsIncompleteWritesAndRemovedRoute(t *testing.T) {
	f := newAgentSkillsTestFixture(t, true)
	server := httptest.NewServer(f.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, body := range []string{`{"key":null}`, `{"key":"mock-skill"}`, `{"pinned":true}`, `{"key":"","pinned":false}`, `{"key":"mock-skill","pinned":null}`, `{"pinned":"yes"}`} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: "invalid", Payload: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
		var frame ws.ErrorFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Frame != ws.FrameError || frame.Code != 400 {
			t.Fatalf("%s: %#v", body, frame)
		}
	}
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills/order", ID: "removed"}); err != nil {
		t.Fatal(err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Frame != ws.FrameError || frame.Code != 400 || !strings.Contains(frame.Msg, "unknown type") {
		t.Fatalf("removed route: %#v", frame)
	}
}
