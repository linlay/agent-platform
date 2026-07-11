package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestTeamSideRunEntriesRejectNonMemberAndCurrentAgentDrift(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime:  setupSideEntryTeamRuntime(t),
		notifications: ws.NewHub(),
	})

	const (
		teamID           = "default.demo"
		continuationChat = "chat-team-continuation"
		btwChat          = "chat-team-btw"
		compactChat      = "chat-team-compact"
	)
	for _, chatID := range []string{continuationChat, btwChat, compactChat} {
		if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", teamID, "hello"); err != nil {
			t.Fatalf("ensure team chat %s: %v", chatID, err)
		}
	}
	const (
		httpRunID      = "run-team-http-drift"
		httpAwaitingID = "await-team-http-drift"
		activeRunID    = "run-team-active-drift"
		activeAwaiting = "await-team-active-drift"
	)
	seedDeferredAwaiting(t, fixture.chats, continuationChat, httpRunID, httpAwaitingID, "question", 600, time.Now().UnixMilli())
	fixture.server.hydrateDeferredAwaitings()
	_, activeControl, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID:    activeRunID,
		ChatID:   continuationChat,
		AgentKey: "mock-agent",
		TeamID:   teamID,
	})
	activeAwaitingContext := contracts.AwaitingSubmitContext{
		AwaitingID:       activeAwaiting,
		PublicAwaitingID: activeAwaiting,
		Mode:             "plan",
		ItemCount:        1,
	}
	activeControl.ExpectSubmit(activeAwaitingContext)
	approveParams := api.SubmitParams{[]byte(`{"decision":"approve"}`)}
	frozenContinuation, prepareErr := fixture.server.prepareActiveSubmitContinuation(api.SubmitRequest{
		ChatID:     continuationChat,
		RunID:      activeRunID,
		AgentKey:   "mock-agent",
		AwaitingID: activeAwaiting,
		Params:     approveParams,
	}, activeAwaitingContext)
	if prepareErr != nil || frozenContinuation.ContinuationRunID == "" || frozenContinuation.ContinuationState == nil {
		t.Fatalf("prepare frozen active continuation: req=%#v err=%v", frozenContinuation, prepareErr)
	}

	continued, err := fixture.server.startAwaitingContinuation(
		DeferredAwaiting{ChatID: continuationChat, RunID: "run-team-non-member", Mode: "question"},
		api.SubmitRequest{
			ChatID:     continuationChat,
			RunID:      "run-team-non-member",
			AgentKey:   "outside-agent",
			AwaitingID: "await-team-non-member",
		},
		map[string]any{"mode": "question"},
	)
	assertTeamSideEntryStatus(t, continued, err, http.StatusForbidden)

	compactNonMember := serveTeamSideEntryJSON(t, fixture.server, "/api/compact", `{"chatId":"`+compactChat+`","agentKey":"outside-agent"}`)
	if compactNonMember.Code != http.StatusForbidden {
		t.Fatalf("compact non-member status = %d, want 403: %s", compactNonMember.Code, compactNonMember.Body.String())
	}

	if err := os.Remove(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")); err != nil {
		t.Fatalf("remove current agent definition: %v", err)
	}
	if err := fixture.registry.Reload(context.Background(), "agents"); err != nil {
		t.Fatalf("reload agents after drift: %v", err)
	}

	httpDrift := serveTeamSideEntryJSON(t, fixture.server, "/api/submit", `{"chatId":"`+continuationChat+`","runId":"`+httpRunID+`","agentKey":"mock-agent","awaitingId":"`+httpAwaitingID+`","params":[{"id":"q1","answer":"Approve"}]}`)
	if httpDrift.Code != http.StatusServiceUnavailable {
		t.Fatalf("submit continuation drift status = %d, want 503: %s", httpDrift.Code, httpDrift.Body.String())
	}
	pendingSummary, err := fixture.chats.Summary(continuationChat)
	if err != nil || pendingSummary == nil || pendingSummary.PendingAwaiting == nil || pendingSummary.PendingAwaiting.AwaitingID != httpAwaitingID {
		t.Fatalf("failed continuation admission must not consume awaiting: summary=%#v err=%v", pendingSummary, err)
	}
	activeBody := `{"chatId":"` + continuationChat + `","runId":"` + activeRunID + `","agentKey":"mock-agent","awaitingId":"` + activeAwaiting + `","params":[{"decision":"approve"}]}`
	activeHTTPDrift := serveTeamSideEntryJSON(t, fixture.server, "/api/submit", activeBody)
	if activeHTTPDrift.Code != http.StatusServiceUnavailable {
		t.Fatalf("active submit drift status = %d, want 503: %s", activeHTTPDrift.Code, activeHTTPDrift.Body.String())
	}
	if _, ok := activeControl.LookupAwaiting(activeAwaiting); !ok {
		t.Fatal("failed active continuation admission consumed awaiting")
	}
	assertTeamActiveSubmitWebSocketDrift(t, fixture.server, activeBody)
	fixture.runs.Finish(activeRunID)
	continuedRunID, continueErr := fixture.server.startRunContinuation(contracts.DeltaRunContinuation{
		SourceRunID:       activeRunID,
		RunID:             frozenContinuation.ContinuationRunID,
		ChatID:            continuationChat,
		AgentKey:          "mock-agent",
		AwaitingID:        activeAwaiting,
		Mode:              "plan",
		Params:            approveParams,
		Answer:            map[string]any{"mode": "plan", "plan": map[string]any{"decision": "approve"}},
		ContinuationState: frozenContinuation.ContinuationState,
	})
	if continueErr != nil || continuedRunID != frozenContinuation.ContinuationRunID {
		t.Fatalf("frozen active continuation did not survive catalog reload: runId=%q err=%v", continuedRunID, continueErr)
	}

	continued, err = fixture.server.startAwaitingContinuation(
		DeferredAwaiting{ChatID: continuationChat, RunID: "run-team-drift", Mode: "question"},
		api.SubmitRequest{
			ChatID:     continuationChat,
			RunID:      "run-team-drift",
			AwaitingID: "await-team-drift",
		},
		map[string]any{"mode": "question"},
	)
	assertTeamSideEntryStatus(t, continued, err, http.StatusServiceUnavailable)

	btwDrift := serveTeamSideEntryJSON(t, fixture.server, "/api/btw", `{"chatId":"`+btwChat+`","message":"side"}`)
	if btwDrift.Code != http.StatusServiceUnavailable {
		t.Fatalf("BTW drift status = %d, want 503: %s", btwDrift.Code, btwDrift.Body.String())
	}

	compactDrift := serveTeamSideEntryJSON(t, fixture.server, "/api/compact", `{"chatId":"`+compactChat+`"}`)
	if compactDrift.Code != http.StatusServiceUnavailable {
		t.Fatalf("compact drift status = %d, want 503: %s", compactDrift.Code, compactDrift.Body.String())
	}
}

