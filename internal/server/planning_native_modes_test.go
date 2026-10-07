package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

// planningMode is a native capability: a GENERAL Agent plans with its own
// tools minus the planning exclusions, and approval starts a separate ordinary
// Run of the same Agent minus the execution exclusions.
func TestGeneralPlanningModeConfirmsThenExecutesInNewRun(t *testing.T) {
	var providerCallCount atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode model request: %v", err)
			return
		}
		toolNames := providerRequestToolNames(payload["tools"])
		// Planning and execution both request the Agent's own model.
		if model := stringValue(payload["model"]); model != "mock-model-id" {
			t.Errorf("provider model = %q, want the Agent's model", model)
		}
		switch call := providerCallCount.Add(1); call {
		case 1:
			assertStringSliceContains(t, toolNames, "file_read", "ask_user_question", "finalize_planning")
			assertStringSliceExcludes(t, toolNames, "bash", "file_write")
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_plan", "finalize_planning", map[string]any{
					"markdown": "# General Plan\n\n## Steps\n- Do the work\n",
				}),
				`[DONE]`,
			)
		case 2:
			assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write")
			assertStringSliceExcludes(t, toolNames, "ask_user_question", "finalize_planning")
			if !providerMessagesContainText(payload, "Execute the confirmed plan.\n\nOriginal request:\nplan this") ||
				!providerMessagesContainText(payload, "Confirmed planning:\n# General Plan") {
				t.Errorf("expected the confirmed plan as the execution message, got %#v", payload["messages"])
			}
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"general execution completed"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Errorf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "general-app")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir general agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: general-app",
				"name: General App",
				"mode: GENERAL",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - bash",
				"    - file_read",
				"    - file_write",
				"    - ask_user_question",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write general agent: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"plan this","agentKey":"general-app","planningMode":true}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID, awaitingID := readAwaitingApproval(t, reader, &streamBody, "confirm")
	submitBody, err := json.Marshal(map[string]any{
		"agentKey": "general-app", "runId": runID, "awaitingId": awaitingID,
		"params": []map[string]any{{"id": "confirm", "decision": "approve"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBuffer(submitBody))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK || !strings.Contains(submitRec.Body.String(), `"accepted":true`) {
		t.Fatalf("expected accepted approval, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read planning stream: %v", err)
	}
	streamBody.Write(rest)
	body := streamBody.String()
	if !strings.Contains(body, `"type":"run.complete","runId":"`+runID+`"`) {
		t.Fatalf("expected the planning Run to complete after approval, got %s", body)
	}
	if strings.Contains(body, "general execution completed") {
		t.Fatalf("the planning Run must not execute the plan itself, got %s", body)
	}

	waitForProviderCallCount(t, &providerCallCount, 2)
	chatID := chatIDFromStreamForPlanningTest(t, body)
	executionRunID := waitForJSONLCoderExecuteRunID(t, fixture.chats, chatID, runID)
	if executionRunID == runID {
		t.Fatalf("expected a new execution Run, got the planning Run %s", runID)
	}
	executionBody := attachRunSSE(t, httpServer.URL, "general-app", executionRunID)
	if !strings.Contains(executionBody, "general execution completed") {
		t.Fatalf("expected execution content in the new Run, got %s", executionBody)
	}
	content, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load jsonl: %v", err)
	}
	for _, cacheKey := range []string{`"cacheKey":"react:planning"`, `"cacheKey":"react:execute"`} {
		if !strings.Contains(content, cacheKey) {
			t.Fatalf("expected %s in chat history, got %s", cacheKey, content)
		}
	}
}

func chatIDFromStreamForPlanningTest(t *testing.T, body string) string {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if chatID := stringValue(message["chatId"]); chatID != "" {
			return chatID
		}
	}
	t.Fatalf("no chatId in stream %s", body)
	return ""
}

// A KBASE planning Run stays read-only even when the request asks for editing;
// the request keeps editingMode for the Run that executes the confirmed plan.
func TestKBasePlanningRunDoesNotEnableWorkspaceEditing(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	workspace := filepath.Join(root, "workspace")
	agentDir := filepath.Join(agentsDir, "docs-kbase")
	for _, dir := range []string{workspace, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(
		"key: docs-kbase\nmode: KBASE\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: "+filepath.ToSlash(workspace)+"\nkbaseConfig: {}\ntoolConfig:\n  tools:\n    - file_read\n    - file_write\n    - ask_user_question\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: agentsDir, ChatsDir: filepath.Join(root, "chats")}}
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def, ok := registry.AgentDefinition("docs-kbase")
	if !ok {
		t.Fatal("expected docs-kbase definition")
	}
	server := &Server{deps: Dependencies{Config: cfg, Registry: registry}}
	enabled := true
	build := func(req api.QueryRequest) (planning bool, editing bool, mutation bool, confirmed bool, tools []string) {
		req.AgentKey, req.ChatID = "docs-kbase", "chat-1"
		session, err := server.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat-1"}, def, querySessionBuildOptions{})
		if err != nil {
			t.Fatalf("build session: %v", err)
		}
		return session.PlanningMode, session.EditingMode, session.ScopedFilePolicy != nil && session.ScopedFilePolicy.WorkspaceMutationEnabled, session.ConfirmedPlanRun, session.ToolNames
	}

	planning, editing, mutation, confirmed, _ := build(api.QueryRequest{RunID: "run-plan", PlanningMode: &enabled, EditingMode: &enabled})
	if !planning || editing || mutation || confirmed {
		t.Fatalf("planning Run: planning=%v editing=%v mutation=%v confirmed=%v", planning, editing, mutation, confirmed)
	}

	disabled := false
	planning, editing, mutation, confirmed, tools := build(api.QueryRequest{
		RunID: "run-exec", PlanningMode: &disabled, EditingMode: &enabled,
		Params: map[string]any{"_coderPlanningApproveContinuation": true},
	})
	if planning || !editing || !mutation || !confirmed {
		t.Fatalf("confirmed-plan Run: planning=%v editing=%v mutation=%v confirmed=%v", planning, editing, mutation, confirmed)
	}
	assertStringSliceContains(t, tools, "file_read", "file_write")
	assertStringSliceExcludes(t, tools, "ask_user_question", "finalize_planning")
}
