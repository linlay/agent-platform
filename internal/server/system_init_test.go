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
	oldProfiles := llm.BuildSystemInitProfiles(oldSession, req, toolDefs, 0, 0, config.PromptsConfig{})
	if len(oldProfiles) != 1 {
		t.Fatalf("expected one system init profile, got %#v", oldProfiles)
	}
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
			Tools:         oldProfiles[0].Tools,
		}},
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
	if len(pending) != 2 {
		t.Fatalf("expected changed system payload and final profile to append system cache lines, got %#v", pending)
	}
	if pending[0].Fingerprint != oldProfiles[0].Fingerprint {
		t.Fatalf("expected same fingerprint to be retained, got %#v", pending[0])
	}
	snapshot, ok := newSession.SystemInitCache["react:main"]
	if !ok {
		t.Fatalf("missing react cache snapshot %#v", newSession.SystemInitCache)
	}
	content, _ := snapshot.SystemMessage["content"].(string)
	if !strings.Contains(content, "fresh session memory") || strings.Contains(content, "stale session memory") {
		t.Fatalf("expected fresh dynamic system message, got %q", content)
	}
	pendingContent, _ := pending[0].SystemMessage["content"].(string)
	if pendingContent != content {
		t.Fatalf("expected pending system to match session cache, pending=%q cache=%q", pendingContent, content)
	}
	if !reflect.DeepEqual(snapshot.Tools, pending[0].Tools) {
		t.Fatalf("expected session cache tools to match pending tools, pending=%#v cache=%#v", pending[0].Tools, snapshot.Tools)
	}
	if _, ok := newSession.SystemInitCache["react:main:final"]; !ok {
		t.Fatalf("expected final profile cache to be populated, got %#v", newSession.SystemInitCache)
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
	if len(pending) != 2 {
		t.Fatalf("expected main and final pending system cache lines, got %#v", pending)
	}
	if pending[0].CacheKey != "react:main" || pending[0].Fingerprint == "" {
		t.Fatalf("unexpected pending system cache line %#v", pending[0])
	}
	if _, ok := session.SystemInitCache["react:main"]; !ok {
		t.Fatalf("expected session cache to be populated, got %#v", session.SystemInitCache)
	}
	if _, ok := session.SystemInitCache["react:main:final"]; !ok {
		t.Fatalf("expected final session cache to be populated, got %#v", session.SystemInitCache)
	}
	loaded, err := store.LoadSystemInit(req.ChatID, "react:main")
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
	if len(pending) != 4 {
		t.Fatalf("expected plan/execute/final/summary profiles in query systems, got %#v", pending)
	}
	if pending[0].CacheKey != "plan-execute:plan" {
		t.Fatalf("expected plan profile first, got %#v", pending[0])
	}
	if _, ok := session.SystemInitCache["plan-execute:plan"]; !ok {
		t.Fatalf("expected plan profile cached, got %#v", session.SystemInitCache)
	}
	if _, ok := session.SystemInitCache["plan-execute:execute"]; !ok {
		t.Fatalf("expected execute profile cached, got %#v", session.SystemInitCache)
	}
	if _, ok := session.SystemInitCache["plan-execute:execute:final"]; !ok {
		t.Fatalf("expected execute final profile cached, got %#v", session.SystemInitCache)
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
	if len(firstPending) != 2 {
		t.Fatalf("expected first main query to carry main and final system init, got %#v", firstPending)
	}
	if firstPending[0].CacheKey != "react:main" || firstPending[1].CacheKey != "react:main:final" {
		t.Fatalf("unexpected first system init cache keys %#v", firstPending)
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
	if len(firstPending) != 2 {
		t.Fatalf("expected first main query to carry main and final system init, got %#v", firstPending)
	}
	if firstPending[0].CacheKey != "react:main" || firstPending[1].CacheKey != "react:main:final" {
		t.Fatalf("unexpected first system init cache keys %#v", firstPending)
	}
	if err := store.AppendQueryLine(req.ChatID, chat.QueryLine{
		Type:      "query",
		ChatID:    req.ChatID,
		RunID:     session.RunID,
		UpdatedAt: 1001,
		Query: map[string]any{
			"role":       "user",
			"message":    req.Message,
			"agentKey":   session.AgentKey,
			"references": req.References,
		},
		Systems: firstPending,
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
	if len(secondPending) != 0 {
		t.Fatalf("expected references-only change to dedup unchanged system init, got %#v", secondPending)
	}
}