func assertTeamActiveSubmitWebSocketDrift(t *testing.T, handler http.Handler, body string) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/submit",
		ID:      "team-active-submit-drift",
		Payload: []byte(body),
	}); err != nil {
		t.Fatalf("write websocket submit: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set websocket deadline: %v", err)
	}
	for {
		var frame ws.ErrorFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read websocket error: %v", err)
		}
		if frame.ID != "team-active-submit-drift" {
			continue
		}
		if frame.Frame != ws.FrameError || frame.Code != http.StatusServiceUnavailable {
			t.Fatalf("unexpected websocket Team drift error: %#v", frame)
		}
		return
	}
}

func setupSideEntryTeamRuntime(t *testing.T) func(string, *config.Config) {
	t.Helper()
	return func(root string, cfg *config.Config) {
		workspace := filepath.Join(root, "mock-coder-workspace")
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("mkdir mock coder workspace: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []byte(strings.Join([]string{
			"key: mock-agent",
			"name: Mock Agent",
			"mode: CODER",
			"modelConfig:",
			"  modelKey: mock-model",
			"runtimeConfig:",
			"  workspaceRoot: " + filepath.ToSlash(workspace),
		}, "\n")), 0o644); err != nil {
			t.Fatalf("write mock coder agent: %v", err)
		}
		stableAgentDir := filepath.Join(cfg.Paths.AgentsDir, "stable-agent")
		if err := os.MkdirAll(stableAgentDir, 0o755); err != nil {
			t.Fatalf("mkdir stable agent: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stableAgentDir, "agent.yml"), []byte(strings.Join([]string{
			"key: stable-agent",
			"name: Stable Agent",
			"mode: REACT",
			"modelConfig:",
			"  modelKey: mock-model",
		}, "\n")), 0o644); err != nil {
			t.Fatalf("write stable agent: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Paths.TeamsDir, "default.demo.yml"), []byte(strings.Join([]string{
			"name: Default Team",
			"defaultAgentKey: stable-agent",
			"agentKeys:",
			"  - stable-agent",
			"  - mock-agent",
		}, "\n")), 0o644); err != nil {
			t.Fatalf("write team: %v", err)
		}
	}
}

func assertTeamSideEntryStatus(t *testing.T, continued bool, err error, want int) {
	t.Helper()
	if continued {
		t.Fatalf("continuation unexpectedly started")
	}
	var typed *statusError
	if !errors.As(err, &typed) || typed.status != want {
		t.Fatalf("continuation error = %#v, want status %d", err, want)
	}
}

func serveTeamSideEntryJSON(t *testing.T, server *Server, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}
