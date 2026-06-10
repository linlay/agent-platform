package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/i18n"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestHTTPErrorMessageUsesRequestedLocale(t *testing.T) {
	fixture := newTestFixture(t)
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/agent?agentKey=missing", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Locale", i18n.LocaleZhCN)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	var body api.ApiResponse[map[string]any]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound || body.Msg != "智能体不存在" {
		t.Fatalf("unexpected localized HTTP response status=%d body=%#v", resp.StatusCode, body)
	}
}

func TestWSLocaleRouteSwitchesCurrentConnection(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?deviceId=device-locale", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/locale",
		ID:      "locale_1",
		Payload: json.RawMessage(`{"locale":"zh-CN"}`),
	}); err != nil {
		t.Fatalf("write locale request: %v", err)
	}
	data := waitForWebSocketResponseData[map[string]any](t, conn, "locale_1")
	if data["locale"] != i18n.LocaleZhCN || data["scope"] != "connection" || data["deviceId"] != "device-locale" {
		t.Fatalf("unexpected locale response %#v", data)
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/agent",
		ID:      "agent_1",
		Payload: json.RawMessage(`{"agentKey":"missing"}`),
	}); err != nil {
		t.Fatalf("write agent request: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(raw []byte) bool {
		var frame ws.ErrorFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameError && frame.ID == "agent_1"
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket error: %v", err)
	}
	if frame.Type != "not_found" || frame.Msg != "智能体不存在" {
		t.Fatalf("unexpected localized websocket error %#v", frame)
	}
}

func TestWSLocaleRouteRejectsUnsupportedLocale(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/locale",
		ID:      "locale_bad",
		Payload: json.RawMessage(`{"locale":"fr"}`),
	}); err != nil {
		t.Fatalf("write locale request: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(raw []byte) bool {
		var frame ws.ErrorFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameError && frame.ID == "locale_bad"
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket error: %v", err)
	}
	if frame.Type != "invalid_locale" || frame.Msg != "invalid locale" {
		t.Fatalf("unexpected invalid locale error %#v", frame)
	}
}
