package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runopspkg "agent-platform/internal/runops"
	"agent-platform/internal/stream"
)

func TestStartRunRegistersIndependentAgentAndTeamRuns(t *testing.T) {
	fixture := newTestFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	agentTargetRun, err := fixture.server.StartRun(cancelled, contracts.RunStartRequest{
		AgentKey: "mock-agent",
		Message:  "detached agent",
		Origin: contracts.RunOrigin{
			AgentKey: "zenmi",
			Subject:  "alice",
			ChatID:   "parent-chat",
			RunID:    "parent-run",
			ToolID:   "tool-agent",
		},
	})
	if err != nil {
		t.Fatalf("start detached agent: %v", err)
	}
	if agentTargetRun.RunID == "" || agentTargetRun.ChatID == "" || agentTargetRun.AgentKey != "mock-agent" || agentTargetRun.TeamID != "" {
		t.Fatalf("unexpected Agent target run %#v", agentTargetRun)
	}
	runStatus, ok := fixture.runs.RunStatus(agentTargetRun.RunID)
	if !ok {
		t.Fatal("Agent target run was returned before registration")
	}
	if runStatus.RunOrigin == nil ||
		runStatus.RunOrigin.AgentKey != "zenmi" ||
		runStatus.RunOrigin.Subject != "alice" ||
		runStatus.RunOrigin.ChatID != "parent-chat" ||
		runStatus.RunOrigin.RunID != "parent-run" ||
		runStatus.RunOrigin.ToolID != "tool-agent" {
		t.Fatalf("runtime origin was not retained: %#v", runStatus.RunOrigin)
	}

	agentTargetRun = waitRunTerminal(t, fixture.server, agentTargetRun.RunID)
	if agentTargetRun.Status != "completed" || agentTargetRun.Content != "Go runtime test response" {
		t.Fatalf("unexpected terminal Agent target run %#v", agentTargetRun)
	}
	summary, err := fixture.chats.Summary(agentTargetRun.ChatID)
	if err != nil {
		t.Fatalf("agent summary: %v", err)
	}
	if summary.Source != "run-query:zenmi" {
		t.Fatalf("run-query source = %q", summary.Source)
	}
	jsonl, err := fixture.chats.LoadJSONLContent(agentTargetRun.ChatID)
	if err != nil {
		t.Fatalf("load run-query jsonl: %v", err)
	}
	for _, expected := range []string{
		`"runOrigin"`,
		`"agentKey":"zenmi"`,
		`"chatId":"parent-chat"`,
		`"runId":"parent-run"`,
		`"toolId":"tool-agent"`,
	} {
		if !strings.Contains(jsonl, expected) {
			t.Fatalf("request.query is missing %s: %s", expected, jsonl)
		}
	}
	for _, forbidden := range []string{`"runQueryOrigin"`, `"callerAgentKey"`, `"parentChatId"`, `"parentRunId"`, `"subject":"alice"`} {
		if strings.Contains(jsonl, forbidden) {
			t.Fatalf("request.query persisted obsolete or private field %s: %s", forbidden, jsonl)
		}
	}

	teamRun, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		TeamID:  "default",
		Message: "detached team",
		Origin: contracts.RunOrigin{
			AgentKey: "zenmi",
			ChatID:   "parent-chat",
			RunID:    "parent-run",
			ToolID:   "tool-team",
		},
	})
	if err != nil {
		t.Fatalf("start detached team: %v", err)
	}
	if teamRun.RunID == agentTargetRun.RunID || teamRun.ChatID == agentTargetRun.ChatID || teamRun.AgentKey != "" || teamRun.TeamID != "default" {
		t.Fatalf("unexpected team run %#v", teamRun)
	}
	if _, ok := fixture.runs.RunStatus(teamRun.RunID); !ok {
		t.Fatal("team run was returned before registration")
	}
}

