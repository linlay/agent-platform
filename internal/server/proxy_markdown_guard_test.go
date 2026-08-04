package server

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/stream"
)

func TestProxyRecorderFiltersMarkdownDestinationsAcrossUpstreamDeltas(t *testing.T) {
	recorder := &proxyEventRecorder{
		req:            api.QueryRequest{ChatID: "chat_proxy"},
		markdownGuards: map[string]*stream.MarkdownDestinationGuard{},
		markdownText:   map[string]*strings.Builder{},
	}
	start := stream.EventData{Type: "content.start", Payload: map[string]any{"contentId": "content_1"}}
	recorder.sanitizeMarkdownEvent(&start)

	first := stream.EventData{Type: "content.delta", Payload: map[string]any{"contentId": "content_1", "delta": "![poster](/api/reso"}}
	recorder.sanitizeMarkdownEvent(&first)
	second := stream.EventData{Type: "content.delta", Payload: map[string]any{"contentId": "content_1", "delta": "urce?file=chat_proxy%2Fposter.png)"}}
	recorder.sanitizeMarkdownEvent(&second)
	end := stream.EventData{Type: "content.end", Payload: map[string]any{"contentId": "content_1"}}
	recorder.sanitizeMarkdownEvent(&end)

	for _, event := range []stream.EventData{first, second, end} {
		if strings.Contains(event.String("delta"), "/api/resource") || strings.Contains(event.String("text"), "/api/resource") {
			t.Fatalf("forbidden transport URL leaked in %#v", event.Payload)
		}
	}
	if first.String("delta") != "" || second.String("delta") != "资源地址不合规" || end.String("text") != "资源地址不合规" {
		t.Fatalf("unexpected sanitized events first=%#v second=%#v end=%#v", first.Payload, second.Payload, end.Payload)
	}
}

func TestNormalizeProxyArtifactURLsKeepsOnlyChatScopeURLs(t *testing.T) {
	event := stream.EventData{Type: "artifact.publish", Payload: map[string]any{
		"artifacts": []map[string]any{
			{"artifactId": "legacy", "url": "/api/resource?file=chat_proxy%2Fartifacts%2Frun_1%2F%E5%A4%8F%E6%97%A5%20%E6%B5%B7%E6%8A%A5.png"},
			{"artifactId": "relative", "url": "artifacts/run_1/report.pdf"},
			{"artifactId": "wrong-chat", "url": "/api/resource?file=chat_other%2Fsecret.png"},
		},
	}}
	normalizeProxyArtifactURLs(&event, "chat_proxy")
	items, ok := event.Payload["artifacts"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("normalized artifacts=%#v", event.Payload["artifacts"])
	}
	if items[0]["url"] != "artifacts/run_1/%E5%A4%8F%E6%97%A5%20%E6%B5%B7%E6%8A%A5.png" || items[1]["url"] != "artifacts/run_1/report.pdf" {
		t.Fatalf("normalized artifact URLs=%#v", items)
	}
	if event.Payload["artifactCount"] != 2 {
		t.Fatalf("artifactCount=%#v", event.Payload["artifactCount"])
	}
}
