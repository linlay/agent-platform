package ws

import "testing"

type nestedTimestampPayload struct {
	Timestamp int64 `json:"timestamp"`
}

func TestPushTimestampContract(t *testing.T) {
	types := []string{
		"connected", "heartbeat", "auth.expiring", "run.started", "run.finished",
		"chat.created", "chat.updated", "chat.unread", "chat.read", "chat.read_all",
		"chat.deleted", "chat.renamed", "chat.archived", "archive.restored", "archive.deleted",
		"catalog.updated", "awaiting.asking", "awaiting.answered", "resource.pushed",
	}
	for _, eventType := range types {
		t.Run(eventType, func(t *testing.T) {
			data := map[string]any{"nested": nestedTimestampPayload{Timestamp: 1_700_000_000_000}}
			allowed := allowsPushTimestamp(eventType, data)
			if eventType == "heartbeat" && !allowed {
				t.Fatalf("heartbeat must allow timestamp")
			}
			if eventType != "heartbeat" && allowed {
				t.Fatalf("%s must reject timestamp: %#v", eventType, data)
			}
		})
	}
}
