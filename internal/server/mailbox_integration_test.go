package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runops"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/stream"
)

func TestMailboxStartsDetachedRunAndReturnsCompleteResultToWaitingChat(t *testing.T) {
	answer := strings.Repeat("complete result ", 80)
	var mu sync.Mutex
	var requests []map[string]any
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		writeProviderSSE(t, w, string(chunk), "[DONE]")
	})
	store, err := mailbox.Open(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	box := mailbox.New(ctx, store, f.cfg.Paths.ChatsDir, 4, nil)
	t.Cleanup(func() {
		cancel()
		if err := box.Close(); err != nil {
			t.Error(err)
		}
	})
	query := testQueryService(f.server)
	query.BindMailbox(box)
	f.server.deps.Mailbox = box
	_, _, err = f.chats.EnsureChatWithSourceAndMode("mail-parent", "mock-agent", "parent", "query:alice", "GENERAL")
	if err != nil {
		t.Fatal(err)
	}
	session := contracts.QuerySession{RunID: "parent-run", ChatID: "mail-parent", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent"), AccessLevel: "ask_for_approval"}
	_, parent, _ := f.runs.Register(ctx, session)
	defer f.runs.Finish(session.RunID)
	scope := controlscope.Scope{Transport: "http", Subject: "alice"}
	f.runs.BindRunControlScope(session.RunID, scope)
	parent.SetSteerPreparer(func(r queryinput.SteerRequest) (queryinput.SteerRequest, error) {
		r.PreparedMessages = []map[string]any{{"role": "user", "content": box.Header(r.Mail.ID) + r.Message}}
		return r, nil
	})
	handler := runops.NewToolHandler(query, f.runs).WithMailbox(box)
	exec := &contracts.ExecutionContext{Session: session, RunControl: parent, CurrentToolID: "send-1"}
	result, err := handler.Invoke(ctx, "chat_send", map[string]any{"message": "write a complete report", "newChat": true, "chatName": "Mailbox report"}, exec)
	if err != nil || result.Error != "" {
		t.Fatalf("send: %#v %v", result, err)
	}
	id, _ := result.Structured["mailId"].(string)
	if id == "" {
		t.Fatalf("missing mail receipt: %#v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	var original mailbox.Mail
	for time.Now().Before(deadline) {
		original, _ = store.Get(id)
		if original.Status == mailbox.Done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if original.Status != mailbox.Done {
		t.Fatalf("letter did not complete: %#v", original)
	}
	reply, err := store.Get(original.AnsweredBy)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Message != answer || reply.Kind != mailbox.Reply {
		t.Fatalf("reply is not complete result: %#v", reply)
	}
	select {
	case <-parent.SteerAvailable():
	case <-time.After(3 * time.Second):
		t.Fatal("reply did not wake parent")
	}
	steers := parent.DrainSteers()
	if len(steers) != 1 || steers[0].Mail == nil || steers[0].Mail.Kind != "reply" {
		t.Fatalf("reply input: %#v", steers)
	}
	writer := chat.NewStepWriter(f.chats, session.ChatID, session.RunID, "GENERAL")
	payload := map[string]any{"runId": session.RunID, "chatId": session.ChatID, "steerId": steers[0].SteerID, "role": "user", "message": steers[0].Message, "origin": steers[0].Origin.Payload()}
	steers[0].Mail.Payload(payload)
	writer.OnEvent(stream.EventData{Type: "request.steer", Timestamp: time.Now().UnixMilli(), Payload: payload})
	writer.Flush()
	if err := box.Consumed(steers[0].Mail, session.RunID); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.runs.RunControlScope(original.RunID); !ok || got != scope {
		t.Fatalf("control scope: %#v %v", got, ok)
	}
	queryLine, err := f.chats.LoadRunQuery(original.ToChatID, original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if queryLine == nil || queryLine.MailInput == nil || queryLine.ID != id {
		t.Fatalf("mail provenance missing: %#v", queryLine)
	}
	encoded, _ := json.Marshal(queryLine.Messages)
	if strings.Contains(string(encoded), "[Letter ") {
		t.Fatal("envelope persisted into original messages")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("model calls=%d", len(requests))
	}
	sent, _ := json.Marshal(requests[0]["messages"])
	if !strings.Contains(string(sent), "[Letter "+id) {
		t.Fatalf("model did not receive envelope: %s", sent)
	}
	if _, err := f.chats.LoadChat(session.ChatID); err != nil {
		t.Fatalf("generated reply steer did not preserve Chat schema: %v", err)
	}
}

func TestMailboxAskSubmitUsesLivePermissionAndPreservesHumanBoundary(t *testing.T) {
	f := newTestFixtureWithModelHandler(t, nil)
	store, err := mailbox.Open(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	box := mailbox.New(ctx, store, f.cfg.Paths.ChatsDir, 4, nil)
	t.Cleanup(func() { cancel(); box.Close() })
	query := testQueryService(f.server)
	query.BindMailbox(box)
	parentSession := contracts.QuerySession{RunID: "parent", ChatID: "parent-chat", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent"), AccessLevel: "ask_for_approval"}
	_, parent, _ := f.runs.Register(ctx, parentSession)
	childSession := contracts.QuerySession{RunID: "child", ChatID: "child-chat", AgentKey: "mock-agent", Subject: "alice", RunOwner: contracts.AgentRunOwner("mock-agent"), AccessLevel: "ask_for_approval"}
	_, child, _ := f.runs.Register(ctx, childSession)
	defer f.runs.Finish("parent")
	defer f.runs.Finish("child")
	parent.RaiseMailHop(4)
	exec := &contracts.ExecutionContext{Session: parentSession, RunControl: parent, CurrentToolID: "answer"}
	for _, tc := range []struct {
		id, mode, source string
		param            queryinput.SubmitParam
		params           queryinput.SubmitParams
		denied           bool
	}{
		{id: "question", mode: "question", source: "ask_user_question", params: queryinput.SubmitParams{json.RawMessage(`{"answer":"skip"}`)}},
		{id: "approval", mode: "approval", source: "tool_approval", params: queryinput.SubmitParams{json.RawMessage(`{"decision":"approve"}`)}, denied: true},
		{id: "review", mode: "form", source: "tool_approval", param: queryinput.SubmitParam{"decision": "approve", "data": map[string]any{}}, denied: true},
		{id: "reject", mode: "approval", source: "tool_approval", params: queryinput.SubmitParams{json.RawMessage(`{"decision":"reject"}`)}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			child.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: tc.id, Mode: tc.mode, ItemCount: 1})
			ask := mailbox.Mail{ID: "ask-" + tc.id, Kind: mailbox.Ask, Status: mailbox.Delivered, ToChatID: parentSession.ChatID, ToAgentKey: "mock-agent", FromChatID: childSession.ChatID, FromAgentKey: "mock-agent", FromRunID: "child", Subject: "alice", AwaitingID: tc.id, AwaitingMode: tc.mode, AwaitingSource: tc.source}
			if _, _, err := store.Accept(ask); err != nil {
				t.Fatal(err)
			}
			req := queryinput.SubmitRequest{RunID: "child", AwaitingID: tc.id, Param: tc.param, Params: tc.params}
			response, err := box.Submit(ctx, req, exec)
			if tc.denied {
				if err == nil {
					t.Fatal("mandatory tool review delegated")
				}
				if _, ok := child.LookupAwaiting(tc.id); !ok {
					t.Fatal("rejection consumed waiting item")
				}
				return
			}
			if err != nil || !response.Accepted {
				t.Fatalf("submit: %#v %v", response, err)
			}
			if child.MailHop() != 5 {
				t.Fatalf("answer did not update hop before resume: %d", child.MailHop())
			}
		})
	}
	wrong := *exec
	wrong.Session.ChatID = "unrelated"
	if _, err := box.Submit(ctx, queryinput.SubmitRequest{RunID: "child", AwaitingID: "approval"}, &wrong); err == nil {
		t.Fatal("unrelated Chat answered an ask")
	}
}

func TestMailboxHTTPNeverListsOtherPrincipalsOrLetsThemRedeliver(t *testing.T) {
	f := newTestFixtureWithModelHandler(t, nil)
	store, err := mailbox.Open(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	box := mailbox.New(ctx, store, f.cfg.Paths.ChatsDir, 4, nil)
	t.Cleanup(func() { cancel(); box.Close() })
	f.server.deps.Mailbox = box
	for _, subject := range []string{"alice", "bob", "*"} {
		_, _, err := store.Accept(mailbox.Mail{ID: "mail-" + subject, Kind: mailbox.Letter, Status: mailbox.Suspended, Subject: subject, Message: "secret-" + subject, CreatedAt: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/mails", nil)
	req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: "alice"}))
	rec := httptest.NewRecorder()
	f.server.handleMails(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "secret-alice") || strings.Contains(rec.Body.String(), "secret-bob") || strings.Contains(rec.Body.String(), "secret-*") {
		t.Fatalf("subject filter: %d %s", rec.Code, rec.Body.String())
	}
	action := httptest.NewRequest(http.MethodPost, "/api/mails/action", strings.NewReader(`{"mailId":"mail-bob","action":"redeliver"}`))
	action = action.WithContext(WithPrincipal(action.Context(), &Principal{Subject: "alice"}))
	denied := httptest.NewRecorder()
	f.server.handleMailAction(denied, action)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-principal action: %d %s", denied.Code, denied.Body.String())
	}
}
