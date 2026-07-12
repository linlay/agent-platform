package server

import (
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestRunEventProcessorExcludesMemberTaskRepliesFromRootResult(t *testing.T) {
	var assistant strings.Builder
	processor := runEventProcessor{assistantText: &assistant}
	processor.decorate(&stream.EventData{Type: "content.delta", Payload: map[string]any{
		"delta": "member reply", "taskId": "task-1", "presentation": "reply",
	}})
	processor.decorate(&stream.EventData{Type: "content.delta", Payload: map[string]any{
		"delta": "Team summary",
	}})
	if got := assistant.String(); got != "Team summary" {
		t.Fatalf("root assistant result=%q, want Team summary", got)
	}
}

func TestRunEventProcessorAggregatesCoordinatorAndMemberUsage(t *testing.T) {
	var runUsage chat.UsageData
	processor := runEventProcessor{
		runUsage:             &runUsage,
		aggregateUsageByTask: true,
	}
	processor.decorate(&stream.EventData{Type: "usage.snapshot", Payload: map[string]any{
		"taskId": "member-1",
		"usage": map[string]any{
			"current": map[string]any{"modelKey": "member-model", "promptTokens": 10, "completionTokens": 2, "totalTokens": 12},
			"run":     map[string]any{"promptTokens": 10, "completionTokens": 2, "totalTokens": 12, "llmChatCompletionCount": 1},
		},
	}})
	processor.decorate(&stream.EventData{Type: "usage.snapshot", Payload: map[string]any{
		"usage": map[string]any{
			"current": map[string]any{"modelKey": "coordinator-model", "promptTokens": 5, "completionTokens": 1, "totalTokens": 6},
			"run":     map[string]any{"promptTokens": 5, "completionTokens": 1, "totalTokens": 6, "llmChatCompletionCount": 1},
		},
	}})
	if runUsage.PromptTokens != 15 || runUsage.CompletionTokens != 3 || runUsage.TotalTokens != 18 || runUsage.LlmChatCompletionCount != 2 {
		t.Fatalf("aggregated Team usage=%#v", runUsage)
	}
	if runUsage.ModelKey != "" {
		t.Fatalf("mixed-model Team usage retained root modelKey %q", runUsage.ModelKey)
	}
}

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
	if payload["ownerType"] != "team" || payload["teamId"] != "research" {
		t.Fatalf("Team awaiting owner=%#v", payload)
	}
	if _, leaked := payload["agentKey"]; leaked {
		t.Fatalf("Team awaiting leaked coordinator identity: %#v", payload)
	}
}
