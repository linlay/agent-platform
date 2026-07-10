package chat

import (
	"strings"
	"testing"
)

func TestSystemInitReadersSupportLegacyAndAgentScopedIdentity(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-compat"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	legacySystem := map[string]any{
		"cacheKey":      "react:main",
		"fingerprint":   "sha256:agent-a",
		"systemMessage": map[string]any{"role": "system", "content": "legacy agent a"},
		"tools":         []any{},
	}
	if err := store.appendJSONLine(store.chatJSONLPath(chatID), map[string]any{
		"_type":     "query",
		"chatId":    chatID,
		"runId":     "run-a",
		"updatedAt": int64(1),
		"query":     map[string]any{"role": "user", "message": "hello", "agentKey": "agent-a"},
		"systems":   []any{legacySystem},
	}); err != nil {
		t.Fatalf("append legacy query: %v", err)
	}
	agentBSystem := QueryLineSystemInit{
		AgentKey:      "agent-b",
		CacheKey:      "react:main",
		Fingerprint:   "sha256:agent-b",
		SystemMessage: map[string]any{"role": "system", "content": "agent b"},
		Tools:         []any{},
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-b",
		UpdatedAt: 2,
		Query:     map[string]any{"role": "user", "message": "hello again", "agentKey": "agent-b"},
		System:    &agentBSystem,
	}); err != nil {
		t.Fatalf("append singular query: %v", err)
	}

	index, err := store.LoadAllSystemInits(chatID)
	if err != nil {
		t.Fatalf("load system inits: %v", err)
	}
	if got := index.Lookup("agent-a", "react:main"); got == nil || got.Fingerprint != "sha256:agent-a" {
		t.Fatalf("legacy agent system not indexed correctly: %#v", got)
	}
	if got := index.Lookup("agent-b", "react:main"); got == nil || got.Fingerprint != "sha256:agent-b" {
		t.Fatalf("singular agent system not indexed correctly: %#v", got)
	}
	lines, err := readJSONLines(store.chatJSONLPath(chatID))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if _, ok := lines[1]["system"].(map[string]any); !ok {
		t.Fatalf("new query must serialize singular system: %#v", lines[1])
	}
	if _, ok := lines[1]["systems"]; ok {
		t.Fatalf("new query must not serialize systems: %#v", lines[1])
	}
}

func TestSingularSystemOverridesMatchingLegacyEntryOnMixedLine(t *testing.T) {
	line := map[string]any{
		"_type": "query",
		"query": map[string]any{"agentKey": "agent"},
		"systems": []any{map[string]any{
			"cacheKey":      "react:main",
			"fingerprint":   "sha256:same",
			"systemMessage": map[string]any{"role": "system", "content": "legacy"},
			"tools":         []any{},
		}},
		"system": map[string]any{
			"agentKey":      "agent",
			"cacheKey":      "react:main",
			"fingerprint":   "sha256:same",
			"systemMessage": map[string]any{"role": "system", "content": "singular"},
			"tools":         []any{},
		},
	}
	systems, err := queryLineSystemInitsFromJSONL(line)
	if err != nil || len(systems) != 1 || systems[0].SystemMessage["content"] != "singular" {
		t.Fatalf("singular system did not override legacy entry: %#v err=%v", systems, err)
	}
}

func TestBuildLLMChatRejectsAmbiguousLegacySystemRef(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	const chatID = "chat-system-ambiguous"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	newSystem := func(agentKey, content string) QueryLineSystemInit {
		return QueryLineSystemInit{
			AgentKey:      agentKey,
			CacheKey:      "react:main",
			Fingerprint:   "sha256:shared",
			SystemMessage: map[string]any{"role": "system", "content": content},
			Tools:         []any{},
			Model:         map[string]any{"key": "mock-model"},
		}
	}
	for index, agentKey := range []string{"agent-a", "agent-b"} {
		system := newSystem(agentKey, agentKey+" system")
		query := map[string]any{"role": "system", "kind": "system-init", "agentKey": agentKey}
		messages := []map[string]any(nil)
		if index == 0 {
			query = map[string]any{"role": "user", "message": "hello", "agentKey": agentKey}
			messages = []map[string]any{{"role": "user", "content": "hello"}}
		}
		if err := store.AppendQueryLine(chatID, QueryLine{
			Type:      "query",
			ChatID:    chatID,
			RunID:     "run-1",
			UpdatedAt: int64(index + 1),
			Query:     query,
			Messages:  messages,
			System:    &system,
		}); err != nil {
			t.Fatalf("append query: %v", err)
		}
	}
	if err := store.AppendStepLine(chatID, StepLine{
		Type:      StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 3,
		Seq:       1,
		SystemRef: map[string]any{"cacheKey": "react:main", "fingerprint": "sha256:shared"},
		Messages:  []StoredMessage{{Role: "assistant", Content: textContent("done")}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}

	_, err = store.BuildLLMChatFromJSONL(chatID, LLMChatBuildOptions{RunID: "run-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous legacy systemRef error, got %v", err)
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
	planSystem := QueryLineSystemInit{
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
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 1,
		Query:     map[string]any{"role": "user", "message": "plan work", "agentKey": "planner"},
		Messages:  []map[string]any{{"role": "user", "content": "plan work"}},
		System:    &planSystem,
	}); err != nil {
		t.Fatalf("append initial query: %v", err)
	}
	if err := store.AppendQueryLine(chatID, QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-1",
		UpdatedAt: 2,
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
