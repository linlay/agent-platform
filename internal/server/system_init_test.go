package server

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
)

type systemInitStaticToolExecutor struct {
	defs []api.ToolDetailResponse
}

func (s systemInitStaticToolExecutor) Definitions() []api.ToolDetailResponse {
	return s.defs
}

func (s systemInitStaticToolExecutor) Invoke(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return contracts.ToolExecutionResult{}, nil
}

func TestPrepareSystemInitCacheWritesFreshSystemMessageOnPayloadChange(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	req := api.QueryRequest{ChatID: "chat-1", Message: "hello"}
	toolDefs := []api.ToolDetailResponse{{Name: "datetime", Description: "get current time"}}
	oldSession := contracts.QuerySession{
		RunID:                "run-old",
		ChatID:               "chat-1",
		AgentKey:             "agent",
		ModelKey:             "mock-model",
		ToolNames:            []string{"datetime"},
		Mode:                 "REACT",
		ContextTags:          []string{"system"},
		PromptAppend:         contracts.DefaultPromptAppendConfig(),
		AgentHasMemoryConfig: true,
		SessionMemoryContext: "Runtime Context: Current Session\n- stale session memory",
		ObservationContext:   "Runtime Context: Relevant Observations\n- stale observation",
	}
	oldProfiles := llm.BuildSystemInitProfiles(oldSession, req, toolDefs, 0, 0, 0, config.PromptsConfig{})
	if len(oldProfiles) != 1 {
		t.Fatalf("expected one system init profile, got %#v", oldProfiles)
	}
	if _, _, err := store.EnsureChat(req.ChatID, oldSession.AgentKey, "", req.Message); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 1_001
	startServerFixtureRun(t, store, req.ChatID, oldSession.RunID, startedAt)
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     oldSession.RunID,
		UpdatedAt: startedAt,
		Query:     map[string]any{"role": "user", "message": "hello", "agentKey": oldSession.AgentKey},
		System: &chat.QueryLineSystemInit{
			AgentKey:      oldSession.AgentKey,
			Fingerprint:   oldProfiles[0].Fingerprint,
			CacheKey:      oldProfiles[0].CacheKey,
			SystemMessage: oldProfiles[0].SystemMessage,
			Tools:         oldProfiles[0].Tools,
		},
	}); err != nil {
		t.Fatalf("append system init: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{defs: toolDefs},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}
	newSession := oldSession
	newSession.RunID = "run-new"
	newSession.SessionMemoryContext = "Runtime Context: Current Session\n- fresh session memory"
	newSession.ObservationContext = "Runtime Context: Relevant Observations\n- fresh observation"

	pending, err := server.prepareSystemInitCache(req, &newSession, false)
	if err != nil {
		t.Fatalf("prepare system init cache: %v", err)
	}
	if pending == nil {
		t.Fatalf("expected changed system payload to append one system cache line, got %#v", pending)
	}
	if pending.Fingerprint != oldProfiles[0].Fingerprint {
		t.Fatalf("expected same fingerprint to be retained, got %#v", pending)
	}
	snapshot, ok := newSession.SystemInitCache["react:main"]
	if !ok {
		t.Fatalf("missing react cache snapshot %#v", newSession.SystemInitCache)
	}
	content, _ := snapshot.SystemMessage["content"].(string)
	if !strings.Contains(content, "fresh session memory") || strings.Contains(content, "stale session memory") {
		t.Fatalf("expected fresh dynamic system message, got %q", content)
	}
	pendingContent, _ := pending.SystemMessage["content"].(string)
	if pendingContent != content {
		t.Fatalf("expected pending system to match session cache, pending=%q cache=%q", pendingContent, content)
	}
	if !reflect.DeepEqual(snapshot.Tools, pending.Tools) {
		t.Fatalf("expected session cache tools to match pending tools, pending=%#v cache=%#v", pending.Tools, snapshot.Tools)
	}
}

func TestPrepareSystemInitCacheReturnsPendingLineOnFingerprintChange(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	req := api.QueryRequest{ChatID: "chat-1", Message: "hello"}
	toolDefs := []api.ToolDetailResponse{{Name: "datetime", Description: "get current time"}}
	session := contracts.QuerySession{
		RunID:                "run-1",
		ChatID:               "chat-1",
		AgentKey:             "agent",
		ModelKey:             "mock-model",
		ToolNames:            []string{"datetime"},
		Mode:                 "REACT",
		ContextTags:          []string{"system"},
		PromptAppend:         contracts.DefaultPromptAppendConfig(),
		AgentHasMemoryConfig: true,
		SessionMemoryContext: "Runtime Context: Current Session\n- fresh",
	}
	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{defs: toolDefs},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}

	pending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("prepare system init cache: %v", err)
	}
	if pending == nil {
		t.Fatalf("expected one pending system cache line, got %#v", pending)
	}
	if pending.CacheKey != "react:main" || pending.Fingerprint == "" || pending.AgentKey != session.AgentKey {
		t.Fatalf("unexpected pending system cache line %#v", pending)
	}
	if _, ok := session.SystemInitCache["react:main"]; !ok {
		t.Fatalf("expected session cache to be populated, got %#v", session.SystemInitCache)
	}
	loaded, err := store.LoadSystemInit(req.ChatID, chat.SystemInitKey{AgentKey: session.AgentKey, CacheKey: "react:main"})
	if err != nil {
		t.Fatalf("load system init: %v", err)
	}
	if loaded != nil {
		t.Fatalf("prepare should not append before query, got %#v", loaded)
	}
}

