package server

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/memory"
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
		if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(env), 0o644); err != nil {
			t.Fatalf("write runtime env: %v", err)
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

func TestResolveSkillRuntimeSettingsSupportsHyphenatedSkillIDs(t *testing.T) {
	marketDir := t.TempDir()
	platformAdminDir := writeSkillRuntimeFixture(t, marketDir, "platform-admin", `{"DANGEROUS_COMMANDS":"1"}`)

	hookDirs, env := resolveSkillRuntimeSettings(nil, "", marketDir, []string{"platform-admin"})
	if !reflect.DeepEqual(hookDirs, []string{filepath.Join(platformAdminDir, ".bash-hooks")}) {
		t.Fatalf("hookDirs = %#v", hookDirs)
	}
	if !reflect.DeepEqual(env, map[string]string{"DANGEROUS_COMMANDS": "1"}) {
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

func TestPrepareQueryUpdatesExistingChatAgentKey(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-agent-drift", "", "", "uploaded image"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	server := &Server{deps: Dependencies{
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-agent-drift","message":"use uploaded image"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}
	if prepared.summary.AgentKey != "agent-a" {
		t.Fatalf("expected prepared summary agent-a, got %q", prepared.summary.AgentKey)
	}

	summary, err := chats.Summary("chat-agent-drift")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.AgentKey != "agent-a" {
		t.Fatalf("expected stored agent-a, got %q", summary.AgentKey)
	}
}

func TestPrepareQueryNonSandboxAgentCreatesChatDirectory(t *testing.T) {
	chatsRoot := t.TempDir()
	chats, err := chat.NewFileStore(chatsRoot)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			Paths:        config.PathsConfig{ChatsDir: chatsRoot},
			ContainerHub: config.ContainerHubConfig{Enabled: false},
		},
		Chats: chats,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-no-dir","message":"hello"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}
	if prepared.session.AgentHasRuntimeSandbox {
		t.Fatal("expected non-sandbox session")
	}
	if stat, err := os.Stat(chats.ChatDir("chat-no-dir")); err != nil || !stat.IsDir() {
		t.Fatalf("expected chat directory to be created, stat=%#v err=%v", stat, err)
	}
	if prepared.session.RuntimeContext.LocalPaths.ChatAttachmentsDir != chats.ChatDir("chat-no-dir") {
		t.Fatalf("chat attachments dir = %q, want %q", prepared.session.RuntimeContext.LocalPaths.ChatAttachmentsDir, chats.ChatDir("chat-no-dir"))
	}
	if prepared.session.RuntimeContext.LocalPaths.WorkspaceDir != chats.ChatDir("chat-no-dir") {
		t.Fatalf("workspace dir = %q, want %q", prepared.session.RuntimeContext.LocalPaths.WorkspaceDir, chats.ChatDir("chat-no-dir"))
	}
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
		Title:      "Recent automation adjustment",
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
				Enabled:         true,
				ContextTopN:     5,
				ContextMaxChars: 4000,
			},
		},
		Chats:  chats,
		Memory: memories,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:           "agent-a",
				Name:          "Agent A",
				ModelKey:      "mock-model",
				MemoryEnabled: true,
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
		t.Fatalf("expected aggregate memory context to stay empty, got %q", prepared.session.MemoryContext)
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
	if got := prepared.memoryUsageSummary.UserHint; !containsAll(got, []string{"本次回答借鉴了历史记忆", "Work hours preference", "Recent automation adjust"}) {
		t.Fatalf("unexpected memory user hint: %q", got)
	}
	if prepared.session.MemoryUsageSummary == nil {
		t.Fatalf("expected session memory usage summary, got nil")
	}
}

func TestBuildMemoryHitItemsReflectsPromptInjectedRecords(t *testing.T) {
	bundle := memory.ContextBundle{
		StableFacts: []api.StoredMemoryResponse{
			{
				ID:       "fact-1",
				Kind:     memory.KindFact,
				Title:    "Automation rules summary",
				Summary:  "Automation rules summary",
				Category: "platform_rules",
			},
			{
				ID:       "fact-2",
				Kind:     memory.KindFact,
				Title:    "Automation rules summary",
				Summary:  "Automation rules summary for current agent",
				Category: "platform_rules",
			},
		},
		SessionSummaries: []api.StoredMemoryResponse{
			{
				ID:       "obs-1",
				Kind:     memory.KindObservation,
				Title:    "Recent automation adjustment",
				Summary:  "Recent automation adjustment",
				Category: "general",
			},
		},
	}

	items := buildMemoryHitItems(bundle)
	if len(items) != 3 {
		t.Fatalf("expected memory hits to reflect bundle items, got %#v", items)
	}
	if items[0].ID != "fact-1" || items[1].ID != "fact-2" || items[2].ID != "obs-1" {
		t.Fatalf("unexpected memory hit ordering: %#v", items)
	}
}

