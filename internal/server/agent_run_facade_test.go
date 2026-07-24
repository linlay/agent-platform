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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentrunpkg "agent-platform/internal/agentrun"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestStartAgentRunRegistersIndependentAgentAndTeamRuns(t *testing.T) {
	fixture := newTestFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	agentRun, err := fixture.server.StartAgentRun(cancelled, contracts.AgentRunStartRequest{
		AgentKey: "mock-agent",
		Message:  "detached agent",
		Origin: contracts.AgentRunOrigin{
			CallerAgentKey: "zenmi",
			Subject:        "alice",
			ParentChatID:   "parent-chat",
			ParentRunID:    "parent-run",
			ToolID:         "tool-agent",
		},
	})
	if err != nil {
		t.Fatalf("start detached agent: %v", err)
	}
	if agentRun.RunID == "" || agentRun.ChatID == "" || agentRun.AgentKey != "mock-agent" || agentRun.TeamID != "" {
		t.Fatalf("unexpected agent run %#v", agentRun)
	}
	runStatus, ok := fixture.runs.RunStatus(agentRun.RunID)
	if !ok {
		t.Fatal("agent run was returned before registration")
	}
	if runStatus.AgentRunOrigin == nil ||
		runStatus.AgentRunOrigin.CallerAgentKey != "zenmi" ||
		runStatus.AgentRunOrigin.Subject != "alice" ||
		runStatus.AgentRunOrigin.ParentRunID != "parent-run" ||
		runStatus.AgentRunOrigin.ToolID != "tool-agent" {
		t.Fatalf("runtime origin was not retained: %#v", runStatus.AgentRunOrigin)
	}

	agentRun = waitAgentRunTerminal(t, fixture.server, agentRun.RunID)
	if agentRun.Status != "completed" || agentRun.Content != "Go runtime test response" {
		t.Fatalf("unexpected terminal agent run %#v", agentRun)
	}
	summary, err := fixture.chats.Summary(agentRun.ChatID)
	if err != nil {
		t.Fatalf("agent summary: %v", err)
	}
	if summary.Source != "agent-run:zenmi" {
		t.Fatalf("agent-run source = %q", summary.Source)
	}
	jsonl, err := fixture.chats.LoadJSONLContent(agentRun.ChatID)
	if err != nil {
		t.Fatalf("load agent-run jsonl: %v", err)
	}
	for _, expected := range []string{
		`"agentRunOrigin"`,
		`"callerAgentKey":"zenmi"`,
		`"parentRunId":"parent-run"`,
		`"toolId":"tool-agent"`,
	} {
		if !strings.Contains(jsonl, expected) {
			t.Fatalf("request.query is missing %s: %s", expected, jsonl)
		}
	}
	if strings.Contains(jsonl, `"subject":"alice"`) {
		t.Fatalf("request.query persisted subject: %s", jsonl)
	}

	teamRun, err := fixture.server.StartAgentRun(context.Background(), contracts.AgentRunStartRequest{
		TeamID:  "default",
		Message: "detached team",
		Origin: contracts.AgentRunOrigin{
			CallerAgentKey: "zenmi",
			ParentRunID:    "parent-run",
			ToolID:         "tool-team",
		},
	})
	if err != nil {
		t.Fatalf("start detached team: %v", err)
	}
	if teamRun.RunID == agentRun.RunID || teamRun.ChatID == agentRun.ChatID || teamRun.AgentKey != "" || teamRun.TeamID != "default" {
		t.Fatalf("unexpected team run %#v", teamRun)
	}
	if _, ok := fixture.runs.RunStatus(teamRun.RunID); !ok {
		t.Fatal("team run was returned before registration")
	}
}

func TestStartAgentRunRejectsUnknownAndChatOwnerMismatch(t *testing.T) {
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
		req  contracts.AgentRunStartRequest
		code string
	}{
		{name: "unknown agent", req: contracts.AgentRunStartRequest{AgentKey: "missing", Message: "x"}, code: "agent_not_found"},
		{name: "unknown team", req: contracts.AgentRunStartRequest{TeamID: "missing", Message: "x"}, code: "team_not_found"},
		{name: "agent chat rebound to team", req: contracts.AgentRunStartRequest{TeamID: "default", ChatID: "owned-chat", Message: "x"}, code: "target_owner_mismatch"},
		{name: "team chat rebound to another team", req: contracts.AgentRunStartRequest{TeamID: "alternate", ChatID: "team-chat", Message: "x"}, code: "target_owner_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.server.StartAgentRun(context.Background(), test.req)
			var typed *contracts.AgentRunError
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("error = %#v, want code %q", err, test.code)
			}
		})
	}
}