func TestStartRunRejectsUnknownAndChatOwnerMismatch(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			alternateDir := filepath.Join(cfg.Paths.TeamsDir, "alternate")
			if err := os.MkdirAll(alternateDir, 0o755); err != nil {
				t.Fatalf("mkdir alternate team: %v", err)
			}
			content := strings.Join([]string{
				"name: Alternate Team",
				"agentKeys:",
				"  - mock-agent",
				"orchestrator:",
				"  modelConfig:",
				"    modelKey: mock-model",
			}, "\n")
			if err := os.WriteFile(filepath.Join(alternateDir, "team.yml"), []byte(content), 0o644); err != nil {
				t.Fatalf("write alternate team: %v", err)
			}
		},
	})
	_, _, err := fixture.chats.EnsureChat("owned-chat", "mock-agent", "", "hello")
	if err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	_, _, err = fixture.chats.EnsureChat("team-chat", "", "default", "hello")
	if err != nil {
		t.Fatalf("ensure Team chat: %v", err)
	}

	tests := []struct {
		name string
		req  contracts.RunStartRequest
		code string
	}{
		{name: "unknown agent", req: contracts.RunStartRequest{AgentKey: "missing", Message: "x"}, code: "agent_not_found"},
		{name: "unknown team", req: contracts.RunStartRequest{TeamID: "missing", Message: "x"}, code: "team_not_found"},
		{name: "agent chat rebound to team", req: contracts.RunStartRequest{TeamID: "default", ChatID: "owned-chat", Message: "x"}, code: "target_owner_mismatch"},
		{name: "team chat rebound to another team", req: contracts.RunStartRequest{TeamID: "alternate", ChatID: "team-chat", Message: "x"}, code: "target_owner_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.server.StartRun(context.Background(), test.req)
			var typed *contracts.RunToolError
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("error = %#v, want code %q", err, test.code)
			}
		})
	}
}

func TestStartRunIgnoresCatalogVisibility(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatalf("read agent config: %v", err)
			}
			content := strings.TrimSpace(string(data)) + "\nvisibility:\n  scopes:\n    - internal\n"
			if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
				t.Fatalf("write internal-only agent config: %v", err)
			}
		},
	})

	started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		AgentKey: "mock-agent",
		Message:  "run by exact key",
		Origin:   contracts.RunOrigin{AgentKey: "zenmi", RunID: "parent", ToolID: "visibility"},
	})
	if err != nil {
		t.Fatalf("start internal-only catalog Agent: %v", err)
	}
	if started.AgentKey != "mock-agent" || started.RunID == "" {
		t.Fatalf("unexpected run %#v", started)
	}
}

