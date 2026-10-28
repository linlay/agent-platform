package query

import "testing"

func TestRunLifecyclePushPayloadsUseSemanticTimeFields(t *testing.T) {
	started := runStartedPushPayload("run-1", "chat-1", "agent-1", 1_700_000_000_001)
	if started["startedAt"] != int64(1_700_000_000_001) || started["timestamp"] != nil {
		t.Fatalf("unexpected run.started payload %#v", started)
	}
	finished := runFinishedPushPayload("run-1", "chat-1", "complete", 1_700_000_000_002)
	if finished["finishedAt"] != int64(1_700_000_000_002) || finished["status"] != "completed" ||
		finished["finishReason"] != "complete" || finished["timestamp"] != nil {
		t.Fatalf("unexpected run.finished payload %#v", finished)
	}
}

func TestRunFinishedPushPayloadMapsTerminalStatus(t *testing.T) {
	for finishReason, wantStatus := range map[string]string{
		"complete": "completed",
		"error":    "failed",
		"cancel":   "interrupted",
	} {
		payload := runFinishedPushPayload("run-1", "chat-1", finishReason, 1_700_000_000_002)
		if payload["status"] != wantStatus || payload["finishReason"] != finishReason {
			t.Fatalf("finishReason %q produced %#v", finishReason, payload)
		}
	}
	payload := runFinishedPushPayload("run-1", "chat-1", "", 1_700_000_000_002)
	if payload["status"] != "failed" || payload["finishReason"] != "error" {
		t.Fatalf("missing finishReason must fail closed: %#v", payload)
	}
}
