package stream

import "testing"

func TestTeamMemberContentCarriesActorAndPresentation(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{RunID: "run-team", ChatID: "chat-team"})
	events := dispatcher.Dispatch(ContentDelta{
		ContentID: "content-a", Delta: "hello", TaskID: "task-a",
		ActorType: "agent", AgentKey: "writer", Presentation: "reply",
	})
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if event.Payload["agentKey"] != "writer" || event.Payload["presentation"] != "reply" {
			t.Fatalf("missing Team metadata on %s: %#v", event.Type, event.Payload)
		}
		actor, _ := event.Payload["actor"].(map[string]any)
		if actor["type"] != "agent" || actor["agentKey"] != "writer" {
			t.Fatalf("unexpected actor on %s: %#v", event.Type, actor)
		}
	}
}

func TestTeamBootstrapUsesPublicOwnerWithoutExecutionAgentKey(t *testing.T) {
	assembler := NewAssembler(StreamRequest{RunID: "run-team", ChatID: "chat-team", AgentKey: "research", Message: "hello", Role: "user"})
	events := assembler.Bootstrap()
	if len(events) < 2 {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if event.Type != "request.query" && event.Type != "run.start" {
			continue
		}
		if event.Payload["agentKey"] != "research" {
			t.Fatalf("%s owner=%#v", event.Type, event.Payload)
		}
		if key, _ := event.Payload["agentKey"].(string); key != "research" {
			t.Fatalf("%s leaked execution agent key %q", event.Type, key)
		}
	}
}

func TestTeamTaskTerminalCarriesActorAndPresentation(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{RunID: "run-team", ChatID: "chat-team"})
	events := dispatcher.Dispatch(TaskComplete{TaskID: "task-a", AgentKey: "writer", Presentation: "task"})
	if len(events) != 1 || events[0].Type != "task.complete" {
		t.Fatalf("events=%#v", events)
	}
	payload := events[0].Payload
	actor, _ := payload["actor"].(map[string]any)
	if payload["presentation"] != "task" || actor["agentKey"] != "writer" {
		t.Fatalf("terminal metadata=%#v", payload)
	}
}

func TestParallelMemberAwaitingUsesExecutorIdentity(t *testing.T) {
	d := NewDispatcher(StreamRequest{AgentKey: "research", RunID: "run"})
	d.Dispatch(TaskStart{TaskID: "a", SubAgentKey: "writer"})
	d.Dispatch(TaskStart{TaskID: "b", SubAgentKey: "reviewer"})
	for task, key := range map[string]string{"a": "writer", "b": "reviewer"} {
		events := d.Dispatch(AwaitAsk{AwaitingID: task, TaskID: task, RunID: "run", Mode: "approval"})
		if len(events) != 1 || events[0].Payload["agentKey"] != key {
			t.Fatalf("events=%#v", events)
		}
		actor, _ := events[0].Payload["actor"].(map[string]any)
		if actor["agentKey"] != key {
			t.Fatalf("actor=%#v", actor)
		}
	}
}