func TestPrepareSystemInitCacheRegistersPlanExecuteProfiles(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	req := api.QueryRequest{ChatID: "chat-plan", Message: "plan this"}
	toolDefs := []api.ToolDetailResponse{{Name: "bash", Description: "run command"}}
	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{defs: toolDefs},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}
	session := contracts.QuerySession{
		RunID:        "run-1",
		ChatID:       "chat-plan",
		AgentKey:     "agent",
		ModelKey:     "mock-model",
		ToolNames:    []string{"bash"},
		Mode:         "PLAN_EXECUTE",
		PromptAppend: contracts.DefaultPromptAppendConfig(),
	}

	pending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("prepare system init cache: %v", err)
	}
	if pending == nil || pending.CacheKey != "plan-execute:plan" {
		t.Fatalf("expected only plan profile on the initial query, got %#v", pending)
	}
	if !session.PendingSystemInitKeys["plan-execute:execute"] || !session.PendingSystemInitKeys["plan-execute:summary"] {
		t.Fatalf("expected execute and summary profiles to remain pending, got %#v", session.PendingSystemInitKeys)
	}
	if _, ok := session.SystemInitCache["plan-execute:plan"]; !ok {
		t.Fatalf("expected plan profile cached, got %#v", session.SystemInitCache)
	}
	if _, ok := session.SystemInitCache["plan-execute:execute"]; !ok {
		t.Fatalf("expected execute profile cached, got %#v", session.SystemInitCache)
	}
	if _, ok := session.SystemInitCache["plan-execute:summary"]; !ok {
		t.Fatalf("expected summary profile cached, got %#v", session.SystemInitCache)
	}
}

func TestMainQueryDedupsSystemsOnlyWhenPayloadMatches(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	req := api.QueryRequest{ChatID: "chat-1", Message: "hello"}
	toolDefs := []api.ToolDetailResponse{{Name: "datetime", Description: "get current time"}}
	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{defs: toolDefs},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}
	session := contracts.QuerySession{
		RunID:        "run-1",
		ChatID:       "chat-1",
		AgentKey:     "agent",
		ModelKey:     "mock-model",
		ToolNames:    []string{"datetime"},
		Mode:         "REACT",
		PromptAppend: contracts.DefaultPromptAppendConfig(),
	}

	firstPending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("first prepare system init cache: %v", err)
	}
	if firstPending == nil {
		t.Fatalf("expected first main query to carry one system init, got %#v", firstPending)
	}
	if firstPending.CacheKey != "react:main" {
		t.Fatalf("unexpected first system init cache keys %#v", firstPending)
	}
	if _, _, err := store.EnsureChat(req.ChatID, session.AgentKey, "", req.Message); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 2_001
	startServerFixtureRun(t, store, req.ChatID, session.RunID, startedAt)
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     session.RunID,
		UpdatedAt: startedAt,
		Query:     map[string]any{"role": "user", "message": req.Message, "agentKey": session.AgentKey},
		System:    firstPending,
	}); err != nil {
		t.Fatalf("append first query: %v", err)
	}

	nextSession := session
	nextSession.RunID = "run-2"
	secondPending, err := server.prepareSystemInitCache(req, &nextSession, false)
	if err != nil {
		t.Fatalf("second prepare system init cache: %v", err)
	}
	if secondPending != nil {
		t.Fatalf("expected second main query to dedup unchanged system init, got %#v", secondPending)
	}
}

