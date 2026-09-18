package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"context"
	"strings"
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

func TestSelectionSteerFreezesNonVisionInput(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "run-a", ChatRoot: t.TempDir()}
	control := contracts.NewRunControl(context.Background(), session.RunID)
	control.SetSteerPreparer(e.steerPreparer(session, false))
	meta := map[string]any{"text": "selected original"}
	req := api.SteerRequest{RunID: session.RunID, ChatID: session.ChatID, Message: "explain", References: []api.Reference{{Type: "selection", Meta: meta, Path: "/untrusted/path"}}}
	if ok, err := control.PrepareAndEnqueueSteer(req); !ok || err != nil {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	meta["text"] = "mutated"
	got := control.DrainSteers()
	if len(got) != 1 || got[0].References[0].Meta["text"] != "selected original" || got[0].References[0].Path != "" {
		t.Fatalf("snapshot: %#v", got)
	}
	content, ok := got[0].PreparedMessages[0]["content"].(string)
	if !ok || !strings.Contains(content, "selected original") || strings.Contains(content, "mutated") {
		t.Fatalf("content: %#v", got[0].PreparedMessages)
	}
	if len(control.DrainSteers()) != 0 {
		t.Fatal("steer consumed twice")
	}
	req.References[0].Meta = map[string]any{"text": " "}
	if _, err := e.steerPreparer(session, false)(req); err == nil {
		t.Fatal("empty selection accepted")
	}
	req.References = append(req.References, api.Reference{Type: "file", URL: "image.png"})
	if _, err := e.steerPreparer(session, false)(req); err == nil {
		t.Fatal("mixed image accepted on nonvision")
	}
}
