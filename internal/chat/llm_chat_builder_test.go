package chat

import (
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestBuildLLMChatFromJSONLUsesSystemFingerprint(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-jsonl"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	oldSystem := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:old",
		SystemMessage: map[string]any{"role": "system", "content": "old system"},
		Tools: []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "old_tool",
				"description": "old",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
		Model: map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 0},
	}
	newSystem := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:new",
		SystemMessage: map[string]any{"role": "system", "content": "new system"},
		Tools: []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "new_tool",
				"description": "new",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
		Model: map[string]any{
			"key":         "new-model",
			"id":          "new-model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 1},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Systems:   []QueryLineSystemInit{oldSystem, newSystem},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:old"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if got := chat.Messages[0]["content"]; got != "old system" {
		t.Fatalf("expected old system message, got %#v", chat.Messages)
	}
	if got := chat.Messages[1]["content"]; got != "hello" {
		t.Fatalf("expected query message, got %#v", chat.Messages)
	}
	if len(chat.Tools) != 1 {
		t.Fatalf("expected one tool, got %#v", chat.Tools)
	}
	fn, _ := chat.Tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "old_tool" {
		t.Fatalf("expected old tool from fingerprinted systemRef, got %#v", chat.Tools)
	}
	if chat.ToolChoice != "auto" {
		t.Fatalf("tool choice = %q", chat.ToolChoice)
	}
	if chat.Model["id"] != "model-id" {
		t.Fatalf("model snapshot not restored: %#v", chat.Model)
	}
	if chat.RequestOptions["temperature"] != float64(0) {
		t.Fatalf("request options not restored: %#v", chat.RequestOptions)
	}
}

func TestBuildLLMChatFromJSONLAppendsInputMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-input"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	executeSystem := QueryLineSystemInit{
		CacheKey:      "plan-execute:execute",
		Fingerprint:   "sha256:execute",
		SystemMessage: map[string]any{"role": "system", "content": "execute system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":             "execute-model",
			"id":              "execute-model-id",
			"providerKey":     "provider",
			"protocol":        "OPENAI",
			"reasoningEffort": "HIGH",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "original"},
		Messages:  []map[string]any{{"role": "user", "content": "original"}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "first answer"}},
		}},
	}); err != nil {
		t.Fatalf("append first step: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:          StepLineTypePlanExecute,
		ChatID:        chatID,
		RunID:         "run-1",
		UpdatedAt:     3,
		Stage:         "execute",
		Seq:           2,
		InputMessages: []map[string]any{{"role": "user", "content": "execute task"}},
		SystemRef:     map[string]any{"cacheKey": "plan-execute:execute", "fingerprint": "sha256:execute"},
		Systems:       []QueryLineSystemInit{executeSystem},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "execute answer"}},
		}},
	}); err != nil {
		t.Fatalf("append execute step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Stage: "execute", Seq: 2})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if got := chat.Messages[0]["content"]; got != "execute system" {
		t.Fatalf("expected execute system, got %#v", chat.Messages)
	}
	if got := chat.Messages[len(chat.Messages)-1]["content"]; got != "execute task" {
		t.Fatalf("expected input message appended, got %#v", chat.Messages)
	}
	for _, msg := range chat.Messages {
		if msg["content"] == "execute answer" {
			t.Fatalf("target assistant response must not be part of request messages: %#v", chat.Messages)
		}
	}
}

