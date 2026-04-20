package server

import (
	"bytes"
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

type skillRuntimeRegistry struct {
	testCatalogRegistry
	skills map[string]catalog.SkillDefinition
}

func (r skillRuntimeRegistry) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	def, ok := r.skills[key]
	return def, ok
}

func TestResolveSkillRuntimeSettingsMergesEnvAndHookDirsInOrder(t *testing.T) {
	registry := skillRuntimeRegistry{
		skills: map[string]catalog.SkillDefinition{
			"alpha": {
				Key:          "alpha",
				BashHooksDir: "/skills/alpha/.bash-hooks",
				SandboxEnv: map[string]string{
					"NODE_ENV": "development",
					"DEBUG":    "1",
				},
			},
			"beta": {
				Key:          "beta",
				BashHooksDir: "/skills/beta/.bash-hooks",
				SandboxEnv: map[string]string{
					"NODE_ENV": "production",
					"TZ":       "UTC",
				},
			},
		},
	}

	agentEnv := map[string]string{
		"NODE_ENV": "test",
		"BASE":     "1",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, []string{"alpha", "beta", "alpha"}, registry)
	if !reflect.DeepEqual(hookDirs, []string{"/skills/alpha/.bash-hooks", "/skills/beta/.bash-hooks"}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	wantEnv := map[string]string{
		"NODE_ENV": "production",
		"BASE":     "1",
		"DEBUG":    "1",
		"TZ":       "UTC",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
	}
}

func TestResolveSkillRuntimeSettingsSkipsMissingSkills(t *testing.T) {
	registry := skillRuntimeRegistry{
		skills: map[string]catalog.SkillDefinition{
			"beta": {
				Key:          "beta",
				BashHooksDir: "/skills/beta/.bash-hooks",
				SandboxEnv: map[string]string{
					"TZ": "UTC",
				},
			},
		},
	}

	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, []string{"missing", "beta"}, registry)
	if !reflect.DeepEqual(hookDirs, []string{"/skills/beta/.bash-hooks"}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	if !reflect.DeepEqual(env, map[string]string{"HTTP_PROXY": "http://agent", "TZ": "UTC"}) {
		t.Fatalf("env = %#v", env)
	}
}

func TestResolveSkillRuntimeSettingsReturnsAgentEnvWithoutSkills(t *testing.T) {
	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}

	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, nil, nil)
	if hookDirs != nil {
		t.Fatalf("hookDirs = %#v, want nil", hookDirs)
	}
	if !reflect.DeepEqual(env, agentEnv) {
		t.Fatalf("env = %#v, want %#v", env, agentEnv)
	}
	if env["HTTP_PROXY"] != "http://agent" {
		t.Fatalf("expected cloned env to preserve values, got %#v", env)
	}
}

