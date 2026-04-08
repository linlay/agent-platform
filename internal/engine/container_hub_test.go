package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform-runner-go/internal/config"
)

func TestContainerHubClientGetEnvironmentAgentPromptParsesSnakeCaseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments/toolbox/agent-prompt" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environment_name":"toolbox","has_prompt":true,"prompt":"prompt-body","updated_at":"2026-04-02T14:20:53Z"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	result, err := client.GetEnvironmentAgentPrompt("toolbox")
	if err != nil {
		t.Fatalf("get environment agent prompt: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok result, got %#v", result)
	}
	if result.EnvironmentName != "toolbox" || !result.HasPrompt || result.Prompt != "prompt-body" || result.UpdatedAt != "2026-04-02T14:20:53Z" {
		t.Fatalf("unexpected parsed result: %#v", result)
	}
}
