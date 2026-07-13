package llm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestSystemInitFingerprintStableAndToolOrderIndependent(t *testing.T) {
	session := fingerprintTestSession()
	toolsA := []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
		{Name: "datetime", Description: "get time", Parameters: map[string]any{"type": "object"}},
	}
	toolsB := []api.ToolDetailResponse{toolsA[1], toolsA[0]}

	first := ComputeSystemInitFingerprint(session, "main", toolsA)
	second := ComputeSystemInitFingerprint(session, "main", toolsB)
	if first == "" || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("unexpected fingerprint %q", first)
	}
	if first != second {
		t.Fatalf("expected tool order independent fingerprint, got %q and %q", first, second)
	}
}

func TestSystemInitFingerprintIgnoresRequestDynamicContext(t *testing.T) {
	session := fingerprintTestSession()
	changed := session
	changed.RequestID = "request-2"
	changed.RunID = "run-2"
	changed.StableMemoryContext = "Runtime Context: Stable Memory\n- changed"
	changed.SessionMemoryContext = "Runtime Context: Current Session\n- changed"
	changed.ObservationContext = "Runtime Context: Relevant Observations\n- changed"
	changed.RuntimeContext.References = []api.Reference{{Name: "new-ref"}}

	tools := []api.ToolDetailResponse{{Name: "bash", Description: "run shell"}}
	first := ComputeSystemInitFingerprint(session, "main", tools)
	second := ComputeSystemInitFingerprint(changed, "main", tools)
	if first != second {
		t.Fatalf("expected dynamic request context to be excluded, got %q and %q", first, second)
	}
}

func TestSystemInitProfileBuilderAddsRequestProfiles(t *testing.T) {
	registry := newSystemInitTestModelRegistry(t)
	session := contracts.QuerySession{
		RunID:        "run-1",
		ChatID:       "chat-1",
		AgentKey:     "agent",
		ModelKey:     "mock-model",
		ToolNames:    []string{"datetime"},
		Mode:         "REACT",
		PromptAppend: contracts.DefaultPromptAppendConfig(),
	}
	toolDefs := []api.ToolDetailResponse{{
		Name:        "datetime",
		Description: "get current time",
		Parameters:  map[string]any{"type": "object"},
	}}

	profiles, err := NewSystemInitProfileBuilder(registry, SystemInitDefaults{}).BuildSystemInitProfiles(contracts.SystemInitBuildInput{
		Session:         session,
		Request:         api.QueryRequest{ChatID: "chat-1", RunID: "run-1", Message: "hello"},
		ToolDefinitions: toolDefs,
	})
	if err != nil {
		t.Fatalf("build system init profiles: %v", err)
	}

	byKey := map[string]contracts.SystemInitProfile{}
	for _, profile := range profiles {
		byKey[profile.CacheKey] = profile
	}
	main := byKey["react:main"]
	if main.Fingerprint == "" || len(main.Tools) != 1 {
		t.Fatalf("expected main profile with tools, got %#v", main)
	}
	if main.ToolChoice != "auto" {
		t.Fatalf("expected main toolChoice auto, got %#v", main)
	}
	if main.Model["id"] != "mock-model-id" || main.Model["endpoint"] != "http://example.test/v1/chat/completions" {
		t.Fatalf("expected model snapshot, got %#v", main.Model)
	}
	if main.RequestOptions["temperature"] != float64(0) || main.RequestOptions["stream"] != true {
		t.Fatalf("expected provider request options, got %#v", main.RequestOptions)
	}
	for _, key := range []string{"messages", "tools", "tool_choice", "model", "system"} {
		if _, ok := main.RequestOptions[key]; ok {
			t.Fatalf("requestOptions must not include %s: %#v", key, main.RequestOptions)
		}
	}

	if _, ok := byKey["react:main:final"]; ok {
		t.Fatalf("did not expect unused final profile to be generated: %#v", byKey)
	}
}