func (skillRuntimeRegistry) Agents(string) []api.AgentSummary       { return nil }
func (skillRuntimeRegistry) Teams() []api.TeamSummary               { return nil }
func (skillRuntimeRegistry) Skills(string) []api.SkillSummary       { return nil }
func (skillRuntimeRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (skillRuntimeRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (skillRuntimeRegistry) DefaultAgentKey() string { return "" }
func (skillRuntimeRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{}, false
}
func (skillRuntimeRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (skillRuntimeRegistry) Reload(context.Context, string) error { return nil }

type queryMemoryRegistry struct {
	testCatalogRegistry
	def catalog.AgentDefinition
}

func (r queryMemoryRegistry) DefaultAgentKey() string { return r.def.Key }

func (r queryMemoryRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if key == r.def.Key {
		return r.def, true
	}
	return catalog.AgentDefinition{}, false
}

func TestPrepareQueryBuildsLayeredMemoryContexts(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	now := int64(1_700_000_000_000)
	if err := memories.Write(api.StoredMemoryResponse{
		ID:         "fact-1",
		AgentKey:   "agent-a",
		ChatID:     "chat-1",
		Kind:       memory.KindFact,
		ScopeType:  memory.ScopeAgent,
		ScopeKey:   "agent:agent-a",
		Title:      "Work hours preference",
		Summary:    "每周工作时间保持 40h。",
		SourceType: "tool-write",
		Category:   "preference",
		Importance: 9,
		Status:     memory.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("write fact memory: %v", err)
	}
	if err := memories.Write(api.StoredMemoryResponse{
		ID:         "obs-1",
		AgentKey:   "agent-a",
		ChatID:     "chat-1",
		Kind:       memory.KindObservation,
		ScopeType:  memory.ScopeChat,
		ScopeKey:   "chat:chat-1",
		Title:      "Recent schedule adjustment",
		Summary:    "上次已经调整过下周工时安排，继续安排下周工时时要参考这个结果。",
		SourceType: "learn",
		Category:   "general",
		Importance: 7,
		Status:     memory.StatusOpen,
		CreatedAt:  now + 1,
		UpdatedAt:  now + 1,
	}); err != nil {
		t.Fatalf("write observation memory: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			Memory: config.MemoryConfig{
				ContextTopN:     5,
				ContextMaxChars: 4000,
			},
		},
		Chats:  chats,
		Memory: memories,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:         "agent-a",
				Name:        "Agent A",
				ModelKey:    "mock-model",
				ContextTags: []string{"memory"},
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"安排下周工时"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}

	if prepared.session.StableMemoryContext == "" {
		t.Fatalf("expected stable memory context, got empty")
	}
	if prepared.session.SessionMemoryContext == "" {
		t.Fatalf("expected session memory context, got empty")
	}
	if prepared.session.MemoryContext != "" {
		t.Fatalf("expected legacy memory context to stay empty, got %q", prepared.session.MemoryContext)
	}
	if prepared.session.Subject != "" {
		t.Fatalf("expected anonymous subject to stay empty, got %q", prepared.session.Subject)
	}
	if got := prepared.session.StableMemoryContext; !containsAll(got, []string{"Runtime Context: Stable Memory", "每周工作时间保持 40h"}) {
		t.Fatalf("unexpected stable memory context: %q", got)
	}
	if got := prepared.session.SessionMemoryContext; !containsAll(got, []string{"Runtime Context: Current Session", "调整过下周工时安排"}) {
		t.Fatalf("unexpected session memory context: %q", got)
	}
	if prepared.memoryUsageSummary == nil {
		t.Fatalf("expected memory usage summary, got nil")
	}
	if prepared.memoryUsageSummary.StableCount != 1 || prepared.memoryUsageSummary.SessionCount != 1 {
		t.Fatalf("unexpected memory usage counts: %#v", prepared.memoryUsageSummary)
	}
	if prepared.memoryUsageSummary.SnapshotID == "" {
		t.Fatalf("expected snapshot id in memory usage summary, got empty")
	}
	if prepared.memoryUsageSummary.StopReason != "session_added" {
		t.Fatalf("expected stop reason session_added, got %#v", prepared.memoryUsageSummary.StopReason)
	}
	if !reflect.DeepEqual(prepared.memoryUsageSummary.DisclosedLayers, []string{"stable", "session"}) {
		t.Fatalf("unexpected disclosed layers: %#v", prepared.memoryUsageSummary.DisclosedLayers)
	}
	if got := prepared.memoryUsageSummary.CandidateCounts["stable"]; got != 1 {
		t.Fatalf("expected stable candidate count 1, got %#v", prepared.memoryUsageSummary.CandidateCounts)
	}
	if got := prepared.memoryUsageSummary.SelectedCounts["session"]; got != 1 {
		t.Fatalf("expected session selected count 1, got %#v", prepared.memoryUsageSummary.SelectedCounts)
	}
	if len(prepared.memoryUsageSummary.StableItems) != 1 || prepared.memoryUsageSummary.StableItems[0].Summary != "每周工作时间保持 40h。" {
		t.Fatalf("unexpected stable memory items: %#v", prepared.memoryUsageSummary.StableItems)
	}
	if len(prepared.memoryUsageSummary.SessionItems) != 1 || prepared.memoryUsageSummary.SessionItems[0].Summary != "上次已经调整过下周工时安排，继续安排下周工时时要参考这个结果。" {
		t.Fatalf("unexpected session memory items: %#v", prepared.memoryUsageSummary.SessionItems)
	}
	if prepared.session.MemoryUsageSummary == nil {
		t.Fatalf("expected session memory usage summary, got nil")
	}
}

func containsAll(text string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}
