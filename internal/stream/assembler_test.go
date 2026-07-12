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
	assertStampedTypes(t, bootstrap, "chat.start", "request.query", "run.start")
	requestQuery := bootstrap[1].ToData()
	if requestQuery["agentKey"] != "agent_1" {
		t.Fatalf("expected request.query agentKey agent_1, got %#v", requestQuery)
	}

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

func TestAssemblerBootstrapUsesRegisteredRunStartTimestamp(t *testing.T) {
	const startedAt = int64(1_700_000_000_123)
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_started",
		RunID:     "run_started",
		ChatID:    "chat_started",
		AgentKey:  "agent_1",
		Message:   "hello",
		Role:      "user",
	})
	assembler.SetRunStartedAtMillis(startedAt)

	found := false
	for _, event := range assembler.Bootstrap() {
		if event.Type != "run.start" {
			continue
		}
		found = true
		if event.Timestamp != startedAt {
			t.Fatalf("run.start timestamp = %d, want registered %d", event.Timestamp, startedAt)
		}
	}
	if !found {
		t.Fatal("expected run.start bootstrap event")
	}
}

func TestAssemblerBootstrapCanUseSyntheticQuery(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID: "submit_1",
		RunID:     "run_exec",
		ChatID:    "chat_1",
		AgentKey:  "coder",
		Role:      "system",
		BootstrapSynthetic: &SyntheticQuery{
			ChatID:  "chat_1",
			Role:    "user",
			Message: "Execute planning",
			Messages: []map[string]any{{
				"role":    "user",
				"content": "Execute the confirmed CODER planning.",
			}},
			System: map[string]any{
				"agentKey":    "coder",
				"cacheKey":    "coder:execute",
				"fingerprint": "fp_1",
			},
		},
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
	query := bootstrap[0].ToData()
	if query["requestId"] != "submit_1" || query["runId"] != "run_exec" ||
		query["message"] != "Execute planning" {
		t.Fatalf("unexpected synthetic bootstrap query %#v", query)
	}
	for _, field := range []string{"synthetic", "stage", "source"} {
		if _, ok := query[field]; ok {
			t.Fatalf("did not expect %s in synthetic bootstrap query %#v", field, query)
		}
	}
	if _, ok := query["messages"]; !ok {
		t.Fatalf("expected synthetic bootstrap query to carry messages for persistence: %#v", query)
	}
	if _, ok := query["system"]; !ok {
		t.Fatalf("expected synthetic bootstrap query to carry system for persistence: %#v", query)
	}
}

func TestAssemblerContinuationCanBootstrapSystemRegistrationQuery(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RunID:       "run_continue",
		ChatID:      "chat_1",
		ContinueRun: true,
		BootstrapSynthetic: &SyntheticQuery{
			ChatID: "chat_1",
			Role:   "system",
			Kind:   "system-init",
			Hidden: true,
			System: map[string]any{
				"agentKey":    "agent",
				"cacheKey":    "react:main",
				"fingerprint": "sha256:changed",
			},
		},
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
	query := bootstrap[0].ToData()
	if query["kind"] != "system-init" || query["hidden"] != true {
		t.Fatalf("unexpected continuation system registration %#v", query)
	}
}

func TestAssemblerBootstrapIncludesOptionalQueryContext(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_3",
		RunID:     "run_3",
		ChatID:    "chat_3",
		AgentKey:  "agent_3",
		Message:   "hello",
		Role:      "user",
		References: []map[string]any{{
			"id":   "ref_1",
			"type": "file",
			"name": "notes.txt",
		}},
		Params: map[string]any{
			"channel": "desktop",
			"nested":  map[string]any{"enabled": true},
		},
		Scene: &SceneRef{URL: "https://example.com/app", Title: "demo"},
		Model: map[string]any{
			"key":             "qwen3-max",
			"reasoningEffort": "HIGH",
		},
		AccessLevel: "auto_approve",
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
	requestQuery := bootstrap[0].ToData()
	if _, ok := requestQuery["references"]; !ok {
		t.Fatalf("expected request.query references, got %#v", requestQuery)
	}
	params, _ := requestQuery["params"].(map[string]any)
	if params["channel"] != "desktop" {
		t.Fatalf("expected request.query params, got %#v", requestQuery)
	}
	model, _ := requestQuery["model"].(map[string]any)
	if model["key"] != "qwen3-max" || model["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected request.query model, got %#v", requestQuery)
	}
	if requestQuery["accessLevel"] != "auto_approve" {
		t.Fatalf("expected request.query accessLevel, got %#v", requestQuery)
	}
	scene, _ := requestQuery["scene"].(map[string]any)
	if scene["url"] != "https://example.com/app" || scene["title"] != "demo" {
		t.Fatalf("expected request.query scene, got %#v", requestQuery)
	}
}

func TestAssemblerBootstrapOmitsTypedNilModel(t *testing.T) {
	type queryModelOptions struct {
		Key string `json:"key,omitempty"`
	}
	var model *queryModelOptions
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_nil_model",
		RunID:     "run_nil_model",
		ChatID:    "chat_nil_model",
		AgentKey:  "agent_nil_model",
		Message:   "hello",
		Role:      "user",
		Model:     model,
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
	requestQuery := bootstrap[0].ToData()
	if _, ok := requestQuery["model"]; ok {
		t.Fatalf("expected typed nil model to be omitted, got %#v", requestQuery)
	}
}

func TestAssemblerBootstrapIncludesPlanningModeWhenEnabled(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID:    "req_plan",
		RunID:        "run_plan",
		ChatID:       "chat_plan",
		AgentKey:     "coder",
		Message:      "plan first",
		Role:         "user",
		PlanningMode: true,
	})

	bootstrap := assembler.Bootstrap()
	requestQuery := bootstrap[0].ToData()
	if requestQuery["planningMode"] != true {
		t.Fatalf("expected request.query planningMode=true, got %#v", requestQuery)
	}
}

func TestAssemblerBootstrapOmitsEmptyQueryContext(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID:  "req_4",
		RunID:      "run_4",
		ChatID:     "chat_4",
		AgentKey:   "agent_4",
		Message:    "hello",
		Role:       "user",
		References: []map[string]any{},
		Params:     map[string]any{},
	})

	bootstrap := assembler.Bootstrap()
	assertStampedTypes(t, bootstrap, "request.query", "run.start")
	requestQuery := bootstrap[0].ToData()
	if _, ok := requestQuery["references"]; ok {
		t.Fatalf("expected empty references to be omitted, got %#v", requestQuery)
	}
	if _, ok := requestQuery["params"]; ok {
		t.Fatalf("expected empty params to be omitted, got %#v", requestQuery)
	}
	if _, ok := requestQuery["planningMode"]; ok {
		t.Fatalf("expected planningMode to be omitted when disabled, got %#v", requestQuery)
	}
}

func TestAssemblerBootstrapOmitsMemoryContextWhenPresent(t *testing.T) {
	assembler := NewAssembler(StreamRequest{
		RequestID: "req_5",
		RunID:     "run_5",
		ChatID:    "chat_5",
		AgentKey:  "agent_5",
		Message:   "hello",
		Role:      "user",
		MemoryUsageSummary: map[string]any{
			"hasStaticMemory":  true,
			"stableCount":      2,
			"observationCount": 1,
		},
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
