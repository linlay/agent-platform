package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageSteerUsesReferencesForNonVisionAndRejectsWrongChat(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "run-a", ChatRoot: t.TempDir()}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.ChatRoot, "a.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	req := api.SteerRequest{RunID: "run-a", ChatID: "chat-a", Message: "", References: []api.Reference{{URL: "a.png"}}}
	got, err := e.steerPreparer(session, false)(req)
	if err != nil {
		t.Fatal(err)
	}
	content, ok := got.PreparedMessages[0]["content"].(string)
	if !ok || !strings.Contains(content, got.References[0].Path) || !strings.Contains(content, "image/png") {
		t.Fatalf("non-vision input must contain a tool-readable image reference: %#v", got)
	}
	vision, err := e.steerPreparer(session, true)(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(vision.PreparedMessages)
	if !bytes.Contains(encoded, []byte("data:image/png;base64,")) {
		t.Fatalf("vision input lost image: %s", encoded)
	}
	req.ChatID = "chat-b"
	if _, err := e.steerPreparer(session, true)(req); err == nil {
		t.Fatal("mismatched chat accepted")
	}
}

func TestFileOnlySteerOnNonVisionModel(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "run-a", ChatRoot: t.TempDir()}
	for _, name := range []string{"page.html", "notes.md"} {
		if err := os.WriteFile(filepath.Join(session.ChatRoot, name), []byte("example"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := e.steerPreparer(session, false)(api.SteerRequest{RunID: session.RunID, References: []api.Reference{{URL: name}}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(got.PreparedMessages)
		if len(got.PreparedMessages) != 1 || !strings.Contains(string(encoded), name) || strings.Contains(string(encoded), "image_url") {
			t.Fatalf("%s", encoded)
		}
	}
}

func TestSelectionSteerFreezesNonVisionInput(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "run-a", ChatRoot: t.TempDir()}
	control := contracts.NewRunControl(context.Background(), session.RunID)
	control.SetSteerPreparer(e.steerPreparer(session, false))
	req := api.SteerRequest{RunID: session.RunID, ChatID: session.ChatID, Message: "explain", References: []api.Reference{{Type: "selection", Text: "selected original", Path: "/untrusted/path"}}}
	if ok, err := control.PrepareAndEnqueueSteer(req); !ok || err != nil {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	req.References[0].Text = "mutated"
	got := control.DrainSteers()
	if len(got) != 1 || got[0].References[0].Text != "selected original" || got[0].References[0].Path != "" {
		t.Fatalf("snapshot: %#v", got)
	}
	content, ok := got[0].PreparedMessages[0]["content"].(string)
	if !ok || !strings.Contains(content, "selected original") || strings.Contains(content, "mutated") {
		t.Fatalf("content: %#v", got[0].PreparedMessages)
	}
	if len(control.DrainSteers()) != 0 {
		t.Fatal("steer consumed twice")
	}
	req.References[0].Text = " "
	if _, err := e.steerPreparer(session, false)(req); err == nil {
		t.Fatal("empty selection accepted")
	}
}

func TestHistorySteerReloadsImagesAndHandlesUnavailableReferences(t *testing.T) {
	e := &LLMAgentEngine{}
	session := contracts.QuerySession{ChatID: "chat-a", RunID: "next-run", ChatRoot: t.TempDir()}
	path := filepath.Join(session.ChatRoot, "a.png")
	writeImage := func(size int) {
		t.Helper()
		var data bytes.Buffer
		if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, size, size))); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw := map[string]any{"role": "user", "content": "fallback", "_steerKey": "old/steer", chat.SteerHistoryInputKey: map[string]any{
		"chatId": "chat-a", "runId": "old", "message": "look", "references": []api.Reference{
			{Type: "file", URL: "a.png", Path: "/untrusted/path"},
			{Type: "selection", Text: "selected", Annotation: "explain"},
		},
	}}
	encode := func(vision bool) string {
		t.Helper()
		got := e.materializeHistorySteer(raw, session, vision)
		if got["_steerKey"] != "old/steer" {
			t.Fatal("lost compaction identity")
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "/untrusted/path") || got[chat.SteerHistoryInputKey] != nil {
			t.Fatalf("unresolved metadata: %s", data)
		}
		return string(data)
	}
	writeImage(1)
	first := encode(true)
	if !strings.Contains(first, "data:image/png;base64,") {
		t.Fatal(first)
	}
	writeImage(2)
	if next := encode(true); next == first || !strings.Contains(next, "data:image/png;base64,") {
		t.Fatal("image was not reloaded")
	}
	if nonvision := encode(false); strings.Contains(nonvision, "image_url") || !strings.Contains(nonvision, "a.png") {
		t.Fatal(nonvision)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing := encode(true)
	for _, want := range []string{"附件已不可用", "a.png", "selected", "explain", "look"} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing %s: %s", want, missing)
		}
	}
	legacy := map[string]any{"role": "user", "content": "legacy snapshot"}
	if got := e.materializeHistorySteer(legacy, session, true); got["content"] != "legacy snapshot" {
		t.Fatal(got)
	}
}
