package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"testing"
)

func TestImageSteerRejectsNonVisionAndWrongChat(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "run-a", ChatRoot: t.TempDir()}
	req := api.SteerRequest{RunID: "run-a", ChatID: "chat-a", Message: "look", References: []api.Reference{{URL: "a.png"}}}
	if _, err := e.steerPreparer(session, false)(req); err == nil {
		t.Fatal("non-vision model accepted image")
	}
	req.ChatID = "chat-b"
	if _, err := e.steerPreparer(session, true)(req); err == nil {
		t.Fatal("mismatched chat accepted")
	}
}
