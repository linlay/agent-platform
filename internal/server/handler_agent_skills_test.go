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

func TestAgentSkillsReturnsConfiguredAndCenterUnion(t *testing.T) {
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
	for _, forbidden := range []string{`"items"`, `"meta"`, `"mustUseSource"`} {
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
		{path: "/api/skills", wantStatus: http.StatusBadRequest, wantCode: "agent_key_required"},
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
		Payload: ws.MarshalPayload(map[string]any{"agentKey": "mock-agent"}),
	}); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}

	response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, "agent_skills")
	assertAgentSkillsResponse(t, response)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/skills",
		ID:      "agent_skills_missing_key",
		Payload: ws.MarshalPayload(map[string]any{}),
	}); err != nil {
		t.Fatalf("write invalid websocket request: %v", err)
	}
	var errorFrame ws.ErrorFrame
	if err := conn.ReadJSON(&errorFrame); err != nil {
		t.Fatalf("read websocket error: %v", err)
	}
	if errorFrame.Frame != ws.FrameError || errorFrame.ID != "agent_skills_missing_key" ||
		errorFrame.Type != "agent_key_required" || errorFrame.Code != http.StatusBadRequest {
		t.Fatalf("unexpected websocket error: %#v", errorFrame)
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
			updated := strings.Replace(string(content), "    - mock-skill", "    - mock-skill\n    - private-skill", 1)
			if updated == string(content) {
				t.Fatal("expected mock-skill declaration in agent config")
			}
			if err := os.WriteFile(agentPath, []byte(updated), 0o644); err != nil {
				t.Fatalf("write agent config: %v", err)
			}
			writeTestSkill(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "skills"), "private-skill")
			writeTestSkill(t, cfg.Paths.SkillsCenterDir, "center-extra")
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
	if response.Skills == nil {
		t.Fatal("skills must be a non-null array")
	}
	if len(response.Skills) != 3 {
		t.Fatalf("expected 3 skills, got %#v", response.Skills)
	}

	wantKeys := []string{"mock-skill", "private-skill", "center-extra"}
	wantConfigured := []bool{true, true, false}
	for index := range wantKeys {
		got := response.Skills[index]
		if got.Key != wantKeys[index] || got.AgentHasSkill != wantConfigured[index] {
			t.Fatalf("skills[%d] = %#v, want key=%q agentHasSkill=%t", index, got, wantKeys[index], wantConfigured[index])
		}
		if strings.TrimSpace(got.Name) == "" {
			t.Fatalf("skills[%d] must include name: %#v", index, got)
		}
	}
}
