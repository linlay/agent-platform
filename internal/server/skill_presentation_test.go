package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestSkillPresentationHTTPAndWSLocaleSwitch(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) { writeProviderSSE(t, w, `[DONE]`) }, testFixtureOptions{
		notifications: ws.NewHub(), setupRuntime: func(_ string, cfg *config.Config) {
			content := "---\nname: mock-skill\ndescription: Default description\ndisplayName: Default name\nmetadata:\n  version: \"1.2.3\"\n  revision: r18\n  i18n:\n    zh-CN:\n      displayName: 流程助手\n      description: 中文描述\n    en:\n      displayName: Workflow\n---\nBody"
			if err := os.WriteFile(filepath.Join(cfg.Paths.SkillsCenterDir, "mock-skill", "SKILL.md"), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		},
	})
	for _, locale := range []string{"zh-CN", "en-US", "zh-CN"} {
		want := "流程助手"
		if locale == "en-US" {
			want = "Workflow"
		}
		for _, path := range []string{"/api/skills", "/api/admin/skills", "/api/admin/skills/detail?id=mock-skill"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Locale", locale)
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"displayName":"`+want+`"`) || !strings.Contains(rec.Body.String(), `"version":"1.2.3"`) || strings.Contains(rec.Body.String(), `"i18n"`) {
				t.Fatalf("%s %s: %s", path, locale, rec.Body.String())
			}
		}
		req := httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=mock-agent", nil)
		req.Header.Set("X-Locale", locale)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		var response api.ApiResponse[api.AgentDetailResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || len(response.Data.Skills) != 1 || response.Data.Skills[0] != "mock-skill" || strings.Contains(rec.Body.String(), `"displayName"`) {
			t.Fatalf("Agent associations must be locale-independent IDs: %s (%v)", rec.Body.String(), err)
		}
	}
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, locale := range []string{"zh-CN", "en-US"} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/locale", ID: "locale-" + locale, Payload: json.RawMessage(`{"locale":"` + locale + `"}`)}); err != nil {
			t.Fatal(err)
		}
		waitForWebSocketResponseData[map[string]any](t, conn, "locale-"+locale)
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: "skills-" + locale, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "skills-"+locale)
		found := false
		for _, skill := range response.Skills {
			if skill.ID == "mock-skill" {
				found = true
				want := "流程助手"
				if locale == "en-US" {
					want = "Workflow"
				}
				if skill.DisplayName != want || skill.Name != "" || skill.Revision != "r18" {
					t.Fatalf("%s: %#v", locale, skill)
				}
			}
		}
		if !found {
			t.Fatal("missing skill")
		}
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/agent", ID: "agent-" + locale, Payload: json.RawMessage(`{"agentKey":"mock-agent"}`)}); err != nil {
			t.Fatal(err)
		}
		agent := waitForWebSocketResponseData[api.AgentDetailResponse](t, conn, "agent-"+locale)
		if len(agent.Skills) != 1 || agent.Skills[0] != "mock-skill" {
			t.Fatalf("Agent WS associations changed with locale: %+v", agent.Skills)
		}
	}
	definition, _ := fixture.server.deps.Registry.SkillDefinition("mock-skill")
	if definition.Name != "mock-skill" || definition.Description != "Default description" {
		t.Fatalf("catalog mutated: %#v", definition)
	}
}

func TestSkillPublicIdentityUsesDisplayNameOnly(t *testing.T) {
	for _, value := range []any{
		api.AgentSkillsResponse{Skills: []api.AgentSkillResponse{{ID: "stable-id", Name: "Friendly Name"}}},
		[]api.SkillSummary{{ID: "stable-id", Name: "Friendly Name"}},
		[]api.AdminSkillSummary{{ID: "stable-id", Name: "Friendly Name"}},
		api.AdminSkillDetailResponse{Skill: api.AdminSkillSummary{ID: "stable-id", Name: "Friendly Name"}},
		api.AdminAgentDetailResponse{PrivateSkills: []api.AdminAgentPrivateSkill{{ID: "stable-id", Name: "Friendly Name"}}},
		[]catalog.ConnectorSkillSummary{{ID: "stable-id", Name: "Friendly Name"}},
		catalog.ConnectorSkillDetail{Skill: catalog.ConnectorSkillSummary{ID: "stable-id", Name: "Friendly Name"}},
	} {
		encoded, err := json.Marshal(localizeSkillResponse("en-US", value))
		if err != nil {
			t.Fatal(err)
		}
		var parsed any
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			t.Fatal(err)
		}
		var check func(any)
		found := false
		check = func(node any) {
			switch node := node.(type) {
			case map[string]any:
				if node["id"] == "stable-id" {
					found = true
					if _, exists := node["name"]; exists {
						t.Fatalf("raw name leaked: %s", encoded)
					}
					if node["displayName"] != "Friendly Name" {
						t.Fatalf("missing fallback: %s", encoded)
					}
				}
				for _, child := range node {
					check(child)
				}
			case []any:
				for _, child := range node {
					check(child)
				}
			}
		}
		check(parsed)
		if !found {
			t.Fatalf("missing key: %s", encoded)
		}
	}
}