func TestValidateSystemInitProfilesRequiresUniqueCacheKeysAndInitial(t *testing.T) {
	tests := []struct {
		name     string
		profiles []contracts.SystemInitProfile
	}{
		{name: "empty agent key", profiles: []contracts.SystemInitProfile{{CacheKey: "react:main", Initial: true}}},
		{name: "empty cache key", profiles: []contracts.SystemInitProfile{{AgentKey: "agent", Initial: true}}},
		{name: "duplicate cache key", profiles: []contracts.SystemInitProfile{{AgentKey: "agent", CacheKey: "react:main", Initial: true}, {AgentKey: "agent", CacheKey: "react:main"}}},
		{name: "missing initial", profiles: []contracts.SystemInitProfile{{AgentKey: "agent", CacheKey: "react:main"}}},
		{name: "multiple initial", profiles: []contracts.SystemInitProfile{{AgentKey: "agent", CacheKey: "coder:planning", Initial: true}, {AgentKey: "agent", CacheKey: "coder:execute", Initial: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSystemInitProfiles(tt.profiles); err == nil {
				t.Fatalf("expected invalid profiles to fail: %#v", tt.profiles)
			}
		})
	}
	if err := validateSystemInitProfiles([]contracts.SystemInitProfile{
		{AgentKey: "agent", CacheKey: "coder:planning", Initial: true},
		{AgentKey: "agent", CacheKey: "coder:execute"},
	}); err != nil {
		t.Fatalf("valid profiles rejected: %v", err)
	}
}

func TestBuiltSystemInitProfilesHaveExactlyOneInitial(t *testing.T) {
	sessions := []contracts.QuerySession{
		{AgentKey: "react", Mode: "REACT"},
		{AgentKey: "coder", Mode: "CODER"},
		{AgentKey: "coder-planning", Mode: "CODER", PlanningMode: true},
		{AgentKey: "kbase", Mode: "KBASE"},
		{AgentKey: "pipeline", Mode: "PLAN_EXECUTE"},
	}
	for _, session := range sessions {
		profiles := BuildSystemInitProfiles(session, api.QueryRequest{Message: "hello"}, nil, 12, 4, 12, config.PromptsConfig{})
		if err := validateSystemInitProfiles(profiles); err != nil {
			t.Fatalf("mode %s produced invalid profiles: %v (%#v)", session.Mode, err, profiles)
		}
	}
}

func TestSystemInitFingerprintChangesWithPromptAndStage(t *testing.T) {
	session := fingerprintTestSession()
	tools := []api.ToolDetailResponse{{Name: "bash", Description: "run shell"}}
	base := ComputeSystemInitFingerprint(session, "main", tools)

	changedPrompt := session
	changedPrompt.SoulPrompt = "new soul"
	if got := ComputeSystemInitFingerprint(changedPrompt, "main", tools); got == base {
		t.Fatalf("expected prompt change to update fingerprint")
	}
	if got := ComputeSystemInitFingerprint(session, "plan", tools); got == base {
		t.Fatalf("expected stage change to update fingerprint")
	}
}

func TestCachedSystemInitConversions(t *testing.T) {
	profiles := BuildSystemInitProfiles(fingerprintTestSession(), api.QueryRequest{ChatID: "chat-1", Message: "hello"}, []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
	}, 12, 4, 12, config.PromptsConfig{})
	if len(profiles) != 1 {
		t.Fatalf("expected one profile, got %#v", profiles)
	}
	systemMessage, ok := cachedSystemMessageToOpenAI(profiles[0].SystemMessage)
	if !ok || systemMessage.Role != "system" {
		t.Fatalf("unexpected cached system message %#v", systemMessage)
	}
	specs, err := cachedToolSpecsToOpenAI(profiles[0].Tools)
	if err != nil {
		t.Fatalf("cached tool specs: %v", err)
	}
	if len(specs) != 1 || specs[0].Function.Name != "bash" {
		t.Fatalf("unexpected specs %#v", specs)
	}
	if !reflect.DeepEqual(openAIToolSpecsToAny(specs), profiles[0].Tools) {
		t.Fatalf("expected tools to round trip, got %#v", openAIToolSpecsToAny(specs))
	}
}

