package server

import (
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestTeamAwaitingNotificationUsesPublicOwner(t *testing.T) {
	notifications := &recordingNotificationSink{}
	handleAwaitingLifecycle(RunExecutorParams{
		Session: contracts.QuerySession{
			ChatID: "chat-team", RunID: "run-team", AgentKey: hiddenTeamAgentKey("research"), TeamID: "research",
			RunOwner: contracts.TeamRunOwner("research", hiddenTeamAgentKey("research")),
		},
		Notifications: notifications,
	}, stream.EventData{Type: "awaiting.ask", Timestamp: testEpochMillis + 1, Payload: map[string]any{
		"awaitingId": "await-team", "runId": "run-team", "mode": "form",
	}}, &awaitingTracker{})
	payloads := notifications.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("notifications=%#v", payloads)
	}
	payload := payloads[0]
	if _, present := payload["ownerType"]; present || payload["teamId"] != "research" {
		t.Fatalf("Team awaiting owner=%#v", payload)
	}
	if _, leaked := payload["agentKey"]; leaked {
		t.Fatalf("Team awaiting leaked coordinator identity: %#v", payload)
	}
}