func TestRunSelfTargetChatRules(t *testing.T) {
	fixture := newTestFixture(t)
	_, _, err := fixture.chats.EnsureChat("self-parent-chat", "mock-agent", "", "parent")
	if err != nil {
		t.Fatalf("ensure parent chat: %v", err)
	}
	_, parentControl, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "self-parent-run",
		ChatID:   "self-parent-chat",
		AgentKey: "mock-agent",
		Subject:  "alice",
		RunOwner: contracts.AgentRunOwner("mock-agent", ""),
	})
	t.Cleanup(func() {
		fixture.runs.Finish("self-parent-run")
	})

	handler := runopspkg.NewToolHandler(fixture.server, fixture.runs)
	execContext := func(toolID string) *contracts.ExecutionContext {
		return &contracts.ExecutionContext{
			Session: contracts.QuerySession{
				RunID:    "self-parent-run",
				ChatID:   "self-parent-chat",
				AgentKey: "mock-agent",
				Subject:  "alice",
				RunOwner: contracts.AgentRunOwner("mock-agent", ""),
			},
			RunControl:      parentControl,
			CurrentToolID:   toolID,
			CurrentToolName: runopspkg.QueryToolName,
		}
	}

	sameChat, err := handler.Invoke(context.Background(), runopspkg.QueryToolName, map[string]any{
		"agentKey": "mock-agent", "chatId": "self-parent-chat", "message": "same chat",
	}, execContext("self-same-chat"))
	if err != nil {
		t.Fatalf("same-chat invoke: %v", err)
	}
	if sameChat.Error != activeRunConflictCode {
		t.Fatalf("same-chat error = %q, want %q: %#v", sameChat.Error, activeRunConflictCode, sameChat)
	}
	active, ok, activeErr := fixture.runs.ActiveRunForChat("self-parent-chat")
	if activeErr != nil || !ok || active.RunID != "self-parent-run" {
		t.Fatalf("parent active run changed: active=%#v ok=%t err=%v", active, ok, activeErr)
	}

	newChat, err := handler.Invoke(context.Background(), runopspkg.QueryToolName, map[string]any{
		"agentKey": "mock-agent", "message": "new chat",
	}, execContext("self-new-chat"))
	if err != nil || newChat.Error != "" {
		t.Fatalf("new-chat self target failed: result=%#v err=%v", newChat, err)
	}
	newRun := newChat.Structured["run"].(map[string]any)
	if newRun["agentKey"] != "mock-agent" || newRun["chatId"] == "" || newRun["chatId"] == "self-parent-chat" {
		t.Fatalf("unexpected new-chat self target %#v", newRun)
	}
	waitRunTerminal(t, fixture.server, newRun["runId"].(string))

	_, _, err = fixture.chats.EnsureChat("self-idle-chat", "mock-agent", "", "idle")
	if err != nil {
		t.Fatalf("ensure idle chat: %v", err)
	}
	idleChat, err := handler.Invoke(context.Background(), runopspkg.QueryToolName, map[string]any{
		"agentKey": "mock-agent", "chatId": "self-idle-chat", "message": "idle chat",
	}, execContext("self-idle-chat"))
	if err != nil || idleChat.Error != "" {
		t.Fatalf("idle-chat self target failed: result=%#v err=%v", idleChat, err)
	}
	idleRun := idleChat.Structured["run"].(map[string]any)
	if idleRun["chatId"] != "self-idle-chat" || idleRun["agentKey"] != "mock-agent" {
		t.Fatalf("unexpected idle-chat self target %#v", idleRun)
	}
	waitRunTerminal(t, fixture.server, idleRun["runId"].(string))
}

func TestGetRunStatusReportsQuestionAwaiting(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Continue?\",\"type\":\"select\",\"options\":[{\"label\":\"Yes\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})

	started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		AgentKey: "mock-agent",
		Message:  "ask first",
		Origin:   contracts.RunOrigin{AgentKey: "zenmi", RunID: "parent", ToolID: "tool"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	awaiting := waitGetRunStatus(t, fixture.server, started.RunID, "awaiting")
	if awaiting.Awaiting == nil || awaiting.Awaiting.Mode != "question" || awaiting.Awaiting.AwaitingID == "" || len(awaiting.Awaiting.Questions) != 1 {
		t.Fatalf("unexpected question status %#v", awaiting)
	}
	response, err := fixture.server.InterruptRun(api.InterruptRequest{
		RunID: started.RunID, ChatID: started.ChatID, AgentKey: started.AgentKey, Message: "finish test",
	})
	if err != nil || !response.Accepted {
		t.Fatalf("interrupt awaiting run: response=%#v err=%v", response, err)
	}
}

func TestGetRunStatusReturnsFailedError(t *testing.T) {
	runs := contracts.NewInMemoryRunManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{
		RunID: "failed-run", ChatID: "failed-chat", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", ""),
	})
	eventBus, ok := runs.EventBus("failed-run")
	if !ok {
		t.Fatal("event bus is unavailable")
	}
	eventBus.Publish(stream.EventData{
		Seq:       1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"error": map[string]any{"code": "provider_error", "message": "upstream failed"},
		},
	})
	control.TransitionState(contracts.RunLoopStateFailed)
	runs.Finish("failed-run")

	server := &Server{deps: Dependencies{Runs: runs}}
	status, err := server.GetRunStatus("failed-run")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "failed" || status.LastSeq != 1 || status.Error["code"] != "provider_error" || status.Error["message"] != "upstream failed" {
		t.Fatalf("unexpected failed status %#v", status)
	}
}