func TestPlanExecuteSystemInitProfilesUseRuntimeSettings(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "PLAN_EXECUTE"
	session.ToolNames = []string{"bash"}
	session.ResolvedPlanExecuteSettings = contracts.PlanExecuteSettings{}
	session.StageSettings = map[string]any{
		"plan": map[string]any{
			"toolConfig": map[string]any{
				"tools": []any{"custom_plan"},
			},
		},
		"execute": map[string]any{
			"instructionsPrompt": "execute primary",
			"toolConfig": map[string]any{
				"tools": []any{"bash", "custom_exec"},
			},
		},
		"summary": map[string]any{
			"instructionsPrompt": "summary primary",
		},
	}
	toolDefs := []api.ToolDetailResponse{
		{Name: "custom_plan", Description: "plan"},
		{Name: "plan_add_tasks", Description: "add tasks"},
		{Name: "bash", Description: "run shell"},
		{Name: "custom_exec", Description: "exec"},
		{Name: "plan_update_task", Description: "update task"},
	}

	settings := resolvePlanExecuteRuntimeSettings(session, 12, 4)
	if settings.MaxSteps != 12 || settings.MaxWorkRoundsPerTask != 4 {
		t.Fatalf("expected runtime defaults to be applied, got %#v", settings)
	}

	profiles := BuildSystemInitProfiles(session, api.QueryRequest{ChatID: "chat-1", Message: "hello"}, toolDefs, 12, 4, 12, config.PromptsConfig{})
	if len(profiles) != 3 {
		t.Fatalf("expected plan/execute/summary profiles, got %#v", profiles)
	}
	byKey := map[string]contracts.SystemInitProfile{}
	for _, profile := range profiles {
		byKey[profile.CacheKey] = profile
	}
	if _, ok := byKey["plan-execute:plan"]; !ok {
		t.Fatalf("missing plan profile %#v", byKey)
	}
	if _, ok := byKey["plan-execute:execute"]; !ok {
		t.Fatalf("missing execute profile %#v", byKey)
	}
	if _, ok := byKey["plan-execute:summary"]; !ok {
		t.Fatalf("missing summary profile %#v", byKey)
	}

	assertToolNames(t, byKey["plan-execute:plan"].Tools, []string{"custom_plan", "plan_add_tasks"})
	assertToolNames(t, byKey["plan-execute:execute"].Tools, appendUniqueTools(stageToolsOrDefault(settings.Execute, session.ToolNames), "plan_update_task"))
	assertToolNames(t, byKey["plan-execute:summary"].Tools, nil)
	if byKey["plan-execute:execute"].SystemMessage["content"] != "execute primary" {
		t.Fatalf("unexpected execute system message %#v", byKey["plan-execute:execute"].SystemMessage)
	}
	if byKey["plan-execute:summary"].SystemMessage["content"] != "summary primary" {
		t.Fatalf("unexpected summary system message %#v", byKey["plan-execute:summary"].SystemMessage)
	}
}

func TestCoderSystemInitProfileUsesDistinctMode(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "CODER"
	toolDefs := []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
	}
	profiles := BuildSystemInitProfiles(session, api.QueryRequest{ChatID: "chat-1", Message: "hello"}, toolDefs, 12, 4, 12, config.PromptsConfig{})
	if len(profiles) != 1 {
		t.Fatalf("expected one CODER profile, got %#v", profiles)
	}
	if profiles[0].CacheKey != "coder:main" || profiles[0].Mode != "coder" {
		t.Fatalf("unexpected CODER system init identity %#v", profiles[0])
	}
	if profiles[0].Fingerprint == ComputeSystemInitFingerprint(fingerprintTestSession(), "main", toolDefs) {
		t.Fatalf("expected CODER fingerprint to differ from REACT")
	}
}

