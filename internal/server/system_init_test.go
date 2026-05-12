package server

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
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
	oldProfiles := llm.BuildSystemInitProfiles(oldSession, req, toolDefs, 0, 0)
	if len(oldProfiles) != 1 {
		t.Fatalf("expected one system init profile, got %#v", oldProfiles)
	}
	cachedTools := []any{map[string]any{"source": "disk-cache"}}
	if err := store.AppendSystemInitLine(req.ChatID, chat.SystemInitLine{
		ChatID:        req.ChatID,
		AgentKey:      oldSession.AgentKey,
		RunID:         oldSession.RunID,
		CreatedAt:     time.Now().UnixMilli(),
		Fingerprint:   oldProfiles[0].Fingerprint,
		CacheKey:      oldProfiles[0].CacheKey,
		Mode:          oldProfiles[0].Mode,
		Stage:         oldProfiles[0].Stage,
		SystemMessage: oldProfiles[0].SystemMessage,
		Tools:         cachedTools,
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

	if err := server.prepareSystemInitCache(req, &newSession, false); err != nil {
		t.Fatalf("prepare system init cache: %v", err)
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