func TestStartAgentRunIgnoresCatalogVisibility(t *testing.T) {
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

	started, err := fixture.server.StartAgentRun(context.Background(), contracts.AgentRunStartRequest{
		AgentKey: "mock-agent",
		Message:  "run by exact key",
		Origin:   contracts.AgentRunOrigin{CallerAgentKey: "zenmi", ParentRunID: "parent", ToolID: "visibility"},
	})
	if err != nil {
		t.Fatalf("start internal-only catalog Agent: %v", err)
	}
	if started.AgentKey != "mock-agent" || started.RunID == "" {
		t.Fatalf("unexpected run %#v", started)
	}
}

func TestAgentRunSelfTargetChatRules(t *testing.T) {
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

	handler := agentrunpkg.NewToolHandler(fixture.server, fixture.runs)
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
			CurrentToolName: agentrunpkg.ToolName,
		}
	}

	sameChat, err := handler.Invoke(context.Background(), agentrunpkg.ToolName, map[string]any{
		"action": "query", "agentKey": "mock-agent", "chatId": "self-parent-chat", "message": "same chat",
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

	newChat, err := handler.Invoke(context.Background(), agentrunpkg.ToolName, map[string]any{
		"action": "query", "agentKey": "mock-agent", "message": "new chat",
	}, execContext("self-new-chat"))
	if err != nil || newChat.Error != "" {
		t.Fatalf("new-chat self target failed: result=%#v err=%v", newChat, err)
	}
	newRun := newChat.Structured["run"].(map[string]any)
	if newRun["agentKey"] != "mock-agent" || newRun["chatId"] == "" || newRun["chatId"] == "self-parent-chat" {
		t.Fatalf("unexpected new-chat self target %#v", newRun)
	}
	waitAgentRunTerminal(t, fixture.server, newRun["runId"].(string))

	_, _, err = fixture.chats.EnsureChat("self-idle-chat", "mock-agent", "", "idle")
	if err != nil {
		t.Fatalf("ensure idle chat: %v", err)
	}
	idleChat, err := handler.Invoke(context.Background(), agentrunpkg.ToolName, map[string]any{
		"action": "query", "agentKey": "mock-agent", "chatId": "self-idle-chat", "message": "idle chat",
	}, execContext("self-idle-chat"))
	if err != nil || idleChat.Error != "" {
		t.Fatalf("idle-chat self target failed: result=%#v err=%v", idleChat, err)
	}
	idleRun := idleChat.Structured["run"].(map[string]any)
	if idleRun["chatId"] != "self-idle-chat" || idleRun["agentKey"] != "mock-agent" {
		t.Fatalf("unexpected idle-chat self target %#v", idleRun)
	}
	waitAgentRunTerminal(t, fixture.server, idleRun["runId"].(string))
}

func TestAgentRunStatusAndQuestionSubmit(t *testing.T) {
	var calls atomic.Int32
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Continue?\",\"type\":\"select\",\"options\":[{\"label\":\"Yes\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"continued"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call")
		}
	})

	started, err := fixture.server.StartAgentRun(context.Background(), contracts.AgentRunStartRequest{
		AgentKey: "mock-agent",
		Message:  "ask first",
		Origin:   contracts.AgentRunOrigin{CallerAgentKey: "zenmi", ParentRunID: "parent", ToolID: "tool"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	awaiting := waitAgentRunStatus(t, fixture.server, started.RunID, "awaiting")
	if awaiting.Awaiting == nil || awaiting.Awaiting.Mode != "question" || awaiting.Awaiting.AwaitingID == "" || len(awaiting.Awaiting.Questions) != 1 {
		t.Fatalf("unexpected question status %#v", awaiting)
	}
	params, err := api.EncodeSubmitParams([]map[string]any{{"id": "q1", "answer": "Yes"}})
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	response, err := fixture.server.AgentRunSubmitQuestion(api.SubmitRequest{
		ChatID:     awaiting.ChatID,
		RunID:      awaiting.RunID,
		AgentKey:   awaiting.AgentKey,
		AwaitingID: awaiting.Awaiting.AwaitingID,
		Params:     params,
	})
	if err != nil || !response.Accepted {
		t.Fatalf("submit question: response=%#v err=%v", response, err)
	}
	completed := waitAgentRunTerminal(t, fixture.server, started.RunID)
	if completed.Status != "completed" || completed.Content != "continued" {
		t.Fatalf("unexpected completed status %#v", completed)
	}
}

func TestAgentRunSubmitRejectsNonQuestionAwaiting(t *testing.T) {
	fixture := newTestFixture(t)
	_, control, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID: "approval-run", ChatID: "approval-chat", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", ""),
	})
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "approval-await", Mode: "approval", ItemCount: 1})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	_, err := fixture.server.AgentRunSubmitQuestion(api.SubmitRequest{
		RunID: "approval-run", AgentKey: "mock-agent", AwaitingID: "approval-await",
	})
	var typed *contracts.AgentRunError
	if !errors.As(err, &typed) || typed.Code != "submit_mode_not_allowed" {
		t.Fatalf("error = %#v", err)
	}
}

