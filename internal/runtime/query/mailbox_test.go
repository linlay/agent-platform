package query

import (
	"context"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	runtimetypes "agent-platform/internal/runtime/types"
)

func testMailService(t *testing.T) (*Service, *mailbox.Service) {
	t.Helper()
	root := t.TempDir()
	store, err := mailbox.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	box := mailbox.New(ctx, store, root, 4, nil)
	t.Cleanup(func() {
		cancel()
		_ = box.Close()
	})
	return &Service{mailbox: box}, box
}

func TestOnlyLetterRunsAreDerivedFromTheSender(t *testing.T) {
	s, _ := testMailService(t)
	origin := &queryinput.Origin{Type: queryinput.OriginAgent, AgentKey: "worker", ChatID: "worker-chat", RunID: "worker-run"}
	for kind, derived := range map[string]bool{"letter": true, "reply": false, "notify": false, "ask": false} {
		var session contracts.QuerySession
		s.bindMailboxSession(runtimetypes.QueryCommand{RunID: "r", ChatID: "lead-chat", Origin: origin, Mail: &queryinput.MailInput{ID: "m", Kind: kind, Attempt: 1}}, &session)
		if (session.RunOrigin != nil) != derived {
			t.Fatalf("%s: RunOrigin=%v", kind, session.RunOrigin)
		}
	}
}

func TestUnpersistedCompletionStillAnswersMail(t *testing.T) {
	s, box := testMailService(t)
	letter := mailbox.Mail{ID: "m1", Kind: mailbox.Letter, Status: mailbox.Delivered, ToAgentKey: "worker", ToChatID: "worker-chat", OriginType: "agent",
		FromAgentKey: "lead", FromChatID: "lead-chat", FromRunID: "lead-run", FromToolID: "t1", Subject: "user", Message: "work", Hop: 1, Delivery: "started", RunID: "worker-run", Attempts: 1, RequestDigest: "d"}
	if _, _, err := box.Store.Accept(letter); err != nil {
		t.Fatal(err)
	}
	var session contracts.QuerySession
	s.bindMailboxSession(runtimetypes.QueryCommand{RunID: "worker-run", ChatID: "worker-chat"}, &session)
	session.RuntimeComplete("worker-run", "complete", "the result", false)
	got, err := box.Store.Get("m1")
	if err != nil || got.Status != mailbox.Done || got.AnsweredBy == "" {
		t.Fatalf("letter: %#v %v", got, err)
	}
	if reply, err := box.Store.Get(got.AnsweredBy); err != nil || reply.Message != "the result" {
		t.Fatalf("reply: %#v %v", reply, err)
	}
}
