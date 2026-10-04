package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestSkillConnectionLocale(t *testing.T) {
	f := newAgentSkillsTestFixture(t, true)
	writeProjectionPackage(t, f, "center-extra")
	content := `{"name":"office","skills":[{"id":"center-extra"}],"metadata":{"i18n":{"zh-CN":{"displayName":"办公包"},"en":{"displayName":"Office Suite"}}}}`
	if err := os.WriteFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "package.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.server.reloadAdminSkills(context.Background()); err != nil {
		t.Fatal(err)
	}
	httpPin := getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills?locale=zh-CN", []byte(`{"id":"office","pinned":true}`))
	if len(httpPin.Pinned) != 1 || httpPin.Pinned[0] != "office" || len(httpPin.Packages) != 1 || httpPin.Packages[0].DisplayName != "办公包" {
		t.Fatalf("HTTP package pin locale: %+v", httpPin)
	}
	server := httptest.NewServer(f.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/locale", ID: "base", Payload: marshalPayload(map[string]any{"locale": "en-US"})}); err != nil {
		t.Fatal(err)
	}
	waitForWebSocketResponseData[map[string]any](t, conn, "base")
	for _, tc := range []struct {
		id      string
		payload map[string]any
		want    string
	}{
		{"zh-list", map[string]any{"locale": "zh-CN"}, "办公包"},
		{"en-list", map[string]any{"locale": "en-US"}, "Office Suite"},
		{"zh-pin", map[string]any{"locale": "zh-CN", "id": "office", "pinned": true}, "办公包"},
		{"unchanged", map[string]any{}, "办公包"},
	} {
		if locale, ok := tc.payload["locale"]; ok {
			if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/locale", ID: tc.id + "-locale", Payload: marshalPayload(map[string]any{"locale": locale})}); err != nil {
				t.Fatal(err)
			}
			waitForWebSocketResponseData[map[string]any](t, conn, tc.id+"-locale")
			delete(tc.payload, "locale")
		}
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: tc.id, Payload: marshalPayload(tc.payload)}); err != nil {
			t.Fatal(err)
		}
		r := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, tc.id)
		if len(r.Pinned) != 1 || r.Pinned[0] != "office" || len(r.Packages) != 1 || r.Packages[0].DisplayName != tc.want {
			t.Fatalf("%s: %+v", tc.id, r.Packages)
		}
	}
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/locale", ID: "bad-locale", Payload: marshalPayload(map[string]any{"locale": "invalid-locale"})}); err != nil {
		t.Fatal(err)
	}
	for {
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame["id"] != "bad-locale" {
			continue
		}
		data, _ := frame["data"].(map[string]any)
		if frame["code"] != float64(400) && data["status"] != float64(400) {
			t.Fatalf("invalid locale not rejected: %+v", frame)
		}
		break
	}
}
