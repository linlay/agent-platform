package contracts

import (
	"agent-platform/internal/contracts/queryinput"
	"context"
	"testing"
)

func TestMailHopOnlyIncreasesUntilUserInput(t *testing.T) {
	c := NewRunControl(context.Background(), "run")
	c.ConsumeMailInput(&queryinput.MailInput{Hop: 3}, nil)
	c.ConsumeMailInput(&queryinput.MailInput{Hop: 1}, nil)
	if c.MailHop() != 3 {
		t.Fatal("unrelated low-hop mail reset causal distance")
	}
	c.ConsumeMailInput(nil, &queryinput.Origin{Type: queryinput.OriginAgent})
	if c.MailHop() != 3 {
		t.Fatal("Agent input reset hop")
	}
	c.ConsumeMailInput(nil, &queryinput.Origin{Type: queryinput.OriginUser})
	if c.MailHop() != 0 {
		t.Fatal("user input did not reset hop")
	}
}
func TestDelegatedAnswerUpdatesHopBeforeWakingChild(t *testing.T) {
	c := NewRunControl(context.Background(), "child")
	c.ExpectSubmit(AwaitingSubmitContext{AwaitingID: "question", Mode: "question", ItemCount: 1})
	hop := 3
	ack := c.ResolveSubmit(queryinput.SubmitRequest{RunID: "child", AwaitingID: "question", MailHop: &hop})
	if !ack.Accepted || c.MailHop() != 3 {
		t.Fatalf("%#v hop=%d", ack, c.MailHop())
	}
}

func TestOnlyLatestHumanInputGrantsMailAddressEligibility(t *testing.T) {
	c := NewRunControl(context.Background(), "run")
	c.ConsumeMailInput(nil, nil, queryinput.Reference{Type: "chat", ID: "human-chat"})
	c.ConsumeMailInput(&queryinput.MailInput{Hop: 2}, &queryinput.Origin{Type: queryinput.OriginAgent}, queryinput.Reference{Type: "chat", ID: "agent-chat"})
	if got := c.UserChatReferences(); len(got) != 1 || got[0] != "human-chat" {
		t.Fatalf("Agent input changed user-address eligibility: %#v", got)
	}
	c.ConsumeMailInput(nil, nil)
	if len(c.UserChatReferences()) != 0 {
		t.Fatal("prior user references survived a newer direct input")
	}
}

func TestCompactIdleHookRunsAfterStateUnlock(t *testing.T) {
	c := NewRunControl(context.Background(), "run")
	called := false
	c.SetCompactIdle(func() {
		called = true
		if c.State() != RunLoopStateIdle {
			t.Fatal("hook saw compaction state")
		}
	})
	c.TransitionState(RunLoopStateCompacting)
	c.TransitionState(RunLoopStateIdle)
	if !called {
		t.Fatal("queued mail would remain asleep after compaction")
	}
}