func TestCoderSystemInitProfileIncludesCoderSystemPrompt(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "CODER"
	session.ModeSystemPrompt = "custom coder system prompt"
	toolDefs := []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
		{Name: "datetime", Description: "get time", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_add_tasks", Description: "add tasks", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_get_tasks", Description: "get tasks", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_update_task", Description: "update task", Parameters: map[string]any{"type": "object"}},
	}
	profiles := BuildSystemInitProfiles(session, api.QueryRequest{ChatID: "chat-1", Message: "hello"}, toolDefs, 12, 4, 12, config.PromptsConfig{})
	if len(profiles) != 1 {
		t.Fatalf("expected one CODER profile, got %#v", profiles)
	}
	content, _ := profiles[0].SystemMessage["content"].(string)
	if !strings.Contains(content, "custom coder system prompt") {
		t.Fatalf("expected coder system prompt in system init, got %q", content)
	}
	assertToolNames(t, profiles[0].Tools, []string{"bash", "datetime", "plan_add_tasks", "plan_get_tasks", "plan_update_task"})
}

func TestCoderPlanningModeBuildsPlanningAndExecuteSystemInit(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "CODER"
	session.PlanningMode = true
	session.ModeSystemPrompt = "custom coder system prompt"
	session.ResolvedCoderPlanningSettings = contracts.CoderPlanningSettings{
		MaxSteps: 12,
		Execute:  contracts.StageSettings{Tools: []string{"bash", "file_read", contracts.FinalizePlanningToolName, "ask_user_question"}},
	}
	toolDefs := []api.ToolDetailResponse{
		{Name: "bash", Description: "run shell", Parameters: map[string]any{"type": "object"}},
		{Name: "file_read", Description: "read files", Parameters: map[string]any{"type": "object"}},
		{Name: "ask_user_question", Description: "ask", Parameters: map[string]any{"type": "object"}},
		{Name: contracts.FinalizePlanningToolName, Description: "write plan", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_add_tasks", Description: "add tasks", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_get_tasks", Description: "get tasks", Parameters: map[string]any{"type": "object"}},
		{Name: "plan_update_task", Description: "update task", Parameters: map[string]any{"type": "object"}},
	}
	req := api.QueryRequest{ChatID: "chat-1", Message: "hello"}
	profiles := BuildSystemInitProfiles(session, req, toolDefs, 12, 4, 12, config.PromptsConfig{})
	if len(profiles) != 2 {
		t.Fatalf("expected CODER planning plan/execute profiles, got %#v", profiles)
	}
	byKey := map[string]contracts.SystemInitProfile{}
	for _, profile := range profiles {
		byKey[profile.CacheKey] = profile
	}
	if _, ok := byKey["coder:planning"]; !ok {
		t.Fatalf("missing coder planning profile %#v", byKey)
	}
	if _, ok := byKey["coder:execute"]; !ok {
		t.Fatalf("missing coder execute profile %#v", byKey)
	}
	if _, ok := byKey["coder:summary"]; ok {
		t.Fatalf("did not expect coder summary profile %#v", byKey)
	}
	assertToolNames(t, byKey["coder:planning"].Tools, []string{"file_read", "ask_user_question", contracts.FinalizePlanningToolName})
	executeTools := []string{"bash", "file_read", "plan_add_tasks", "plan_get_tasks", "plan_update_task"}
	assertToolNames(t, byKey["coder:execute"].Tools, executeTools)
	wantExecuteSystem := agentcoder.PlanningExecutionSystemPrompt(session, req, session.ResolvedCoderPlanningSettings, agentcoder.PlanningModeTools(), executeTools, agentcoder.DefaultExecuteSystemPrompt)
	if byKey["coder:execute"].SystemMessage["content"] != wantExecuteSystem {
		t.Fatalf("unexpected coder execute system message %#v want %q", byKey["coder:execute"].SystemMessage, wantExecuteSystem)
	}
}

