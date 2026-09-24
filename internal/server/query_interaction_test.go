package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/interaction"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInteractionAdmission(t *testing.T) {
	c := interaction.Defaults("KBASE")
	tests := []struct {
		name   string
		req    api.QueryRequest
		denied bool
	}{
		{"default", api.QueryRequest{AccessLevel: "default"}, false},
		{"model", api.QueryRequest{Model: &api.QueryModelOptions{Key: "model"}}, true},
		{"reasoning", api.QueryRequest{Model: &api.QueryModelOptions{ReasoningEffort: "HIGH"}}, true},
		{"empty model", api.QueryRequest{Model: &api.QueryModelOptions{}}, false},
		{"access", api.QueryRequest{AccessLevel: "full_access"}, true},
		{"chat", api.QueryRequest{References: []api.Reference{{Type: "CHAT"}}}, true},
		{"file", api.QueryRequest{References: []api.Reference{{Type: "file"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateInteractionInput(c, tt.req); (err != nil) != tt.denied {
				t.Fatalf("error=%v denied=%v", err, tt.denied)
			}
		})
	}
	c.MustUseSkills = false
	if err := validateInteractionInput(c, api.QueryRequest{MustUseSkills: []string{"skill"}}); err == nil {
		t.Fatal("accepted disabled skills")
	}
	c.Attachment.LocalFiles = false
	for _, kind := range []string{"file", "image", ""} {
		if err := validateInteractionInput(c, api.QueryRequest{References: []api.Reference{{Type: kind}}}); err == nil {
			t.Fatalf("accepted disabled file %q", kind)
		}
	}
}

func TestAgentInteractionDetailAndQueryHTTP(t *testing.T) {
	fixture := newTestFixture(t)
	agent := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "interaction-agent", "definition": map[string]any{
			"key": "interaction-agent", "name": "Interaction", "mode": "REACT", "modelConfig": map[string]any{"modelKey": "mock-model"},
			"interactionConfig": map[string]any{"model": false, "accessLevel": false, "mustUseSkills": false, "connectors": false, "attachment": map[string]any{"localFiles": false, "chatRecords": false}},
		},
	})
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey="+agent.Key, nil))
	var detail api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || detail.Data.InteractionConfig.Model || detail.Data.InteractionConfig.AccessLevel || detail.Data.InteractionConfig.MustUseSkills || detail.Data.InteractionConfig.Connectors || detail.Data.InteractionConfig.Attachment.LocalFiles || detail.Data.InteractionConfig.Attachment.ChatRecords {
		t.Fatalf("detail: %s", rec.Body.String())
	}
	for _, extra := range []map[string]any{
		{"model": map[string]any{"key": "mock-model"}}, {"accessLevel": "full_access"}, {"mustUseSkills": []string{"skill"}}, {"references": []map[string]any{{"type": "file", "url": "file.txt"}}}, {"references": []map[string]any{{"type": "chat", "id": "other-chat"}}},
	} {
		extra["agentKey"] = agent.Key
		extra["message"] = "hello"
		body, _ := json.Marshal(extra)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "interaction_disabled") {
			t.Fatalf("query %#v: %d %s", extra, rec.Code, rec.Body.String())
		}
	}
}

func TestReactModelConfigCanBeSelected(t *testing.T) {
	fixture := newTestFixture(t)
	result := postAgentJSON[api.AgentModelConfigResponse](t, fixture.server, "/api/agent/model-config", map[string]any{"agentKey": "mock-agent", "modelKey": "mock-model", "reasoningEffort": "HIGH"})
	if result.ModelKey != "mock-model" {
		t.Fatalf("model config: %+v", result)
	}
}

func TestRestoredInteractionPolicy(t *testing.T) {
	s := newTestFixture(t).server
	frozen := interaction.Defaults("CODER")
	frozen.AccessLevel = false
	if err := s.runInteractionPolicies().Bind("frozen", frozen); err != nil {
		t.Fatal(err)
	}
	legacy := &chat.QueryLine{Query: map[string]any{"interactionConfig": map[string]any{"model": false}}}
	restored, err := s.restoredInteractionPolicy("frozen", "REACT", legacy)
	if err != nil || restored == nil || *restored != frozen {
		t.Fatalf("private snapshot: %+v %v", restored, err)
	}
	restored, err = s.restoredInteractionPolicy("legacy", "REACT", legacy)
	if err != nil || restored == nil || restored.Model || !restored.AccessLevel {
		t.Fatalf("legacy snapshot: %+v %v", restored, err)
	}
	restored, err = s.restoredInteractionPolicy("missing", "REACT", nil)
	if err != nil || restored != nil {
		t.Fatalf("missing snapshot: %+v %v", restored, err)
	}
}