func TestPrepareQueryDedupesNearDuplicateStableFacts(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	now := int64(1_700_000_000_000)
	items := []api.StoredMemoryResponse{
		{
			ID:         "fact-1",
			AgentKey:   "agent-a",
			ChatID:     "chat-1",
			Kind:       memory.KindFact,
			ScopeType:  memory.ScopeAgent,
			ScopeKey:   "agent:agent-a",
			Title:      "Work hours baseline",
			Summary:    "用户每周需要保证 40 小时的工作时间。",
			SourceType: "tool-write",
			Category:   "user_preference",
			Importance: 9,
			Status:     memory.StatusActive,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:         "fact-2",
			AgentKey:   "agent-a",
			ChatID:     "chat-2",
			Kind:       memory.KindFact,
			ScopeType:  memory.ScopeAgent,
			ScopeKey:   "agent:agent-a",
			Title:      "Work hours baseline expanded",
			Summary:    "用户每周要保证40小时的工作时间，默认优先按工作日均摊（即每天8小时，按5个工作日计算）。",
			SourceType: "tool-write",
			Category:   "user_preference",
			Importance: 9,
			Status:     memory.StatusActive,
			CreatedAt:  now + 1,
			UpdatedAt:  now + 1,
		},
		{
			ID:         "fact-3",
			AgentKey:   "agent-a",
			ChatID:     "chat-3",
			Kind:       memory.KindFact,
			ScopeType:  memory.ScopeAgent,
			ScopeKey:   "agent:agent-a",
			Title:      "Break time rule",
			Summary:    "午休时间不计入工时。",
			SourceType: "tool-write",
			Category:   "user_preference",
			Importance: 8,
			Status:     memory.StatusActive,
			CreatedAt:  now + 2,
			UpdatedAt:  now + 2,
		},
	}
	for _, item := range items {
		if err := memories.Write(item); err != nil {
			t.Fatalf("write memory %s: %v", item.ID, err)
		}
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			Memory: config.MemoryConfig{
				Enabled:         true,
				ContextTopN:     5,
				ContextMaxChars: 4000,
			},
		},
		Chats:  chats,
		Memory: memories,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:           "agent-a",
				Name:          "Agent A",
				ModelKey:      "mock-model",
				MemoryEnabled: true,
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"帮我安排本周工时"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}

	if strings.Count(prepared.session.StableMemoryContext, "- ") != 2 {
		t.Fatalf("expected 2 stable bullets after dedupe, got %q", prepared.session.StableMemoryContext)
	}
	if !strings.Contains(prepared.session.StableMemoryContext, "每天8小时") {
		t.Fatalf("expected richer duplicate winner to survive, got %q", prepared.session.StableMemoryContext)
	}
	if prepared.memoryUsageSummary == nil || prepared.memoryUsageSummary.SelectedCounts["stable"] != 2 {
		t.Fatalf("expected stable selected count 2, got %#v", prepared.memoryUsageSummary)
	}
}

