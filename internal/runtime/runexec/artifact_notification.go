package runexec

import (
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

// NotifyArtifactPublished projects a live, persisted publication into one
// notification per artifact. Call only from the producer lifecycle, never from
// attach or history replay. The notification transport restricts this event to
// the authenticated Desktop Main connection.
func NotifyArtifactPublished(sink contracts.NotificationSink, event stream.EventData) {
	if sink == nil || event.Type != "artifact.publish" {
		return
	}
	chatID, runID := strings.TrimSpace(event.String("chatId")), strings.TrimSpace(event.String("runId"))
	if chatID == "" || runID == "" || timecontract.ValidateEpochMillis(event.Timestamp, "publishedAt", "artifact.published") != nil {
		return
	}
	var items []map[string]any
	switch raw := event.Value("artifacts").(type) {
	case []map[string]any:
		items = raw
	case []any:
		for _, value := range raw {
			if item, ok := value.(map[string]any); ok {
				items = append(items, item)
			}
		}
	}
	for _, item := range items {
		artifactID := strings.TrimSpace(contracts.AnyStringNode(item["artifactId"]))
		if artifactID == "" {
			continue
		}
		// Explicit public fields prevent tool paths and stream metadata leaking
		// into the push. URLs have already been projected by the live pipeline.
		payload := map[string]any{
			"chatId": chatID, "runId": runID, "artifactId": artifactID,
			"publishedAt": event.Timestamp,
			"sizeBytes":   contracts.AnyIntNode(item["sizeBytes"]),
		}
		for _, key := range []string{"name", "type", "mimeType", "sha256", "url"} {
			payload[key] = contracts.AnyStringNode(item[key])
		}
		sink.Broadcast("artifact.published", payload)
	}
}
