package stream

import "testing"

func TestAssemblerBootstrapAndComplete(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_1",
		RunID:     "run_1",
		ChatID:    "chat_1",
		ChatName:  "Test Chat",
		AgentKey:  "agent_1",
		Message:   "hello",
		Role:      "user",
		Created:   true,
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "chat.start", "run.start")

	events := assembler.Consume(ContentDelta{ContentID: "run_1_c_1", Delta: "hello"})
	assertStampedTypes(t, events, "content.start", "content.delta")

	complete := assembler.Consume(InputRunComplete{FinishReason: "stop"})
	if len(complete) != 0 {
		t.Fatalf("expected no terminal events before Complete, got %#v", complete)
	}

	finalEvents := assembler.Complete()
	assertStampedTypes(t, finalEvents, "content.end", "content.snapshot", "run.complete")

	runComplete := finalEvents[len(finalEvents)-1].ToData()
	if _, ok := runComplete["chatId"]; ok {
		t.Fatalf("run.complete should not carry chatId: %#v", runComplete)
	}
	if _, ok := runComplete["agentKey"]; ok {
		t.Fatalf("run.complete should not carry agentKey: %#v", runComplete)
	}
	if runComplete["finishReason"] != "stop" {
		t.Fatalf("unexpected finishReason: %#v", runComplete)
	}
}

func TestAssemblerBootstrapSkipsChatStartForExistingChat(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_2",
		RunID:     "run_2",
		ChatID:    "chat_1",
		ChatName:  "Existing Chat",
		AgentKey:  "agent_1",
		Message:   "again",
		Role:      "user",
		Created:   false,
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
}

func TestAssemblerFailNormalizesRunError(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := assembler.Fail(assertErr("broken"))
	assertStampedTypes(t, events, "run.error")
	payload := events[0].ToData()
	errPayload, _ := payload["error"].(map[string]any)
	if errPayload["code"] != "stream_failed" {
		t.Fatalf("unexpected run.error payload: %#v", errPayload)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func assertStampedTypes(t *testing.T, events []StreamEvent, want ...string) {
	t.Helper()
	assertEventTypes(t, events, want...)
	var prev int64
	for _, event := range events {
		if event.Seq <= prev {
			t.Fatalf("expected ascending seq values, got %#v", events)
		}
		prev = event.Seq
		if event.Timestamp == 0 {
			t.Fatalf("expected timestamp on event %#v", event)
		}
	}
}
