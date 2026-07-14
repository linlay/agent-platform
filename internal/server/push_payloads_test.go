package server

import "testing"

func TestLifecycleAndCatalogPushPayloadsUseSemanticTimeFields(t *testing.T) {
	started := runStartedPushPayload("run-1", "chat-1", "agent-1", 1_700_000_000_001)
	if started["startedAt"] != int64(1_700_000_000_001) || started["timestamp"] != nil {
		t.Fatalf("unexpected run.started payload %#v", started)
	}
	finished := runFinishedPushPayload("run-1", "chat-1", 1_700_000_000_002)
	if finished["finishedAt"] != int64(1_700_000_000_002) || finished["timestamp"] != nil {
		t.Fatalf("unexpected run.finished payload %#v", finished)
	}
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
