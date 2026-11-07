package conversation

import (
	"errors"
	"testing"
)

func TestBotPrimaryCannotPromoteWorkerOrReplaceExistingMain(t *testing.T) {
	service, store, _, _ := primaryFixture(t)
	service.IsBotAgent = func(key string) bool { return key == "agent" }
	if _, err := store.SetChatPrimary("agent", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetChatPrimary("old"); err != nil {
		t.Fatal("idempotent pointer rejected", err)
	}
	if _, err := service.SetChatPrimary("next"); !errors.Is(err, ErrBotPrimaryFixed) {
		t.Fatal("worker promotion accepted", err)
	}
	if err := service.PublishPrimaryChat("agent", "next"); !errors.Is(err, ErrBotPrimaryFixed) {
		t.Fatal("alternate publication bypassed restriction", err)
	}
	if err := store.DeleteChat("old"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetChatPrimary("next"); !errors.Is(err, ErrBotPrimaryFixed) {
		t.Fatal("worker promoted after main removal", err)
	}
}

func TestBotOpenedWorkersDoNotDependOnMailbox(t *testing.T) {
	service, store, _, _ := primaryFixture(t)
	service.IsBotAgent = func(key string) bool { return key == "agent" }
	if _, err := store.SetChatPrimary("agent", "old"); err != nil {
		t.Fatal(err)
	}
	service.MailboxOpened = func(string, string) ([]map[string]any, error) {
		t.Fatal("BOT worker lookup read mail.db")
		return nil, nil
	}
	rows, err := service.ControlOpened(ControlCaller{Subject: "user", ChatID: "old", AgentKey: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ChatID == "old" {
			t.Fatal("main listed as worker")
		}
		if row.ChatID == "next" {
			found = true
		}
	}
	if !found {
		t.Fatal("worker missing after mailbox reset")
	}
	if _, err := service.ResolveControlChat(ControlCaller{Subject: "user", ChatID: "external", AgentKey: "external"}, "next", false); err == nil {
		t.Fatal("external Agent discovered BOT worker")
	}
	if _, err := service.ResolveControlChat(ControlCaller{Subject: "user", ChatID: "external", AgentKey: "external"}, "old", false); err != nil {
		t.Fatal("BOT main hidden", err)
	}
}
