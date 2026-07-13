package chat

import (
	"strings"
	"testing"
)

func TestSystemInitReadersUseSingularAgentScopedIdentity(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-singular"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	agentSystem := QueryLineSystem{
		AgentKey:      "agent-a",
		CacheKey:      "react:main",
		Fingerprint:   "sha256:agent-a",
		SystemMessage: map[string]any{"role": "system", "content": "agent a"},
		Tools:         []any{},
	}
	if err := appendQueryLineForTest(store, chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-a",
		UpdatedAt: testEpochMillis(1),
		Query:     map[string]any{"role": "user", "message": "hello", "agentKey": "agent-a"},
		System:    &agentSystem,
	}); err != nil {
		t.Fatalf("append singular query: %v", err)
	}

	index, err := store.LoadAllSystemInits(chatID)
	if err != nil {
		t.Fatalf("load system inits: %v", err)
	}
	if got := index.Lookup("agent-a", "react:main"); got == nil || got.Fingerprint != "sha256:agent-a" {
		t.Fatalf("singular agent system not indexed correctly: %#v", got)
	}
	if got := index.Lookup("", "react:main"); got != nil {
		t.Fatalf("empty agent key lookup must not fall back: %#v", got)
	}
	if got, err := store.LoadSystemInit(chatID, SystemInitKey{CacheKey: "react:main"}); err != nil || got != nil {
		t.Fatalf("empty agent key file lookup must not fall back: system=%#v err=%v", got, err)
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if _, ok := lines[0]["system"].(map[string]any); !ok {
		t.Fatalf("query must serialize singular system: %#v", lines[0])
	}
}