func TestSystemInitCacheKeyMapsCoderPlanningStages(t *testing.T) {
	cases := []struct {
		mode  string
		stage string
		want  string
	}{
		{mode: "CODER", stage: "coder", want: "coder:main"},
		{mode: "CODER", stage: "coder-planning", want: "coder:planning"},
		{mode: "CODER", stage: "coder-planning-feedback", want: "coder:planning"},
		{mode: "CODER", stage: "coder-execute", want: "coder:execute"},
		{mode: "CODER", stage: "coder-execute-step-2", want: "coder:execute"},
		{mode: "PLAN_EXECUTE", stage: "summary", want: "plan-execute:summary"},
		{mode: "REACT", stage: "anything", want: "react:main"},
	}
	for _, tc := range cases {
		if got := SystemInitCacheKey(tc.mode, tc.stage); got != tc.want {
			t.Fatalf("SystemInitCacheKey(%q, %q)=%q want %q", tc.mode, tc.stage, got, tc.want)
		}
	}
}

func TestCoderSystemPromptChangesFingerprint(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "CODER"
	session.ModeSystemPrompt = "coder prompt one"
	toolDefs := []api.ToolDetailResponse{{Name: "bash", Description: "run shell"}}
	first := ComputeSystemInitFingerprint(session, "main", toolDefs)
	session.ModeSystemPrompt = "coder prompt two"
	second := ComputeSystemInitFingerprint(session, "main", toolDefs)
	if first == second {
		t.Fatalf("expected coder system prompt change to update fingerprint")
	}
}

func TestKBaseSystemPromptChangesFingerprint(t *testing.T) {
	session := fingerprintTestSession()
	session.Mode = "KBASE"
	session.ModeSystemPrompt = "kbase prompt one"
	toolDefs := []api.ToolDetailResponse{{Name: "kbase_search", Description: "search knowledge base"}}
	first := ComputeSystemInitFingerprint(session, "main", toolDefs)
	session.ModeSystemPrompt = "kbase prompt two"
	second := ComputeSystemInitFingerprint(session, "main", toolDefs)
	if first == second {
		t.Fatalf("expected kbase system prompt change to update fingerprint")
	}
}

func fingerprintTestSession() contracts.QuerySession {
	return contracts.QuerySession{
		RequestID:        "request-1",
		RunID:            "run-1",
		ChatID:           "chat-1",
		AgentKey:         "agent",
		AgentName:        "Agent",
		AgentRole:        "helper",
		AgentDescription: "does work",
		ModelKey:         "mock-model",
		ToolNames:        []string{"datetime", "bash"},
		Mode:             "REACT",
		SkillKeys:        []string{"skill-a"},
		ContextTags:      []string{"system", "session"},
		PromptAppend:     contracts.DefaultPromptAppendConfig(),
		SoulPrompt:       "soul",
		AgentsPrompt:     "agents",
		PlanPrompt:       "plan",
		ExecutePrompt:    "execute",
		SummaryPrompt:    "summary",
		ResolvedPlanExecuteSettings: contracts.PlanExecuteSettings{
			Plan:    contracts.StageSettings{SystemPrompt: "plan system"},
			Execute: contracts.StageSettings{SystemPrompt: "execute system"},
			Summary: contracts.StageSettings{SystemPrompt: "summary system"},
		},
		RuntimeEnvOverrides: map[string]string{"FOO": "bar"},
	}
}

func newSystemInitTestModelRegistry(t *testing.T) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	providerYAML := strings.Join([]string{
		"key: mock",
		"baseUrl: http://example.test",
		"apiKey: token",
		"endpointPath: /v1/chat/completions",
		"defaultModel: mock-model",
		"",
	}, "\n")
	modelYAML := strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(providerYAML), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "mock.yml"), []byte(modelYAML), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}

func assertToolNames(t *testing.T, raw []any, expected []string) {
	t.Helper()
	specs, err := cachedToolSpecsToOpenAI(raw)
	if err != nil {
		t.Fatalf("decode tool specs: %v", err)
	}
	var actual []string
	for _, spec := range specs {
		actual = append(actual, spec.Function.Name)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("tool names = %#v, want %#v", actual, expected)
	}
}
