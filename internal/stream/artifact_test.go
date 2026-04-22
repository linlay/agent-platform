package stream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatcherEmitsBatchedArtifactPublish(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(ArtifactPublish{
		ChatID: "chat_1",
		RunID:  "run_1",
		Artifacts: []map[string]any{
			{
				"artifactId": "artifact_1",
				"type":       "file",
				"name":       "report.md",
				"mimeType":   "text/markdown",
				"sizeBytes":  123,
				"url":        "/api/resource?file=chat_1%2Freport.md",
			},
			{
				"artifactId": "artifact_2",
				"type":       "file",
				"name":       "summary.txt",
				"mimeType":   "text/plain",
				"sizeBytes":  45,
				"url":        "/api/resource?file=chat_1%2Fsummary.txt",
			},
		},
	})

	assertEventTypes(t, events, "artifact.publish")
	payload := events[0].Data()
	if payload.String("chatId") != "chat_1" || payload.String("runId") != "run_1" {
		t.Fatalf("unexpected routing fields %#v", payload)
	}
	if got, ok := payload.Value("artifactCount").(int); !ok || got != 2 {
		t.Fatalf("expected artifactCount=2, got %#v", payload.Value("artifactCount"))
	}
	artifacts, ok := payload.Value("artifacts").([]map[string]any)
	if !ok || len(artifacts) != 2 {
		t.Fatalf("expected two artifacts, got %#v", payload.Value("artifacts"))
	}
	if artifacts[0]["artifactId"] != "artifact_1" || artifacts[1]["artifactId"] != "artifact_2" {
		t.Fatalf("unexpected artifacts payload %#v", artifacts)
	}
}

func TestEventDataMarshalsArtifactPublishWithContractKeyOrder(t *testing.T) {
	event := NewEvent("artifact.publish", map[string]any{
		"chatId":        "chat_1",
		"runId":         "run_1",
		"artifactCount": 2,
		"artifacts": []map[string]any{
			{
				"artifactId": "artifact_1",
				"type":       "file",
				"name":       "report.md",
				"mimeType":   "text/markdown",
				"sizeBytes":  123,
				"sha256":     "abc123",
				"url":        "/api/resource?file=chat_1%2Freport.md",
			},
			{
				"artifactId": "artifact_2",
				"type":       "file",
				"name":       "summary.txt",
				"mimeType":   "text/plain",
				"sizeBytes":  45,
				"url":        "/api/resource?file=chat_1%2Fsummary.txt",
			},
		},
	})
	event.Seq = 9

	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":9`,
		`"type":"artifact.publish"`,
		`"chatId":"chat_1"`,
		`"runId":"run_1"`,
		`"artifactCount":2`,
		`"artifacts":[`,
		`"timestamp":`,
	}
	prev := -1
	for _, part := range order {
		idx := strings.Index(text, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, text)
		}
		if idx <= prev {
			t.Fatalf("expected ordered keys in %s", text)
		}
		prev = idx
	}
	if !strings.Contains(text, `"artifactId":"artifact_1"`) || !strings.Contains(text, `"artifactId":"artifact_2"`) {
		t.Fatalf("expected artifact ids in payload, got %s", text)
	}
}
