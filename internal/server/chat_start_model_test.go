package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestChatStartModelOverridesReachProviderAndDoNotPersist(t *testing.T) {
	calls := make(chan map[string]any, 10)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		calls <- body
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		mockPath := filepath.Join(cfg.Paths.RegistriesDir, "models", "mock-model.yml")
		mock, err := os.ReadFile(mockPath)
		if err != nil {
			t.Fatal(err)
		}
		mock = []byte(strings.ReplaceAll(string(mock), "isReasoner: false", "isReasoner: true") + "\nreasoningEffortMapping:\n  LOW: LOW\n  MEDIUM: MEDIUM\n  HIGH: HIGH\n  XHIGH: HIGH\n  MAX: HIGH\n")
		if err := os.WriteFile(mockPath, mock, 0600); err != nil {
			t.Fatal(err)
		}
		data := []byte("key: alternate\nprovider: mock\nprotocol: OPENAI\nmodelId: alternate-provider-id\nisFunction: true\nisReasoner: true\nreasoningEffortMapping:\n  HIGH: HIGH\n  LOW: LOW\n  MEDIUM: MEDIUM\n  XHIGH: HIGH\n  MAX: HIGH\nreasoningEfforts:\n  - LOW\n  - HIGH\n")
		if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "alternate.yml"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	registerTestParentRun(t, fixture, "parent")
	defer fixture.runs.Finish("parent")
	var chatID string
	for _, tc := range []struct{ key, effort, wantModel, wantEffort string }{
		{"alternate", "HIGH", "alternate-provider-id", "high"},
		{"alternate", "NONE", "alternate-provider-id", ""},
		{"", "", "mock-model-id", ""},
		{"", "LOW", "mock-model-id", "low"},
	} {
		started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{AgentKey: "mock-agent", ChatID: chatID, Message: "task", ModelKey: tc.key, ReasoningEffort: tc.effort, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "model-test"}})
		if err != nil {
			t.Fatal(err)
		}
		waitRunTerminal(t, fixture.server, started.RunID)
		chatID = started.ChatID
		select {
		case body := <-calls:
			if body["model"] != tc.wantModel {
				t.Fatalf("model=%v want %s", body["model"], tc.wantModel)
			}
			got, _ := body["reasoning_effort"].(string)
			if got != tc.wantEffort {
				t.Fatalf("effort=%q want %q; body=%v", got, tc.wantEffort, body)
			}
		default:
			t.Fatal("no provider request")
		}
	}
}

func TestChatStartModelRejectionsStartNothing(t *testing.T) {
	for _, tc := range []struct{ name, key, effort string }{
		{"unknown model", "missing", ""}, {"non-chat model", "embed", ""}, {"invalid effort", "", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, _ := chatStartFixture(t, "default")
			if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.RegistriesDir, "models", "embed.yml"), []byte("key: embed\nprovider: mock\ntype: embedding\nmodelId: embed-id\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := fixture.modelRegistry.ReloadModels(); err != nil {
				t.Fatal(err)
			}
			req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", ModelKey: tc.key, ReasoningEffort: tc.effort, Origin: contracts.RunOrigin{RunID: "parent"}}
			_, err := fixture.server.StartRun(context.Background(), req)
			requireNoChatStarted(t, fixture, err, "invalid_request")
		})
	}
}

func TestChatStartACPModelOverridesDoNotFallBackToNative(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) { t.Error("native provider must not be called") }, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			workspace := setupCoderTestWorkspace(t, cfg, "acp-workspace")
			dir := filepath.Join(cfg.Paths.AgentsDir, "acp-agent")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			data := "key: acp-agent\nname: ACP Agent\nengine: acp\nruntimeConfig:\n  engineId: fixture/agent\n  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		},
	})
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	registerTestParentRun(t, fixture, "parent")
	defer fixture.runs.Finish("parent")
	for _, key := range []string{"Model-A", "Model-B"} {
		req := contracts.RunStartRequest{AgentKey: "acp-agent", Message: "task", ModelKey: key, Origin: contracts.RunOrigin{RunID: "parent"}}
		if _, err := fixture.server.PrepareRunStart(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		// Without a configured stdio host, the formal entry must fail closed.
		_, err := fixture.server.StartRun(context.Background(), req)
		requireNoChatStarted(t, fixture, err, "engine_unavailable")
	}
}