func TestPrepareQueryDedupesNearDuplicateAcrossStableAndSession(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	now := int64(1_700_000_000_000)
	stored := []api.StoredMemoryResponse{
		{
			ID:         "fact-1",
			AgentKey:   "agent-a",
			ChatID:     "chat-1",
			Kind:       memory.KindFact,
			ScopeType:  memory.ScopeAgent,
			ScopeKey:   "agent:agent-a",
			Title:      "Work hours baseline",
			Summary:    "用户每周要保证40小时的工作时间，默认优先按工作日均摊（即每天8小时，按5个工作日计算）。",
			SourceType: "tool-write",
			Category:   "user_preference",
			Importance: 9,
			Status:     memory.StatusActive,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:         "obs-1",
			AgentKey:   "agent-a",
			ChatID:     "chat-1",
			Kind:       memory.KindObservation,
			ScopeType:  memory.ScopeChat,
			ScopeKey:   "chat:chat-1",
			Title:      "Current automation rule",
			Summary:    "本周仍按每周40小时、5个工作日每天8小时来记录工时。",
			SourceType: "learn",
			Category:   "user_preference",
			Importance: 8,
			Status:     memory.StatusOpen,
			CreatedAt:  now + 1,
			UpdatedAt:  now + 1,
		},
	}
	for _, item := range stored {
		if err := memories.Write(item); err != nil {
			t.Fatalf("write memory %s: %v", item.ID, err)
		}
	}

	server := &Server{deps: Dependencies{
		Config: config.Config{
			Memory: config.MemoryConfig{
				Enabled:         true,
				ContextTopN:     5,
				ContextMaxChars: 4000,
			},
		},
		Chats:  chats,
		Memory: memories,
		Registry: queryMemoryRegistry{
			def: catalog.AgentDefinition{
				Key:           "agent-a",
				Name:          "Agent A",
				ModelKey:      "mock-model",
				MemoryEnabled: true,
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"帮我记录本周工时"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}

	if prepared.session.SessionMemoryContext != "" {
		t.Fatalf("expected duplicate session memory to be removed, got %q", prepared.session.SessionMemoryContext)
	}
	if prepared.memoryUsageSummary == nil || prepared.memoryUsageSummary.SelectedCounts["session"] != 0 {
		t.Fatalf("expected session selected count 0, got %#v", prepared.memoryUsageSummary)
	}
}

func TestPrepareQuerySkipsMemoryContextWhenMemorySystemDisabled(t *testing.T) {
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
				Key:      "agent-a",
				Name:     "Agent A",
				ModelKey: "mock-model",
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"安排下周工时"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}
	if prepared.session.StableMemoryContext != "" || prepared.session.SessionMemoryContext != "" || prepared.session.ObservationContext != "" {
		t.Fatalf("expected no memory context when memory system disabled, got stable=%q session=%q obs=%q", prepared.session.StableMemoryContext, prepared.session.SessionMemoryContext, prepared.session.ObservationContext)
	}
	if prepared.memoryUsageSummary != nil || prepared.session.MemoryUsageSummary != nil {
		t.Fatalf("expected no memory usage summary when memory system disabled, got %#v %#v", prepared.memoryUsageSummary, prepared.session.MemoryUsageSummary)
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
				Runtime: map[string]any{
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

func TestPrepareQueryAllowsRuntimeEnvWithoutContainerHub(t *testing.T) {
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
				Runtime: map[string]any{
					"env": map[string]string{
						"HTTP_PROXY": "http://127.0.0.1:8001",
					},
				},
			},
		},
	}}

	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(`{"agentKey":"agent-a","chatId":"chat-1","message":"列出目录"}`))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}
	if prepared.session.AgentHasRuntimeSandbox {
		t.Fatal("expected env-only runtime config to avoid sandbox routing")
	}
	if got := prepared.session.RuntimeEnvOverrides["HTTP_PROXY"]; got != "http://127.0.0.1:8001" {
		t.Fatalf("RuntimeEnvOverrides[HTTP_PROXY] = %q", got)
	}
	if !containsString(prepared.session.ToolNames, "bash") {
		t.Fatalf("expected bash tool for runtime env overrides, got %#v", prepared.session.ToolNames)
	}
}

func TestPrepareQueryDesktopParamsDoNotGrantToolsOrRuntimeEnv(t *testing.T) {
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
				Runtime: map[string]any{
					"env": map[string]string{
						"CDP_HOST": "127.0.0.1",
						"CDP_PORT": "11789",
					},
				},
				Tools: []string{"datetime"},
			},
		},
	}}

	body := `{"agentKey":"agent-a","chatId":"chat-1","message":"看当前页面","params":{"desktop":{"surfaceId":"surface-a","source":"copilot"}}}`
	req := httptest.NewRequest("POST", "/api/query", bytes.NewBufferString(body))
	prepared, err := server.prepareQuery(req)
	if err != nil {
		t.Fatalf("prepareQuery: %v", err)
	}

	if containsString(prepared.session.ToolNames, "desktop_action") || containsString(prepared.session.ToolNames, "desktop_cdp") {
		t.Fatalf("did not expect desktop tools from params.desktop, got %#v", prepared.session.ToolNames)
	}
	if !reflect.DeepEqual(prepared.session.ToolNames, []string{"datetime", "bash"}) {
		t.Fatalf("unexpected tool names: %#v", prepared.session.ToolNames)
	}
	if _, ok := prepared.session.RuntimeEnvOverrides["ZENMIND_CDP_AGENT_KEY"]; ok {
		t.Fatalf("did not expect ZENMIND_CDP_AGENT_KEY injection: %#v", prepared.session.RuntimeEnvOverrides)
	}
	if _, ok := prepared.session.RuntimeEnvOverrides["ZENMIND_CDP_SURFACE_ID"]; ok {
		t.Fatalf("did not expect ZENMIND_CDP_SURFACE_ID injection: %#v", prepared.session.RuntimeEnvOverrides)
	}
	if prepared.session.RuntimeEnvOverrides["CDP_PORT"] != "11789" {
		t.Fatalf("expected explicit CDP_PORT to remain unchanged, got %#v", prepared.session.RuntimeEnvOverrides)
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