func TestInterruptRunsDetachedSSEProxy(t *testing.T) {
	queryStarted := make(chan struct{}, 1)
	interrupted := make(chan map[string]any, 1)
	stopQuery := make(chan struct{})

	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/query":
			queryStarted <- struct{}{}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			now := time.Now().UnixMilli()
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"content.start\",\"contentId\":\"c1\",\"timestamp\":%d}\n\n", now)
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"content.delta\",\"contentId\":\"c1\",\"delta\":\"working\",\"timestamp\":%d}\n\n", now+1)
			flusher.Flush()
			<-stopQuery
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"run.cancel\",\"runId\":\"upstream\",\"timestamp\":%d}\n\n", time.Now().UnixMilli())
			flusher.Flush()
		case "/api/interrupt":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			interrupted <- payload
			close(stopQuery)
			_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{"accepted":true,"status":"accepted","runId":"upstream"}}`)
		default:
			http.NotFound(w, r)
		}
	}))

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			content := strings.Join([]string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"role: proxy",
				"description: detached proxy",
				"mode: PROXY",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
				"  timeout: 30",
			}, "\n")
			if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
				t.Fatalf("write proxy agent: %v", err)
			}
		},
	})

	started, err := fixture.server.StartRun(context.Background(), contracts.RunStartRequest{
		AgentKey: "mock-agent",
		Message:  "proxy work",
		Origin:   contracts.RunOrigin{AgentKey: "zenmi", ChatID: "parent-chat", RunID: "parent", ToolID: "proxy-tool"},
	})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	select {
	case <-queryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy query did not start")
	}

	interruptResponse, err := fixture.server.InterruptRun(api.InterruptRequest{
		RunID: started.RunID, ChatID: started.ChatID, AgentKey: started.AgentKey, Message: "stop",
	})
	if err != nil || !interruptResponse.Accepted {
		t.Fatalf("proxy interrupt: response=%#v err=%v", interruptResponse, err)
	}
	select {
	case payload := <-interrupted:
		if payload["message"] != "stop" || payload["agentKey"] != "mock-agent" {
			t.Fatalf("unexpected proxy interrupt payload %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy interrupt was not forwarded")
	}
	if status := waitRunFinished(t, fixture.server, started.RunID, "interrupted"); status.Status != "interrupted" {
		t.Fatalf("unexpected proxy status %#v", status)
	}
	jsonl, err := fixture.chats.LoadJSONLContent(started.ChatID)
	if err != nil {
		t.Fatalf("load proxy run JSONL: %v", err)
	}
	for _, expected := range []string{
		`"runOrigin"`,
		`"agentKey":"zenmi"`,
		`"chatId":"parent-chat"`,
		`"runId":"parent"`,
		`"toolId":"proxy-tool"`,
	} {
		if !strings.Contains(jsonl, expected) {
			t.Fatalf("proxy request.query is missing %s: %s", expected, jsonl)
		}
	}
}

func waitGetRunStatus(t *testing.T, server *Server, runID string, want string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	encoded, _ := json.Marshal(status)
	t.Fatalf("run %s did not reach %s: %s", runID, want, encoded)
	return contracts.RunSnapshot{}
}

func waitRunTerminal(t *testing.T, server *Server, runID string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		switch status.Status {
		case "completed", "failed", "interrupted":
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	t.Fatalf("run did not terminate: %#v", status)
	return contracts.RunSnapshot{}
}

func waitRunFinished(t *testing.T, server *Server, runID string, want string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want && status.CompletedAt != 0 {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	t.Fatalf("run did not finish as %s: %#v", want, status)
	return contracts.RunSnapshot{}
}
