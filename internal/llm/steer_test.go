package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"bytes"
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