func TestSystemInitReadersRejectUnsupportedAndIncompleteSchema(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-invalid"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	validSystem := map[string]any{
		"agentKey": "agent", "cacheKey": "react:main", "fingerprint": "sha256:system",
	}
	cases := []struct {
		name string
		line map[string]any
		want string
	}{
		{
			name: "query systems",
			line: map[string]any{"_type": "query", "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "systems": []any{}},
			want: "unsupported system schema field=systems",
		},
		{
			name: "mixed system and systems",
			line: map[string]any{"_type": "query", "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "system": validSystem, "systems": []any{}},
			want: "unsupported system schema field=systems",
		},
		{
			name: "step systems",
			line: map[string]any{"_type": StepLineTypeReact, "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "systems": []any{}},
			want: "unsupported system schema field=systems",
		},
		{
			name: "system missing agent",
			line: map[string]any{"_type": "query", "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "system": map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"}},
			want: "invalid system missing=agentKey",
		},
		{
			name: "system null",
			line: map[string]any{"_type": "query", "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "system": nil},
			want: "invalid system field=system must be an object",
		},
		{
			name: "system ref missing agent",
			line: map[string]any{"_type": StepLineTypeReact, "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "systemRef": map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"}},
			want: "invalid systemRef missing=agentKey",
		},
		{
			name: "system ref null",
			line: map[string]any{"_type": StepLineTypeReact, "chatId": chatID, "runId": "run-1", "updatedAt": testEpochMillis(1), "systemRef": nil},
			want: "invalid systemRef field=systemRef must be an object",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePersistedSystemInitSchema([]map[string]any{tc.line}); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "chatId="+chatID) || !strings.Contains(err.Error(), "runId=run-1") {
				t.Fatalf("expected contextual %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestPublicWritersRejectIncompleteSystemIdentity(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-write-invalid"
	cases := []struct {
		name  string
		write func() error
		want  string
	}{
		{
			name: "system missing agent",
			write: func() error {
				return store.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: "run-1", UpdatedAt: testEpochMillis(1), System: &QueryLineSystem{CacheKey: "react:main", Fingerprint: "sha256:system"}})
			},
			want: "invalid system missing=agentKey",
		},
		{
			name: "system ref missing agent",
			write: func() error {
				return store.AppendStepLine(chatID, StepLine{Type: StepLineTypeReact, ChatID: chatID, RunID: "run-1", UpdatedAt: testEpochMillis(1), SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:system"}})
			},
			want: "invalid systemRef missing=agentKey",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "chatId="+chatID) || !strings.Contains(err.Error(), "runId=run-1") {
				t.Fatalf("expected contextual %q write error, got %v", tc.want, err)
			}
		})
	}
}

func TestBuildLLMChatRejectsNonExactSystemRefAgent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-nonexact"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	system := QueryLineSystem{AgentKey: "agent-a", CacheKey: "react:main", Fingerprint: "sha256:shared", SystemMessage: map[string]any{"role": "system", "content": "private"}, Tools: []any{}, Model: map[string]any{"key": "mock-model"}}
	if err := appendQueryLineForTest(store, chatID, QueryLine{Type: "query", ChatID: chatID, RunID: "run-1", UpdatedAt: testEpochMillis(1), Query: map[string]any{"role": "user", "message": "hello"}, Messages: []map[string]any{{"role": "user", "content": "hello"}}, System: &system}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := appendStepLineForTest(store, chatID, StepLine{Type: StepLineTypeReact, ChatID: chatID, RunID: "run-1", UpdatedAt: testEpochMillis(2), Seq: 1, SystemRef: map[string]any{"agentKey": "agent-b", "cacheKey": "react:main", "fingerprint": "sha256:shared"}, Messages: []StoredMessage{{Role: "assistant", Content: textContent("done")}}}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "systemRef snapshot not found") || !strings.Contains(err.Error(), "chatId="+chatID) || !strings.Contains(err.Error(), "runId=run-1") {
		t.Fatalf("expected exact-agent snapshot miss, got %v", err)
	}
}

func TestSystemInitQueryIsStorageOnly(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-hidden"
	if _, _, err := store.EnsureChat(chatID, "planner", "", "plan work"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	planSystem := QueryLineSystem{
		AgentKey:      "planner",
		CacheKey:      "plan-execute:plan",
		Fingerprint:   "sha256:plan",
		SystemMessage: map[string]any{"role": "system", "content": "plan"},
		Tools:         []any{},
		Model:         map[string]any{"key": "mock-model"},
	}
	executeSystem := planSystem
	executeSystem.CacheKey = "plan-execute:execute"
	executeSystem.Fingerprint = "sha256:execute"
	executeSystem.SystemMessage = map[string]any{"role": "system", "content": "execute"}
	if err := appendQueryLineForTest(store, chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: testEpochMillis(1),
		Query:     map[string]any{"role": "user", "message": "plan work", "agentKey": "planner"},
		Messages:  []map[string]any{{"role": "user", "content": "plan work"}},
		System:    &planSystem,
	}); err != nil {
		t.Fatalf("append initial query: %v", err)
	}
	if err := appendQueryLineForTest(store, chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: testEpochMillis(2),
		Query: map[string]any{
			"role":    "system",
			"message": "private system registration",
			"kind":    "system-init",
			"stage":   "execute",
			"hidden":  true,
		},
		System: &executeSystem,
	}); err != nil {
		t.Fatalf("append system registration query: %v", err)
	}

	messages, err := store.LoadRawMessages(chatID, 5)
	if err != nil || len(messages) != 1 || messages[0]["content"] != "plan work" {
		t.Fatalf("storage query leaked into raw messages: %#v err=%v", messages, err)
	}
	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	queryCount := 0
	for _, event := range detail.Events {
		if event.Type == "request.query" {
			queryCount++
		}
	}
	if queryCount != 1 {
		t.Fatalf("expected only visible user query in replay, got %#v", detail.Events)
	}
	trace, err := store.LoadRunTrace(chatID, "run-1")
	if err != nil || trace.Query == nil || trace.Query.Query["message"] != "plan work" {
		t.Fatalf("system registration replaced run query: %#v err=%v", trace.Query, err)
	}
	hits, err := store.SearchSession(chatID, "private system registration", 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("storage query leaked into search: %#v err=%v", hits, err)
	}
	index, err := store.LoadAllSystemInits(chatID)
	if err != nil || index.Lookup("planner", "plan-execute:execute") == nil {
		t.Fatalf("storage query was not available to system cache: %#v err=%v", index, err)
	}
}
