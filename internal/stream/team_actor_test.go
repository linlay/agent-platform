package stream

import "testing"

func TestTeamMemberContentCarriesActorAndPresentation(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{RunID: "run-team", ChatID: "chat-team", TeamID: "team-a", OwnerType: "team"})
	events := dispatcher.Dispatch(ContentDelta{
		ContentID: "content-a", Delta: "hello", TaskID: "task-a",
		ActorType: "agent", TeamID: "team-a", AgentKey: "writer", Presentation: "reply",
	})
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if event.Payload["teamId"] != "team-a" || event.Payload["agentKey"] != "writer" || event.Payload["presentation"] != "reply" {
			t.Fatalf("missing Team metadata on %s: %#v", event.Type, event.Payload)
		}
		actor, _ := event.Payload["actor"].(map[string]any)
		if actor["type"] != "agent" || actor["agentKey"] != "writer" {
			t.Fatalf("unexpected actor on %s: %#v", event.Type, actor)
		}
	}
}

func TestTeamBootstrapUsesPublicOwnerWithoutExecutionAgentKey(t *testing.T) {
	assembler := NewAssembler(StreamRequest{RunID: "run-team", ChatID: "chat-team", TeamID: "team-a", OwnerType: "team", Message: "hello", Role: "user"})
	events := assembler.Bootstrap()
	if len(events) < 2 {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if event.Type != "request.query" && event.Type != "run.start" {
			continue
		}
		if event.Payload["teamId"] != "team-a" {
			t.Fatalf("%s teamId=%#v", event.Type, event.Payload)
		}
		if key, _ := event.Payload["agentKey"].(string); key != "" {
			t.Fatalf("%s leaked execution agent key %q", event.Type, key)
		}
	}
}

func TestTeamTaskTerminalCarriesActorAndPresentation(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{RunID: "run-team", ChatID: "chat-team", TeamID: "team-a", OwnerType: "team"})
	events := dispatcher.Dispatch(TaskComplete{TaskID: "task-a", TeamID: "team-a", AgentKey: "writer", Presentation: "task"})
	if len(events) != 1 || events[0].Type != "task.complete" {
		t.Fatalf("events=%#v", events)
	}
	payload := events[0].Payload
	actor, _ := payload["actor"].(map[string]any)
	if payload["teamId"] != "team-a" || payload["presentation"] != "task" || actor["agentKey"] != "writer" {
		t.Fatalf("terminal metadata=%#v", payload)
	}
}
