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

func TestPrepareSystemInitCacheUsesFreshSystemMessageOnFingerprintMatch(t *testing.T) {
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
	oldProfiles := llm.BuildSystemInitProfiles(oldSession, req, toolDefs, 0, 0, config.PromptsConfig{})
	if len(oldProfiles) != 1 {
		t.Fatalf("expected one system init profile, got %#v", oldProfiles)
	}
	cachedTools := []any{map[string]any{"source": "disk-cache"}}
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     oldSession.RunID,
		UpdatedAt: 1001,
		Query:     map[string]any{"role": "user", "message": "hello", "agentKey": oldSession.AgentKey},
		Systems: []chat.QueryLineSystemInit{{
			Fingerprint:   oldProfiles[0].Fingerprint,
			CacheKey:      oldProfiles[0].CacheKey,
			SystemMessage: oldProfiles[0].SystemMessage,
			Tools:         cachedTools,
		}},
	}); err != nil {
		t.Fatalf("append system init: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{},
		Chats:  store,
		Tools:  systemInitStaticToolExecutor{defs: toolDefs},
	}}
	newSession := oldSession
	newSession.RunID = "run-new"
	newSession.SessionMemoryContext = "Runtime Context: Current Session\n- fresh session memory"
	newSession.ObservationContext = "Runtime Context: Relevant Observations\n- fresh observation"

	pending, err := server.prepareSystemInitCache(req, &newSession, false)
	if err != nil {
		t.Fatalf("prepare system init cache: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("did not expect unchanged fingerprint to append system cache line, got %#v", pending)
	}
	snapshot, ok := newSession.SystemInitCache["react:main"]
	if !ok {
		t.Fatalf("missing react cache snapshot %#v", newSession.SystemInitCache)
	}
	content, _ := snapshot.SystemMessage["content"].(string)
	if !strings.Contains(content, "fresh session memory") || strings.Contains(content, "stale session memory") {
		t.Fatalf("expected fresh dynamic system message, got %q", content)
	}
	if !reflect.DeepEqual(snapshot.Tools, cachedTools) {
		t.Fatalf("expected cached tools %#v, got %#v", cachedTools, snapshot.Tools)
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
		Config: config.Config{},
		Chats:  store,
		Tools:  systemInitStaticToolExecutor{defs: toolDefs},
	}}

	pending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("prepare system init cache: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending system cache line, got %#v", pending)
	}
	if pending[0].CacheKey != "react:main" || pending[0].Fingerprint == "" {
		t.Fatalf("unexpected pending system cache line %#v", pending[0])
	}
	if _, ok := session.SystemInitCache["react:main"]; !ok {
		t.Fatalf("expected session cache to be populated, got %#v", session.SystemInitCache)
	}
	loaded, err := store.LoadSystemInit(req.ChatID, "react:main")
	if err != nil {
		t.Fatalf("load system init: %v", err)
	}
	if loaded != nil {
		t.Fatalf("prepare should not append before query, got %#v", loaded)
	}
}

func TestMainQueryStillDedupsSystemsByFingerprint(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	req := api.QueryRequest{ChatID: "chat-1", Message: "hello"}
	toolDefs := []api.ToolDetailResponse{{Name: "datetime", Description: "get current time"}}
	server := &Server{deps: Dependencies{
		Config: config.Config{},
		Chats:  store,
		Tools:  systemInitStaticToolExecutor{defs: toolDefs},
	}}
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
	}

	firstPending, err := server.prepareSystemInitCache(req, &session, true)
	if err != nil {
		t.Fatalf("first prepare system init cache: %v", err)
	}
	if len(firstPending) != 1 {
		t.Fatalf("expected first main query to carry system init, got %#v", firstPending)
	}
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     session.RunID,
		UpdatedAt: 1001,
		Query:     map[string]any{"role": "user", "message": req.Message, "agentKey": session.AgentKey},
		Systems:   firstPending,
	}); err != nil {
		t.Fatalf("append first query: %v", err)
	}

	nextSession := session
	nextSession.RunID = "run-2"
	secondPending, err := server.prepareSystemInitCache(req, &nextSession, false)
	if err != nil {
		t.Fatalf("second prepare system init cache: %v", err)
	}
	if len(secondPending) != 0 {
		t.Fatalf("expected second main query to dedup unchanged system init, got %#v", secondPending)
	}
}
