package llm

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
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
	}, 12, 4, config.PromptsConfig{})
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
	session.ResolvedStageSettings = contracts.PlanExecuteSettings{}
	session.StageSettings = map[string]any{
		"plan": map[string]any{
			"tools": []any{"custom_plan"},
		},
		"execute": map[string]any{
			"tools":              []any{"bash", "custom_exec"},
			"instructionsPrompt": "execute primary",
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

	profiles := BuildSystemInitProfiles(session, api.QueryRequest{ChatID: "chat-1", Message: "hello"}, toolDefs, 12, 4, config.PromptsConfig{})
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
	profiles := BuildSystemInitProfiles(session, api.QueryRequest{ChatID: "chat-1", Message: "hello"}, toolDefs, 12, 4, config.PromptsConfig{})
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
		ResolvedStageSettings: contracts.PlanExecuteSettings{
			Plan:    contracts.StageSettings{SystemPrompt: "plan system"},
			Execute: contracts.StageSettings{SystemPrompt: "execute system"},
			Summary: contracts.StageSettings{SystemPrompt: "summary system"},
		},
		RuntimeEnvOverrides: map[string]string{"FOO": "bar"},
	}
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
