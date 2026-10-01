package server

import (
	"bytes"
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

func writeProjectionPackage(t *testing.T, fixture testFixture, ids ...string) {
	t.Helper()
	root := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "office")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	members := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		members = append(members, map[string]string{"id": id})
	}
	manifest, _ := json.Marshal(map[string]any{"name": "office", "displayName": "Office 工具", "skills": members})
	if err := os.WriteFile(filepath.Join(root, "package.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		writeTestSkill(t, root, id)
	}
	if err := fixture.server.reloadAdminSkills(context.Background()); err != nil {
		t.Fatal(err)
	}

}

func TestSkillPackageProjectionTracksDiskAndPreservesOwnership(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "mock-skill", "center-extra")
	admin := getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", "/api/admin/skill-packages", nil)
	if len(admin) != 1 || admin[0].Status != "ready" || admin[0].MissingSkillIDs == nil {
		t.Fatalf("admin=%+v", admin)
	}
	chat := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills?agentKey=mock-agent", nil)
	if len(chat.Packages) != 1 || len(chat.Packages[0].Skills) != 2 || chat.Packages[0].DisplayName != "Office 工具" {
		t.Fatalf("chat=%+v", chat)
	}
	summaries := getAPIData[api.AdminSkillsResponse](t, f.server, "GET", "/api/admin/skills", nil).Skills
	for _, item := range summaries {
		want := ""
		if item.ID == "office/mock-skill" || item.ID == "office/center-extra" {
			want = "office"
		}
		if item.PackageID != want {
			t.Fatalf("summary=%+v", item)
		}
	}
	detail := getAPIData[api.AdminSkillDetailResponse](t, f.server, "GET", "/api/admin/skills/detail?id=office%2Fcenter-extra", nil)
	if detail.Skill.PackageID != "office" {
		t.Fatalf("detail=%+v", detail.Skill)
	}
	// Do not reload: external file deletion must be visible even while the catalog is stale.
	if err := os.Remove(filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "center-extra", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	admin = getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", "/api/admin/skill-packages", nil)
	if admin[0].Status != "incomplete" || len(admin[0].Skills) != 2 || !reflect.DeepEqual(admin[0].MissingSkillIDs, []string{"office/center-extra"}) {
		t.Fatalf("declared missing members must remain diagnosable: %+v", admin)
	}
	chat = getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil)
	if len(chat.Packages[0].Skills) != 1 {
		t.Fatalf("chat=%+v", chat)
	}
	data, err := os.ReadFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "package.json"))
	if err != nil || !bytes.Contains(data, []byte(`"skills"`)) {
		t.Fatalf("listing wrote member metadata: %s %v", data, err)
	}

}

func TestSkillPackageProjectionOnlySelectsGlobalSharedSkills(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	root := f.cfg.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	writeTestSkill(t, filepath.Join(root, "demo", "skills"), "service-guide")
	writeProjectionPackage(t, f, "center-extra")
	chat := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills?agentKey=mock-agent", nil)
	if len(chat.Packages) != 1 || len(chat.Packages[0].Skills) != 1 || chat.Packages[0].Skills[0].ID != "office/center-extra" {
		t.Fatalf("non-member exposed: %+v", chat.Packages)
	}

}

func TestSkillPackageProjectionHTTPWebSocketPinParity(t *testing.T) {
	f := newAgentSkillsTestFixture(t, true)
	writeProjectionPackage(t, f, "center-extra")
	expected := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil)
	pinned := getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(`{"id":"office/center-extra","pinned":true}`))
	if !reflect.DeepEqual(pinned.Packages, expected.Packages) {
		t.Fatalf("HTTP pin loses packages: %+v", pinned)
	}
	server := httptest.NewServer(f.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, tc := range []struct {
		id      string
		payload map[string]any
	}{
		{"list", map[string]any{}},
		{"pin", map[string]any{"id": "office/center-extra", "pinned": false}},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: tc.id, Payload: marshalPayload(tc.payload)}); err != nil {
			t.Fatal(err)
		}
		response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, tc.id)
		if !reflect.DeepEqual(response.Packages, expected.Packages) {
			t.Fatalf("WS %s: %+v", tc.id, response)
		}
	}
}