func TestBuildLLMChatFromJSONLUsesSyntheticQueryMessagesOnce(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-synthetic-query"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	executePrompt := "Execute the confirmed CODER plan.\n\nOriginal request:\nhello\n\nConfirmed plan:\n# Plan"
	executeSystem := QueryLineSystemInit{
		CacheKey:      "coder:execute",
		Fingerprint:   "sha256:execute",
		SystemMessage: map[string]any{"role": "system", "content": "execute system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":         "execute-model",
			"id":          "execute-model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReactTool,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "tool",
			Name:    "finalize_planning",
			ToolID:  "tool_plan",
			Content: []ContentPart{{Type: "text", Text: `{"decision":"approve"}`}},
		}},
	}); err != nil {
		t.Fatalf("append react-tool: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 3,
		Query: map[string]any{
			"role":      "user",
			"message":   "执行计划",
			"synthetic": true,
			"stage":     "coder-execute",
			"source":    "coder-plan-approve",
		},
		Messages: []map[string]any{{"role": "user", "content": executePrompt}},
	}); err != nil {
		t.Fatalf("append synthetic query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 4,
		Seq:       2,
		SystemRef: map[string]any{"cacheKey": "coder:execute", "fingerprint": "sha256:execute"},
		Systems:   []QueryLineSystemInit{executeSystem},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "done"}},
		}},
	}); err != nil {
		t.Fatalf("append execute step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 2})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if got := chat.Messages[0]["content"]; got != "execute system" {
		t.Fatalf("expected execute system, got %#v", chat.Messages)
	}
	executePromptCount := 0
	for _, msg := range chat.Messages {
		if msg["content"] == executePrompt {
			executePromptCount++
		}
		if msg["content"] == "done" {
			t.Fatalf("target assistant response must not be part of request messages: %#v", chat.Messages)
		}
	}
	if executePromptCount != 1 {
		t.Fatalf("expected execute prompt once, got %d in %#v", executePromptCount, chat.Messages)
	}
}

func TestBuildLLMChatFromJSONLReplaysSteerWithoutInputMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-steer"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	system := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:react",
		SystemMessage: map[string]any{"role": "system", "content": "react system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":         "react-model",
			"id":          "react-model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "original"},
		Messages:  []map[string]any{{"role": "user", "content": "original"}},
		Systems:   []QueryLineSystemInit{system},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "first answer"}},
		}},
	}); err != nil {
		t.Fatalf("append first step: %v", err)
	}
	if err := store.AppendEventLine(chatID, EventLine{
		Type:      "steer",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 3,
		Event: map[string]any{
			"type":    "request.steer",
			"runId":   "run-1",
			"chatId":  chatID,
			"steerId": "steer-1",
			"message": "Please keep it short.",
			"role":    "user",
		},
	}); err != nil {
		t.Fatalf("append steer: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 4,
		Seq:       2,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:react"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "final answer"}},
		}},
	}); err != nil {
		t.Fatalf("append target step: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 2})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	if got := chat.Messages[0]["content"]; got != "react system" {
		t.Fatalf("expected system message first, got %#v", chat.Messages)
	}
	steerIndex := -1
	for i, msg := range chat.Messages {
		if msg["content"] == "Please keep it short." {
			steerIndex = i
		}
		if msg["content"] == "final answer" {
			t.Fatalf("target assistant response must not be part of request messages: %#v", chat.Messages)
		}
	}
	if steerIndex < 0 {
		t.Fatalf("expected steer message in reconstructed request, got %#v", chat.Messages)
	}
	if got := chat.Messages[len(chat.Messages)-1]["content"]; got != "Please keep it short." {
		t.Fatalf("expected steer message at end of reconstructed request, got %#v", chat.Messages)
	}
}

func TestBuildLLMChatFromJSONLUsesReactToolAuditMessageOnce(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-audit-once"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	system := QueryLineSystemInit{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:system",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true},
	}
	auditNotice := `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="pwd" decision=approve reason=""
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.`
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Systems:   []QueryLineSystemInit{system},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReactTool,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:       "tool",
				Name:       "bash",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "ok"}},
			},
			{
				Role:    "user",
				Content: textContent(auditNotice),
			},
		},
	}); err != nil {
		t.Fatalf("append react-tool: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 3,
		Seq:       2,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "done"}},
		}},
	}); err != nil {
		t.Fatalf("append target react: %v", err)
	}

	chat, err := store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 2})
	if err != nil {
		t.Fatalf("build llm chat: %v", err)
	}
	auditCount := 0
	for _, message := range chat.Messages {
		content, _ := message["content"].(string)
		if strings.Contains(content, "[System audit — HITL approval batch]") {
			auditCount++
		}
	}
	if auditCount != 1 {
		t.Fatalf("expected audit message once, got %d in %#v", auditCount, chat.Messages)
	}
}

