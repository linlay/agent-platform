package query

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/runstate"
	"testing"
)

func TestExternalAgentCannotCreateOrAddressBotWorkers(t *testing.T) {
	service := NewService(Dependencies{Registry: primaryRoleRegistry{def: catalog.AgentDefinition{Key: "bot", Mode: "BOT"}}, Chats: primaryRoleStore{PrimaryStore: primaryPointer{}}, Runs: runstate.NewManager()})
	for _, tc := range []struct {
		chat    string
		newChat bool
	}{{"", true}, {"worker", false}} {
		req := mailbox.Request{NewChat: tc.newChat, Start: contracts.RunStartRequest{AgentKey: "bot", ChatID: tc.chat, Message: "work", Origin: contracts.RunOrigin{AgentKey: "external", ChatID: "external-main", RunID: "r", ToolID: "t", Subject: "owner"}}}
		_, err := service.PrepareMail(t.Context(), req)
		e, ok := err.(*contracts.RunToolError)
		if !ok || e.Code != "bot_main_chat_required" {
			t.Fatalf("worker address accepted: %+v err=%v", tc, err)
		}
	}
}
