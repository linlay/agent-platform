package server

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

func writeSkillRuntimeFixture(t *testing.T, root string, skillID string, env string) string {
	t.Helper()

	skillDir := filepath.Join(root, skillID)
	if err := os.MkdirAll(filepath.Join(skillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir skill hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+skillID+"\n\nskill"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if strings.TrimSpace(env) != "" {
		if err := os.WriteFile(filepath.Join(skillDir, ".sandbox-env.json"), []byte(env), 0o644); err != nil {
			t.Fatalf("write sandbox env: %v", err)
		}
	}
	return skillDir
}

func TestResolveSkillRuntimeSettingsMergesEnvAndHookDirsInOrder(t *testing.T) {
	agentDir := t.TempDir()
	marketDir := t.TempDir()
	alphaDir := writeSkillRuntimeFixture(t, filepath.Join(agentDir, "skills"), "alpha", `{"NODE_ENV":"development","DEBUG":"1"}`)
	betaDir := writeSkillRuntimeFixture(t, marketDir, "beta", `{"NODE_ENV":"production","TZ":"UTC"}`)

	agentEnv := map[string]string{
		"NODE_ENV": "test",
		"BASE":     "1",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, agentDir, marketDir, []string{"alpha", "beta", "alpha"})
	if !reflect.DeepEqual(hookDirs, []string{
		filepath.Join(alphaDir, ".bash-hooks"),
		filepath.Join(betaDir, ".bash-hooks"),
	}) {
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
	marketDir := t.TempDir()
	betaDir := writeSkillRuntimeFixture(t, marketDir, "beta", `{"TZ":"UTC"}`)

	agentEnv := map[string]string{
		"HTTP_PROXY": "http://agent",
	}
	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, "", marketDir, []string{"missing", "beta"})
	if !reflect.DeepEqual(hookDirs, []string{filepath.Join(betaDir, ".bash-hooks")}) {
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

	hookDirs, env := resolveSkillRuntimeSettings(agentEnv, "", "", nil)
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

func TestPrepareQueryFailsFastWhenSandboxAgentRequiresDisabledContainerHub(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			ContainerHub: config.ContainerHubConfig{Enabled: false},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
				Sandbox: map[string]any{
					"environmentId": "shell",
				},
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"列出目录"}`))
	_, err = server.prepareQuery(req)
	if err == nil {
		t.Fatal("expected prepareQuery to fail when sandbox agent requires disabled container-hub")
	}
	if !strings.Contains(err.Error(), `agent "agent-a" requires sandbox but container-hub is disabled`) {
		t.Fatalf("unexpected error: %v", err)
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
