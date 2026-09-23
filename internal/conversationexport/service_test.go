package conversationexport

import (
	"errors"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

type exportReaderStub struct {
	summary       *chat.Summary
	detail        chat.Detail
	manifestErr   error
	manifestReads int
}

func (s *exportReaderStub) Summary(string) (*chat.Summary, error) { return s.summary, nil }
func (s *exportReaderStub) LoadConversationHistory(string) (chat.Detail, error) {
	return s.detail, nil
}
func (s *exportReaderStub) PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error) {
	s.manifestReads++
	return nil, s.manifestErr
}
func (s *exportReaderStub) ChatDir(string) string { return "" }

func TestMarkdownExportDoesNotReadArtifactManifest(t *testing.T) {
	const capturedAt int64 = 1_790_000_000_100
	reader := &exportReaderStub{
		summary: &chat.Summary{ChatID: "chat-1", ChatName: "Export", CreatedAt: capturedAt - 100},
		detail: chat.Detail{Events: []stream.EventData{
			{Type: "request.query", Timestamp: capturedAt - 90, Payload: map[string]any{"runId": "run-1", "message": "question"}},
			{Type: "content.snapshot", Timestamp: capturedAt - 80, Payload: map[string]any{"runId": "run-1", "text": "answer"}},
			{Type: "run.complete", Timestamp: capturedAt - 70, Payload: map[string]any{"runId": "run-1"}},
		}},
		manifestErr: errors.New("corrupt manifest"),
	}
	body, title, err := (Service{Chats: reader}).Markdown("chat-1", capturedAt, "en-US")
	if err != nil || len(body) == 0 || title != "Export" {
		t.Fatalf("body=%q title=%q error=%v", body, title, err)
	}
	if reader.manifestReads != 0 {
		t.Fatalf("manifest reads=%d", reader.manifestReads)
	}
	if _, err := (Service{Chats: reader}).Snapshot("chat-1", capturedAt, "en-US"); !errors.Is(err, reader.manifestErr) {
		t.Fatalf("snapshot error=%v", err)
	}
}