func TestStepWriterKeepsLLMRequestProfileOutOfStepLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-request"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, chatID, "run-1", "REACT")
	writer.SetPendingSystemInits([]QueryLineSystemInit{{
		CacheKey:      "react:main",
		Fingerprint:   "sha256:system",
		SystemMessage: map[string]any{"role": "system", "content": "system"},
		Tools:         []any{},
		Model: map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		ToolChoice:     "auto",
		RequestOptions: map[string]any{"stream": true, "temperature": 0},
	}})
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello"}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{
		"role":    "user",
		"message": "hello",
		"runId":   "run-1",
		"chatId":  chatID,
	}).Data())
	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{
		"runId":  "run-1",
		"chatId": chatID,
		"model": map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		"systemRef":      map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"},
		"toolChoice":     "auto",
		"requestOptions": map[string]any{"stream": true, "temperature": 0},
		"inputMessages":  []any{map[string]any{"role": "user", "content": "internal"}},
	}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "answer",
	}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected query and step lines, got %#v", lines)
	}
	query := lines[0]
	systems, _ := query["systems"].([]any)
	if len(systems) != 1 {
		t.Fatalf("expected query system profile, got %#v", query)
	}
	profile, _ := systems[0].(map[string]any)
	if profile["toolChoice"] != "auto" {
		t.Fatalf("expected query system toolChoice, got %#v", profile)
	}
	model, _ := profile["model"].(map[string]any)
	if model["id"] != "model-id" {
		t.Fatalf("expected query system model snapshot, got %#v", profile)
	}
	options, _ := profile["requestOptions"].(map[string]any)
	if options["temperature"] != float64(0) {
		t.Fatalf("expected query system request options, got %#v", profile)
	}
	step := lines[1]
	if _, ok := step["toolChoice"]; ok {
		t.Fatalf("did not expect step toolChoice, got %#v", step)
	}
	if _, ok := step["model"]; ok {
		t.Fatalf("did not expect step model snapshot, got %#v", step)
	}
	if _, ok := step["requestOptions"]; ok {
		t.Fatalf("did not expect step request options, got %#v", step)
	}
	if _, ok := step["system"]; ok {
		t.Fatalf("did not expect step system, got %#v", step)
	}
	if _, ok := step["systems"]; ok {
		t.Fatalf("did not expect duplicate step systems, got %#v", step)
	}
	inputMessages, _ := step["inputMessages"].([]any)
	if len(inputMessages) != 1 {
		t.Fatalf("expected input messages, got %#v", step)
	}
}

