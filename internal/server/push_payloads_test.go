package server

import "testing"

func TestCatalogPushPayloadUsesUpdatedAt(t *testing.T) {
	catalog := catalogUpdatedPushPayload("agents", 1_700_000_000_003)
	if catalog["updatedAt"] != int64(1_700_000_000_003) || catalog["timestamp"] != nil {
		t.Fatalf("unexpected catalog.updated payload %#v", catalog)
	}
}

func TestChatCreatedPayloadUsesCreatedAt(t *testing.T) {
	payload := chatCreatedPayload("chat-1", "name", "agent-1", 1_700_000_000_001, "query:app")
	if payload["createdAt"] != int64(1_700_000_000_001) || payload["timestamp"] != nil {
		t.Fatalf("unexpected chat.created payload %#v", payload)
	}
}
