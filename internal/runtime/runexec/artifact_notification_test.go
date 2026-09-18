package runexec

import (
	"reflect"
	"testing"

	"agent-platform/internal/stream"
)

type artifactNotificationRecorder struct {
	types []string
	items []map[string]any
}

func (r *artifactNotificationRecorder) Broadcast(kind string, data map[string]any) {
	r.types = append(r.types, kind)
	r.items = append(r.items, data)
}

func TestArtifactPublishedProjectsEachArtifact(t *testing.T) {
	item := map[string]any{
		"artifactId": "a1", "name": "a.png", "type": "image", "mimeType": "image/png",
		"sizeBytes": 42, "sha256": "abc", "url": "artifacts/r/a.png", "path": "/private/source.png",
	}
	for _, raw := range []any{[]map[string]any{item, item}, []any{item, nil, "invalid", item}} {
		r := &artifactNotificationRecorder{}
		NotifyArtifactPublished(r, stream.EventData{Type: "artifact.publish", Timestamp: 1_700_000_000_000,
			Payload: map[string]any{"chatId": "c", "runId": "r", "toolId": "t", "artifacts": raw}})
		if !reflect.DeepEqual(r.types, []string{"artifact.published", "artifact.published"}) {
			t.Fatalf("unexpected notifications: %v", r.types)
		}
		want := map[string]any{"chatId": "c", "runId": "r", "artifactId": "a1", "name": "a.png",
			"type": "image", "mimeType": "image/png", "sizeBytes": 42, "sha256": "abc",
			"url": "artifacts/r/a.png", "publishedAt": int64(1_700_000_000_000)}
		for _, got := range r.items {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("payload = %#v, want %#v", got, want)
			}
		}
	}
}

func TestArtifactPublishedRejectsNonPublicationAndInvalidIdentity(t *testing.T) {
	for _, event := range []stream.EventData{
		{Type: "tool.result", Timestamp: 1_700_000_000_000},
		{Type: "artifact.publish", Timestamp: 0, Payload: map[string]any{"chatId": "c", "runId": "r"}},
		{Type: "artifact.publish", Timestamp: 1_700_000_000_000, Payload: map[string]any{"chatId": "c"}},
		{Type: "artifact.publish", Timestamp: 1_700_000_000_000, Payload: map[string]any{"chatId": "c", "runId": "r", "artifacts": []any{map[string]any{"name": "no-id"}}}},
	} {
		r := &artifactNotificationRecorder{}
		NotifyArtifactPublished(r, event)
		if len(r.items) != 0 {
			t.Fatalf("unexpected notifications for %#v", event)
		}
	}
}
