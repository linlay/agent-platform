package server

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
)

func TestQueryPromptLocaleAndHistoricalReplay(t *testing.T) {
	prompts := make(chan string, 4)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, message := range body.Messages {
			if message.Role == "system" {
				prompts <- message.Content
				break
			}
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		body := "key: mock-agent\nname: Mock Agent\nmode: GENERAL\nmodelConfig:\n  modelKey: mock-model\ncontextConfig:\n  tags:\n    - system\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	var previous string
	for i, locale := range []string{"en-US", "zh-CN", "zh-CN", ""} {
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"locale-history","message":"hello"}`))
		req.Header.Set("X-Locale", locale)
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"type":"run.error"`) {
			t.Fatalf("query failed: %s", recorder.Body.String())
		}
		var prompt string
		select {
		case prompt = <-prompts:
		default:
			t.Fatal("no provider system prompt")
		}
		want := "zh-CN"
		if i == 0 {
			want = "en"
		}
		if !strings.Contains(prompt, "language: "+want+"\n") {
			t.Fatalf("expected %s: %s", want, prompt)
		}
		if i >= 2 && prompt != previous {
			t.Fatal("unchanged language and context changed system prompt")
		}
		previous = prompt
	}
	store := fixture.chats.(*chat.FileStore)
	lines, err := readServerTestJSONLines(store, "locale-history")
	if err != nil {
		t.Fatal(err)
	}
	var runIDs []string
	for _, line := range lines {
		if line["_type"] != "query" {
			continue
		}
		query, _ := line["query"].(map[string]any)
		if query["role"] == "user" {
			if id, ok := line["runId"].(string); ok {
				runIDs = append(runIDs, id)
			}
		}
	}
	if len(runIDs) != 4 {
		t.Fatalf("Run count: %v", runIDs)
	}
	for i, id := range runIDs {
		result, err := store.BuildLLMChatFromJSONL("locale-history", chat.LLMChatBuildOptions{RunID: id, Seq: 1})
		if err != nil {
			t.Fatal(err)
		}
		want := "zh-CN"
		if i == 0 {
			want = "en"
		}
		if len(result.Messages) == 0 || !strings.Contains(result.Messages[0]["content"].(string), "language: "+want+"\n") {
			t.Fatalf("wrong historical prompt for %s: %+v", id, result.Messages)
		}
	}
}

func TestChildPromptLocaleInherited(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{newInvokeAgentsDelta(contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write", TaskName: "writing"})}}
			engine := &orchestratorAgentEngine{streams: []contracts.AgentStream{&stubOrchestratableStream{finalText: "done"}}}
			orchestrator := newTestFrameOrchestrator(engine, map[string]catalog.AgentDefinition{"writer": {Key: "writer", Mode: "GENERAL"}}, nil, nil)
			orchestrator.Session.Locale = locale
			build := orchestrator.BuildQuerySession
			called := false
			orchestrator.BuildQuerySession = func(ctx context.Context, req runtimetypes.QueryCommand, summary chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
				called = true
				if options.Locale != locale {
					t.Fatalf("child locale %q, parent %q", options.Locale, locale)
				}
				return build(ctx, req, summary, def, options)
			}
			if _, _, err := orchestrator.Run(main); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("child was not built")
			}
		})
	}
}