func TestMainQueryDedupsSystemsWhenOnlyReferencesChange(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	firstSize := int64(537)
	secondSize := int64(812)
	req := api.QueryRequest{
		ChatID:  "chat-refs",
		Message: "hello #{r01}",
		References: []api.Reference{{
			ID:        "r01",
			Type:      "file",
			Name:      "sales.csv",
			MimeType:  "text/csv",
			SizeBytes: &firstSize,
		}},
	}
	toolDefs := []api.ToolDetailResponse{{Name: "datetime", Description: "get current time"}}
	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{defs: toolDefs},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}
	session := contracts.QuerySession{
		RunID:        "run-1",
		ChatID:       "chat-refs",
		AgentKey:     "agent",
		ModelKey:     "mock-model",
		ToolNames:    []string{"datetime"},
		Mode:         "REACT",
		ContextTags:  []string{"session"},
		PromptAppend: contracts.DefaultPromptAppendConfig(),
		RuntimeContext: contracts.RuntimeRequestContext{
			References: req.References,
		},
	}

	firstPending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("first prepare system init cache: %v", err)
	}
	if firstPending == nil {
		t.Fatalf("expected first main query to carry one system init, got %#v", firstPending)
	}
	if firstPending.CacheKey != "react:main" {
		t.Fatalf("unexpected first system init cache keys %#v", firstPending)
	}
	if _, _, err := store.EnsureChat(req.ChatID, session.AgentKey, "", req.Message); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 3_001
	startServerFixtureRun(t, store, req.ChatID, session.RunID, startedAt)
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     session.RunID,
		UpdatedAt: startedAt,
		Query: map[string]any{
			"role":       "user",
			"message":    req.Message,
			"agentKey":   session.AgentKey,
			"references": req.References,
		},
		System: firstPending,
	}); err != nil {
		t.Fatalf("append first query: %v", err)
	}

	nextReq := req
	nextReq.Message = "hello #{r02}"
	nextReq.References = []api.Reference{{
		ID:        "r02",
		Type:      "file",
		Name:      "returns.csv",
		MimeType:  "text/csv",
		SizeBytes: &secondSize,
	}}
	nextSession := session
	nextSession.RunID = "run-2"
	nextSession.RuntimeContext.References = nextReq.References
	secondPending, err := server.prepareSystemInitCache(nextReq, &nextSession, false)
	if err != nil {
		t.Fatalf("second prepare system init cache: %v", err)
	}
	if secondPending != nil {
		t.Fatalf("expected references-only change to dedup unchanged system init, got %#v", secondPending)
	}
}

func TestSystemInitDedupIsScopedByAgentKey(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	server := &Server{deps: Dependencies{
		Config:      config.Config{},
		Chats:       store,
		Tools:       systemInitStaticToolExecutor{},
		SystemInits: llm.SystemInitProfileBuilder{},
	}}
	const chatID = "chat-agent-systems"
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	register := func(agentKey, runID string) *chat.QueryLineSystemInit {
		session := contracts.QuerySession{
			RunID:        runID,
			ChatID:       chatID,
			AgentKey:     agentKey,
			ModelKey:     "mock-model",
			Mode:         "REACT",
			PromptAppend: contracts.DefaultPromptAppendConfig(),
		}
		system, err := server.prepareSystemInitCache(api.QueryRequest{ChatID: chatID, Message: "hello", AgentKey: agentKey}, &session, false)
		if err != nil {
			t.Fatalf("prepare %s: %v", agentKey, err)
		}
		return system
	}
	for index, item := range []struct{ agentKey, runID string }{{"agent-a", "run-a"}, {"agent-b", "run-b"}} {
		system := register(item.agentKey, item.runID)
		if system == nil || system.AgentKey != item.agentKey {
			t.Fatalf("expected new system for %s, got %#v", item.agentKey, system)
		}
		startedAt := testEpochMillis + int64(index+1)
		startServerFixtureRun(t, store, chatID, item.runID, startedAt)
		if err := store.AppendQueryLine(chatID, chat.QueryLine{
			Type:      "query",
			ChatID:    chatID,
			RunID:     item.runID,
			UpdatedAt: startedAt,
			Query:     map[string]any{"role": "user", "message": "hello", "agentKey": item.agentKey},
			System:    system,
		}); err != nil {
			t.Fatalf("append %s query: %v", item.agentKey, err)
		}
	}
	if system := register("agent-a", "run-a2"); system != nil {
		t.Fatalf("expected agent-a to reuse its own cached system after agent-b, got %#v", system)
	}
}