func TestAgentRunStatusReturnsFailedError(t *testing.T) {
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
	status, err := server.AgentRunStatus("failed-run")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "failed" || status.LastSeq != 1 || status.Error["code"] != "provider_error" || status.Error["message"] != "upstream failed" {
		t.Fatalf("unexpected failed status %#v", status)
	}
}

func TestAgentRunControlsDetachedSSEProxy(t *testing.T) {
	queryStarted := make(chan struct{}, 1)
	steered := make(chan map[string]any, 1)
	interrupted := make(chan map[string]any, 1)
	stopQuery := make(chan struct{})
	var stopOnce sync.Once

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
		case "/api/steer":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			steered <- payload
			_, _ = io.WriteString(w, `{"code":0,"msg":"success","data":{"accepted":true,"status":"accepted","runId":"upstream","steerId":"s1"}}`)
		case "/api/interrupt":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			interrupted <- payload
			stopOnce.Do(func() { close(stopQuery) })
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

	started, err := fixture.server.StartAgentRun(context.Background(), contracts.AgentRunStartRequest{
		AgentKey: "mock-agent",
		Message:  "proxy work",
		Origin:   contracts.AgentRunOrigin{CallerAgentKey: "zenmi", ParentRunID: "parent", ToolID: "proxy-tool"},
	})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	select {
	case <-queryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy query did not start")
	}

	steerResponse, err := fixture.server.AgentRunSteer(api.SteerRequest{
		RunID: started.RunID, ChatID: started.ChatID, AgentKey: started.AgentKey, Message: "focus",
	})
	if err != nil || !steerResponse.Accepted {
		t.Fatalf("proxy steer: response=%#v err=%v", steerResponse, err)
	}
	select {
	case payload := <-steered:
		if payload["message"] != "focus" || payload["agentKey"] != "mock-agent" {
			t.Fatalf("unexpected proxy steer payload %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy steer was not forwarded")
	}

	interruptResponse, err := fixture.server.AgentRunInterrupt(api.InterruptRequest{
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
	if status := waitAgentRunFinished(t, fixture.server, started.RunID, "interrupted"); status.Status != "interrupted" {
		t.Fatalf("unexpected proxy status %#v", status)
	}
}

func waitAgentRunStatus(t *testing.T, server *Server, runID string, want string) contracts.AgentRunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.AgentRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.AgentRunStatus(runID)
	encoded, _ := json.Marshal(status)
	t.Fatalf("run %s did not reach %s: %s", runID, want, encoded)
	return contracts.AgentRunSnapshot{}
}

func waitAgentRunTerminal(t *testing.T, server *Server, runID string) contracts.AgentRunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.AgentRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		switch status.Status {
		case "completed", "failed", "interrupted":
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.AgentRunStatus(runID)
	t.Fatalf("run did not terminate: %#v", status)
	return contracts.AgentRunSnapshot{}
}

func waitAgentRunFinished(t *testing.T, server *Server, runID string, want string) contracts.AgentRunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.AgentRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want && status.CompletedAt != 0 {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.AgentRunStatus(runID)
	t.Fatalf("run did not finish as %s: %#v", want, status)
	return contracts.AgentRunSnapshot{}
}
