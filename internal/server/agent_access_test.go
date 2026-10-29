package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
)

func accessDefaultFixture(t *testing.T, defaultLevel string) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, []byte("\naccessConfig:\n  default: "+defaultLevel+"\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	}})
}

func TestMailboxFreezesTargetDefaultBeforeDelayedDelivery(t *testing.T) {
	f := accessDefaultFixture(t, "full_access")
	bindTestRunControl(t, f.server, "parent", "ws", "desktop")
	_, _, _ = f.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "default"})
	defer f.runs.Finish("parent")
	store, err := mailbox.Open(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	box := mailbox.New(ctx, store, f.cfg.Paths.ChatsDir, 4, nil)
	t.Cleanup(func() { cancel(); _ = box.Close() })
	q := testQueryService(f.server)
	q.BindMailbox(box)
	req := mailbox.Request{NewChat: true, Start: contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ChatID: "parent-chat", ToolID: "mail-call"}}}
	prepared, err := q.PrepareMail(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var grant mailbox.Grant
	if err := q.AcceptMail(ctx, req, prepared, func(g mailbox.Grant) error { grant = g; return nil }); err != nil {
		t.Fatal(err)
	}
	if grant.AccessLevel != "full_access" || grant.AccessGrant["source"] != "agent_default" {
		t.Fatalf("grant=%+v", grant)
	}
	path := filepath.Join(f.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("default: full_access"), []byte("default: default"), 1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Reload(ctx, "agents"); err != nil {
		t.Fatal(err)
	}
	f.runs.Finish("parent")
	m := mailbox.Mail{ID: "frozen-mail", Kind: mailbox.Letter, ToAgentKey: "mock-agent", ToChatID: prepared.ChatID, FromAgentKey: "mock-agent", FromChatID: "parent-chat", FromRunID: "parent", Message: "task", RunID: "frozen-mail-run", AccessLevel: grant.AccessLevel, AccessGrant: grant.AccessGrant}
	result, err := q.DeliverMail(ctx, m, controlscope.Scope{Transport: "ws", Boundary: "desktop"})
	if err != nil || !result.Accepted {
		t.Fatalf("delivery=%+v err=%v", result, err)
	}
	finished := waitRunTerminal(t, f.server, result.RunID)
	if finished.AccessLevel != "full_access" {
		t.Fatalf("mail permission recomputed: %+v", finished)
	}
}

func TestAgentDefaultAccessAdmissionPreservesRawInput(t *testing.T) {
	fixture := accessDefaultFixture(t, "full_access")
	for _, tc := range []struct{ extra, level, source string }{
		{"", "full_access", "agent_default"}, {`,"accessLevel":"default"`, "default", "explicit"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"agentKey":"mock-agent","message":"hello"`+tc.extra+`}`))
		prepared, err := prepareQueryForTest(fixture.server, req)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.Release != nil {
			defer prepared.Release()
		}
		if prepared.Session.AccessLevel != tc.level {
			t.Fatalf("level=%s want=%s", prepared.Session.AccessLevel, tc.level)
		}
		if bytes.Contains(prepared.Req.OriginalQuery, []byte("accessLevel")) != (tc.extra != "") {
			t.Fatal("raw input changed")
		}
	}
	def, _ := fixture.server.deps.Registry.AgentDefinition("mock-agent")
	if detail := fixture.server.buildAgentDetailResponse(def); detail.DefaultAccessLevel != "full_access" {
		t.Fatalf("metadata=%+v", detail)
	}
}

func TestChatStartTargetDefaultCanExceedParentAndRemainsIndependent(t *testing.T) {
	fixture := accessDefaultFixture(t, "full_access")
	bindTestRunControl(t, fixture.server, "parent", "ws", "desktop")
	_, parent, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", AccessLevel: "default"})
	defer fixture.runs.Finish("parent")
	req := contracts.RunStartRequest{AgentKey: "mock-agent", Message: "task", Input: map[string]any{"agentKey": "mock-agent", "message": "task"}, Origin: contracts.RunOrigin{AgentKey: "mock-agent", RunID: "parent", ToolID: "default-call"}}
	started, err := fixture.server.StartRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if started.AccessLevel != "full_access" {
		t.Fatalf("target default was capped: %+v", started)
	}
	parent.UpdateAccessLevel("auto_approve")
	waitRunTerminal(t, fixture.server, started.RunID)
	line, err := fixture.chats.LoadRunQuery(started.ChatID, started.RunID)
	if err != nil || line.AccessGrant["source"] != "agent_default" || line.AccessGrant["parentAccessLevel"] != "default" {
		t.Fatalf("audit=%+v err=%v", line, err)
	}
	if _, exists := line.Query["accessLevel"]; exists {
		t.Fatal("resolved permission polluted original query")
	}
	req.AccessLevel = "full_access"
	_, err = fixture.server.StartRun(context.Background(), req)
	var typed *contracts.RunToolError
	if !errors.As(err, &typed) || typed.Code != "run_start_access_level_exceeded" {
		t.Fatalf("explicit escalation accepted: %v", err)
	}
}