func TestStepWriterSkipsSystemAuditInputMessages(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-skip-audit-input"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	auditNotice := `[System audit — HITL approval batch]
The user reviewed the following tool call(s) and submitted decisions:
1. tool=bash command="pwd" decision=approve reason=""
The tool results above already reflect these decisions; do not re-prompt for approval and do not retry rejected calls.`
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReactTool,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Seq:       1,
		Messages: []StoredMessage{
			{
				Role:       "tool",
				Name:       "bash",
				ToolCallID: "tool-1",
				Content:    []ContentPart{{Type: "text", Text: "ok"}},
			},
			{
				Role:    "user",
				Content: textContent(auditNotice),
			},
		},
	}); err != nil {
		t.Fatalf("append react-tool: %v", err)
	}

	writer := NewStepWriter(store, chatID, "run-1", "REACT")
	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{
		"runId":         "run-1",
		"chatId":        chatID,
		"inputMessages": []any{map[string]any{"role": "user", "content": auditNotice}},
	}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "done",
	}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected react-tool and react lines, got %#v", lines)
	}
	target := lines[1]
	if stringValue(target["_type"]) != StepLineTypeReact {
		t.Fatalf("expected target react line, got %#v", target)
	}
	if _, ok := target["inputMessages"]; ok {
		t.Fatalf("did not expect duplicate audit inputMessages, got %#v", target)
	}
}

func TestStepWriterDoesNotPersistInlineProfileAsStepSystems(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-step-systems"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, chatID, "run-1", "REACT")
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello"}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{
		"role":    "user",
		"message": "hello",
		"runId":   "run-1",
		"chatId":  chatID,
	}).Data())
	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{
		"runId":  "run-1",
		"chatId": chatID,
		"model": map[string]any{
			"key":         "model-key",
			"id":          "model-id",
			"providerKey": "provider",
			"protocol":    "OPENAI",
		},
		"system": map[string]any{
			"cacheKey":      "react:main",
			"fingerprint":   "sha256:inline",
			"systemMessage": map[string]any{"role": "system", "content": "inline system"},
			"tools":         []any{},
			"requestOptions": map[string]any{
				"stream": true,
			},
		},
		"inputMessages": []any{map[string]any{"role": "user", "content": "final input"}},
	}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "final answer",
	}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	step := lines[1]
	if _, ok := step["systems"]; ok {
		t.Fatalf("did not expect step-level systems, got %#v", step)
	}
	if _, ok := step["systemRef"]; ok {
		t.Fatalf("did not expect inline system to synthesize systemRef, got %#v", step)
	}
	if _, ok := step["system"]; ok {
		t.Fatalf("did not expect legacy inline system, got %#v", step)
	}
}

func TestBuildLLMChatFromJSONLRejectsMissingSystemRef(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-missing-system-ref"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Systems: []QueryLineSystemInit{{
			CacheKey:      "react:main",
			Fingerprint:   "sha256:system",
			SystemMessage: map[string]any{"role": "system", "content": "system"},
			Tools:         []any{},
			Model:         map[string]any{"key": "model-key"},
		}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "missing systemRef") {
		t.Fatalf("expected missing systemRef error, got %v", err)
	}
}

func TestBuildLLMChatFromJSONLRejectsMissingSystemSnapshot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-missing-system-snapshot"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:missing"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "system snapshot not found") {
		t.Fatalf("expected missing system snapshot error, got %v", err)
	}
}

func TestBuildLLMChatFromJSONLRejectsSystemSnapshotWithoutModelKey(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-missing-model-key"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Systems: []QueryLineSystemInit{{
			CacheKey:      "react:main",
			Fingerprint:   "sha256:system",
			SystemMessage: map[string]any{"role": "system", "content": "system"},
			Tools:         []any{},
			Model:         map[string]any{"id": "model-id"},
		}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
		Seq:       1,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"},
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "missing model key") {
		t.Fatalf("expected missing model key error, got %v", err)
	}
}

func TestBuildLLMChatFromJSONLRejectsLegacyRequestFields(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-llm-legacy-fields"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "hello"},
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:            StepLineTypeReact,
		ChatID:          chatID,
		RunID:           "run-1",
		UpdatedAt:       2,
		Seq:             1,
		ModelKey:        "legacy-model",
		ReasoningEffort: "LOW",
		Messages: []StoredMessage{{
			Role:    "assistant",
			Content: []ContentPart{{Type: "text", Text: "hi"}},
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "missing systemRef") {
		t.Fatalf("expected legacy request fields to be ignored, got %v", err)
	}
}

func TestStepWriterDropsIncompleteInlineSystemProfile(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-incomplete-inline-system"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	writer := NewStepWriter(store, chatID, "run-1", "REACT")
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello"}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{
		"role":    "user",
		"message": "hello",
		"runId":   "run-1",
		"chatId":  chatID,
	}).Data())
	writer.OnEvent(stream.NewEvent("llm.request", map[string]any{
		"runId":  "run-1",
		"chatId": chatID,
		"model": map[string]any{
			"key": "model-key",
		},
		"system": map[string]any{
			"systemMessage": map[string]any{"role": "system", "content": "legacy system"},
			"tools":         []any{},
		},
		"inputMessages": []any{map[string]any{"role": "user", "content": "internal"}},
	}).Data())
	writer.OnEvent(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "answer",
	}).Data())
	writer.Flush()

	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	step := lines[1]
	for _, key := range []string{"system", "systemRef", "systems", "model", "toolChoice", "requestOptions"} {
		if _, ok := step[key]; ok {
			t.Fatalf("did not expect incomplete inline profile to write %s: %#v", key, step)
		}
	}
	if inputMessages, _ := step["inputMessages"].([]any); len(inputMessages) != 1 {
		t.Fatalf("expected input messages to remain, got %#v", step)
	}
}
