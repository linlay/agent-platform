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
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentSkillsReturnsGlobalCatalogWithConfiguredFlags(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, false)

	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/skills?agentKey=mock-agent", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/skills expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope api.ApiResponse[api.AgentSkillsResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertAgentSkillsResponse(t, envelope.Data)

	body := recorder.Body.String()
	for _, forbidden := range []string{`"items"`, `"meta"`, `"mustUseSource"`, `"agentHasSkill"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response must not contain %s: %s", forbidden, body)
		}
	}
}

func TestAgentSkillsValidatesAgentKey(t *testing.T) {
	fixture := newTestFixture(t)

	for _, testCase := range []struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		{path: "/api/skills?agentKey=missing-agent", wantStatus: http.StatusNotFound, wantCode: "agent_not_found"},
	} {
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, testCase.path, nil))
		if recorder.Code != testCase.wantStatus {
			t.Fatalf("GET %s expected %d, got %d: %s", testCase.path, testCase.wantStatus, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"code":"`+testCase.wantCode+`"`) {
			t.Fatalf("GET %s expected error code %q: %s", testCase.path, testCase.wantCode, recorder.Body.String())
		}
	}
}

func TestAgentSkillsWebSocketReturnsSameData(t *testing.T) {
	fixture := newAgentSkillsTestFixture(t, true)
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/skills",
		ID:      "agent_skills",
		Payload: marshalPayload(map[string]any{"agentKey": "mock-agent"}),
	}); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}

	response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "agent_skills")
	assertAgentSkillsResponse(t, response)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/skills",
		ID:      "agent_skills_missing_key",
		Payload: marshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write invalid websocket request: %v", err)
	}
	global := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "agent_skills_missing_key")
	if global.AgentKey != "" || global.Skills == nil || global.Pinned == nil {
		t.Fatalf("global: %#v", global)
	}
	for _, skill := range global.Skills {
		if skill.Configured {
			t.Fatal("global skill must not be configured")
		}
	}

}

func newAgentSkillsTestFixture(t *testing.T, withWebSocket bool) testFixture {
	t.Helper()
	options := testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			content, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatalf("read agent config: %v", err)
			}
			updated := strings.Replace(string(content), "    - mock-skill", "    - mock-skill\n    - private-skill\n    - private-only", 1)
			if updated == string(content) {
				t.Fatal("expected mock-skill declaration in agent config")
			}
			if err := os.WriteFile(agentPath, []byte(updated), 0o644); err != nil {
				t.Fatalf("write agent config: %v", err)
			}
			writeTestSkill(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "skills"), "private-skill")
			writeTestSkill(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "skills"), "private-only")
			writeTestSkill(t, cfg.Paths.SkillsCenterDir, "center-extra")
			writeTestSkill(t, cfg.Paths.SkillsCenterDir, "private-skill")
			writeAgentSkillIconPNG(t, cfg.Paths.SkillsCenterDir, "mock-skill", 30)
			writeAgentSkillIconPNG(t, cfg.Paths.SkillsCenterDir, "center-extra", 60)
			writeAgentSkillIconPNG(t, cfg.Paths.SkillsCenterDir, "private-skill", 90)
			writeAgentSkillIconPNG(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "skills"), "private-skill", 180)
		},
	}
	if withWebSocket {
		options.notifications = ws.NewHub()
	}
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, options)
}

func assertAgentSkillsResponse(t *testing.T, response api.AgentSkillsResponse) {
	t.Helper()
	if response.AgentKey != "mock-agent" {
		t.Fatalf("agentKey = %q", response.AgentKey)
	}
	if response.Pinned == nil {
		t.Fatal("pinned must be a non-null array")
	}
	if response.Skills == nil {
		t.Fatal("skills must be a non-null array")
	}
	if len(response.Skills) != 3 {
		t.Fatalf("expected 3 skills, got %#v", response.Skills)
	}

	wantKeys := []string{"center-extra", "mock-skill", "private-skill"}
	wantConfigured := []bool{false, true, true}
	for index := range wantKeys {
		got := response.Skills[index]
		if got.Key != wantKeys[index] || got.Configured != wantConfigured[index] {
			t.Fatalf("skills[%d] = %#v, want key=%q configured=%t", index, got, wantKeys[index], wantConfigured[index])
		}
		if !strings.HasPrefix(got.Icon, "/api/skills/icon?key=") {
			t.Fatalf("skills[%d] missing icon: %#v", index, got)
		}
		if strings.TrimSpace(got.Name) == "" {
			t.Fatalf("skills[%d] must include name: %#v", index, got)
		}
	}
}

func TestAgentSkillsOptionalContextDoesNotFilterCatalog(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	global := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil)
	scoped := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills?agentKey=mock-agent", nil)
	if global.AgentKey != "" || len(global.Skills) != len(scoped.Skills) || global.Pinned == nil {
		t.Fatalf("global: %#v", global)
	}
	for i, skill := range global.Skills {
		if skill.Configured {
			t.Fatal("unscoped skill configured")
		}
		scoped.Skills[i].Configured = false
		if skill != scoped.Skills[i] {
			t.Fatalf("catalog differs: %#v %#v", skill, scoped.Skills[i])
		}
	}
}
