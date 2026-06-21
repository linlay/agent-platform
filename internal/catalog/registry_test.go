package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestShouldLoadRuntimeNameMatchesJavaSemantics(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "demoModePlain", want: true},
		{name: "agent.demo", want: true},
		{name: "agent.demo.yml", want: true},
		{name: "agent.example", want: false},
		{name: "agent.example.yml", want: false},
		{name: "agent.EXAMPLE.yaml", want: false},
		{name: "skill.example", want: false},
		{name: ".hidden.example", want: false},
		{name: "sample.demo.yaml", want: true},
	}
	for _, tc := range cases {
		if got := ShouldLoadRuntimeName(tc.name); got != tc.want {
			t.Fatalf("ShouldLoadRuntimeName(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldIgnoreRuntimeWatchPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: ".DS_Store", want: true},
		{path: "/tmp/runtime/.DS_Store", want: true},
		{path: "agent.yml", want: false},
		{path: "SKILL.md", want: false},
		{path: "/tmp/runtime/demo.yaml", want: false},
	}
	for _, tc := range cases {
		if got := ShouldIgnoreRuntimeWatchPath(tc.path); got != tc.want {
			t.Fatalf("ShouldIgnoreRuntimeWatchPath(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}

func TestShouldWatchRuntimeDir(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Normal agent directories — should be watched
		{name: "cutej", want: true},
		{name: "dailyOfficeProAssistant", want: true},
		{name: "myAgent", want: true},

		// Staging directories — should NOT be watched
		{name: "cutej.bootstrap", want: false},
		{name: "dailyOfficeProAssistant.bootstrap", want: false},
		{name: "Agent.Bootstrap", want: false},

		// Backup directories — should NOT be watched
		{name: "cutej.bak.20260612-142448", want: false},
		{name: "bootstrap.deleted.bak.20260612", want: false},

		// Post-init leftovers — should NOT be watched
		{name: "bootstrap.deleted", want: false},
		{name: "something.deleted", want: false},

		// Example directories — already excluded by ShouldLoadRuntimeName
		{name: "agent.example", want: false},

		// Empty name
		{name: "", want: false},
	}
	for _, tc := range cases {
		if got := ShouldWatchRuntimeDir(tc.name); got != tc.want {
			t.Fatalf("ShouldWatchRuntimeDir(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLogicalRuntimeBaseNameStripsDemoAndExampleMarkers(t *testing.T) {
	cases := map[string]string{
		"auth.yml":            "auth",
		"auth.demo.yml":       "auth",
		"auth.example.yml":    "auth",
		"owner.example":       "owner",
		"viewport.demo.yaml":  "viewport",
		"provider.production": "provider",
		"plain":               "plain",
	}
	for input, want := range cases {
		if got := LogicalRuntimeBaseName(input); got != want {
			t.Fatalf("LogicalRuntimeBaseName(%q)=%q want %q", input, got, want)
		}
	}
}

func TestParseAgentFileReadsContextTagsBudgetStageSettingsAndControls(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"controls:\n"+
			"  - key: tone\n"+
			"    type: select\n"+
			"    label: 语气\n"+
			"    options:\n"+
			"      - value: concise\n"+
			"        label: 简洁\n"+
			"contextConfig:\n"+
			"  tags:\n"+
			"    - session\n"+
			"budget:\n"+
			"  timeout: 1000\n"+
			"stageSettings:\n"+
			"  stage: alpha\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.ContextTags) != 1 || def.ContextTags[0] != "session" || def.Budget["timeout"] != int64(1000) && def.Budget["timeout"] != 1000 {
		t.Fatalf("expected parsed context tags and budget, got %#v", def)
	}
	if def.StageSettings["stage"] != "alpha" {
		t.Fatalf("expected stage settings, got %#v", def.StageSettings)
	}
	if len(def.Controls) != 1 || def.Controls[0]["key"] != "tone" {
		t.Fatalf("expected parsed controls, got %#v", def.Controls)
	}
}

func TestParseAgentFilePreservesMultilineWonders(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"wonders:\n"+
			"  - 单行推荐问题\n"+
			"  - |-\n"+
			"    帮我演示 Bash HITL 审批确认\n"+
			"    并展示下一步会出现什么\n"+
			"  - \"\"\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}

	want := []string{
		"单行推荐问题",
		"帮我演示 Bash HITL 审批确认\n并展示下一步会出现什么",
	}
	if got := def.Wonders; !reflect.DeepEqual(got, want) {
		t.Fatalf("wonders = %#v, want %#v", got, want)
	}
}

func TestParseAgentFilePreservesMultilineGreetings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"greetings:\n"+
			"  - 我可以帮你练习词汇、复盘错题，也能把今天的学习拆成小步骤。\n"+
			"  - |-\n"+
			"    我会先帮你定位最容易提分的地方\n"+
			"    再给出可以马上开始的练习。\n"+
			"  - \"\"\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}

	want := []string{
		"我可以帮你练习词汇、复盘错题，也能把今天的学习拆成小步骤。",
		"我会先帮你定位最容易提分的地方\n再给出可以马上开始的练习。",
	}
	if got := def.Greetings; !reflect.DeepEqual(got, want) {
		t.Fatalf("greetings = %#v, want %#v", got, want)
	}
}

func TestParseAgentFileReadsSingularGreeting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"greeting: 我可以帮你快速看懂这个项目，并给出下一步行动建议。\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}

	want := []string{"我可以帮你快速看懂这个项目，并给出下一步行动建议。"}
	if got := def.Greetings; !reflect.DeepEqual(got, want) {
		t.Fatalf("greeting = %#v, want %#v", got, want)
	}
}

func TestParseAgentFileReadsRuntimePromptsAndContextConfigTags(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: runtime_prompts\n"+
			"name: Runtime Prompts\n"+
			"mode: ONESHOT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"runtimePrompts:\n"+
			"  skill:\n"+
			"    catalogHeader: skills-header-override\n"+
			"  toolAppendix:\n"+
			"    toolDescriptionTitle: tool-desc-title-override\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := strings.Join(def.ContextTags, ","); got != "" {
		t.Fatalf("expected empty context tags, got %q", got)
	}
	if def.RuntimePrompts.Skill.CatalogHeader != "skills-header-override" {
		t.Fatalf("expected skill prompt override, got %#v", def.RuntimePrompts)
	}
	if def.RuntimePrompts.ToolAppendix.ToolDescriptionTitle != "tool-desc-title-override" {
		t.Fatalf("expected tool appendix override, got %#v", def.RuntimePrompts)
	}
}

func TestParseAgentFileReadsOnlyContextConfigTags(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: zenmi\n"+
			"name: 小宅\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextConfig:\n"+
			"  tags:\n"+
			"    - system\n"+
			"    - session\n"+
			"    - owner\n"+
			"contextTags:\n"+
			"  - execution_policy\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := strings.Join(def.ContextTags, ","); got != "system,session,owner" {
		t.Fatalf("expected contextConfig tags only, got %q", got)
	}
}

func TestNormalizeContextTagOnlyAcceptsModernTags(t *testing.T) {
	cases := map[string]string{
		"system":           "system",
		"session":          "session",
		"owner":            "owner",
		"all-agents":       "all-agents",
		" SYSTEM ":         "system",
		"context":          "",
		"auth":             "",
		"agent_identity":   "",
		"run_session":      "",
		"scene":            "",
		"references":       "",
		"execution_policy": "",
		"skills":           "",
		"memory":           "",
		"memory_context":   "",
		"sandbox":          "",
	}
	for input, want := range cases {
		if got := normalizeContextTag(input); got != want {
			t.Fatalf("normalizeContextTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeContextTagsDeduplicatesModernTags(t *testing.T) {
	got := normalizeContextTags([]string{"system", "SYSTEM", "session", "context"})
	if !reflect.DeepEqual(got, []string{"system", "session"}) {
		t.Fatalf("normalizeContextTags() = %#v, want %#v", got, []string{"system", "session"})
	}
}

func TestParseAgentFileDropsSandboxContextTag(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: zenmi\n"+
			"name: 小宅\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextConfig:\n"+
			"  tags:\n"+
			"    - system\n"+
			"    - sandbox\n"+
			"    - memory\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := strings.Join(def.ContextTags, ","); got != "system" {
		t.Fatalf("expected sandbox tag to be dropped, got %q", got)
	}
}

func TestLoadAgentsDoesNotExposeSandboxInContextTagsMeta(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	marketDir := filepath.Join(root, "skills-market")
	agentDir := filepath.Join(agentsDir, "zenmi")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.MkdirAll(marketDir, 0o755); err != nil {
		t.Fatalf("mkdir skills market dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(
		"key: zenmi\n"+
			"name: 小宅\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextTags:\n"+
			"  - sandbox\n"+
			"  - memory\n"+
			"runtimeConfig:\n"+
			"  environmentId: shell\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	agents, err := loadAgents(agentsDir, marketDir, true)
	if err != nil {
		t.Fatalf("loadAgents: %v", err)
	}
	def := agents["zenmi"]
	if got := strings.Join(def.ContextTags, ","); got != "" {
		t.Fatalf("expected sandbox tag to be removed from loaded agent, got %q", got)
	}
}

func TestRuntimeSandboxSummaryMetaOmitsEnv(t *testing.T) {
	meta := runtimeSandboxSummaryMeta(map[string]any{
		"environmentId": "shell",
		"level":         "run",
		"env":           map[string]string{"HTTP_PROXY": "secret"},
	})
	if meta["environmentId"] != "shell" || meta["level"] != "RUN" {
		t.Fatalf("unexpected sandbox summary meta: %#v", meta)
	}
	if _, ok := meta["env"]; ok {
		t.Fatalf("expected runtime env to stay private, got %#v", meta)
	}
}

func TestParseAgentFileMapsModelReasoningIntoStageSettings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: reasoned\n"+
			"name: Reasoned\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"  reasoning:\n"+
			"    enabled: true\n"+
			"    effort: HIGH\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	execute := mapNode(def.StageSettings["execute"])
	reasoning := mapNode(mapNode(execute["modelConfig"])["reasoning"])
	if reasoning["enabled"] != true {
		t.Fatalf("expected reasoning enabled default, got %#v", def.StageSettings)
	}
	if reasoning["effort"] != "HIGH" {
		t.Fatalf("expected reasoning effort default, got %#v", def.StageSettings)
	}
}

func TestParseAgentFilePreservesExplicitStageReasoningOverrides(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: reasoned\n"+
			"name: Reasoned\n"+
			"mode: PLAN_EXECUTE\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"  reasoning:\n"+
			"    enabled: true\n"+
			"    effort: HIGH\n"+
			"stageSettings:\n"+
			"  execute:\n"+
			"    modelConfig:\n"+
			"      reasoning:\n"+
			"        enabled: false\n"+
			"        effort: LOW\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	execute, _ := def.StageSettings["execute"].(map[string]any)
	reasoning := mapNode(mapNode(execute["modelConfig"])["reasoning"])
	if reasoning["enabled"] != false {
		t.Fatalf("expected explicit execute reasoningEnabled to win, got %#v", execute)
	}
	if reasoning["effort"] != "LOW" {
		t.Fatalf("expected explicit execute reasoningEffort to win, got %#v", execute)
	}
}

func TestParseAgentFileMapsModelSamplingIntoStageSettings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: sampled\n"+
			"name: Sampled\n"+
			"mode: PLAN_EXECUTE\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"  sampling:\n"+
			"    temperature: 0.7\n"+
			"    topP: 0.9\n"+
			"    presencePenalty: 0\n"+
			"stageSettings:\n"+
			"  plan:\n"+
			"    modelConfig:\n"+
			"      sampling:\n"+
			"        temperature: 0.2\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	plan, _ := def.StageSettings["plan"].(map[string]any)
	planSampling := contracts.ParseSamplingSettings(mapNode(mapNode(plan["modelConfig"])["sampling"]))
	if planSampling.Temperature == nil || *planSampling.Temperature != 0.2 {
		t.Fatalf("expected plan temperature override, got %#v", planSampling)
	}
	if planSampling.TopP == nil || *planSampling.TopP != 0.9 {
		t.Fatalf("expected plan topP inherited from modelConfig, got %#v", planSampling)
	}
	if planSampling.PresencePenalty == nil || *planSampling.PresencePenalty != 0 {
		t.Fatalf("expected plan explicit zero presence penalty inherited from modelConfig, got %#v", planSampling)
	}
}

func TestParseAgentFileRejectsInvalidSamplingType(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: bad-sampling\n"+
			"name: Bad Sampling\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"  sampling:\n"+
			"    temperature: creative\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "modelConfig.sampling.temperature must be a number") {
		t.Fatalf("expected invalid sampling type error, got %v", err)
	}
}

func TestParseAgentFileRejectsInvalidNestedStageSamplingType(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: bad-stage-sampling\n"+
			"name: Bad Stage Sampling\n"+
			"mode: PLAN_EXECUTE\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"stageSettings:\n"+
			"  plan:\n"+
			"    modelConfig:\n"+
			"      sampling:\n"+
			"        temperature: warm\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	_, err := parseAgentFile(path)
	if err == nil || !strings.Contains(err.Error(), "stageSettings.plan.modelConfig.sampling.temperature must be a number") {
		t.Fatalf("expected invalid nested stage sampling type error, got %v", err)
	}
}

func TestLoadTeamsSupportsYAMLAndSkipsExampleFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "default.yaml"), []byte(
		"name: Default Team\n"+
			"defaultAgentKey: default_agent\n"+
			"agentKeys:\n"+
			"  - default_agent\n",
	), 0o644); err != nil {
		t.Fatalf("write yaml team: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "example.example.yml"), []byte(
		"name: Example Team\n"+
			"agentKeys:\n"+
			"  - default_agent\n",
	), 0o644); err != nil {
		t.Fatalf("write example team: %v", err)
	}

	teams, err := loadTeams(root)
	if err != nil {
		t.Fatalf("load teams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected one loadable team, got %#v", teams)
	}
	if _, ok := teams["default"]; !ok {
		t.Fatalf("expected default team to load, got %#v", teams)
	}
	if _, ok := teams["example.example"]; ok {
		t.Fatalf("did not expect example team to load, got %#v", teams)
	}
}

func TestLoadSkillsSkipsExampleDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mock-skill"), 0o755); err != nil {
		t.Fatalf("mkdir mock skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mock-skill", "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write mock skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sample.example"), 0o755); err != nil {
		t.Fatalf("mkdir example skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.example", "SKILL.md"), []byte("# Example Skill\n\nShould be ignored"), 0o644); err != nil {
		t.Fatalf("write example skill: %v", err)
	}

	skills, err := loadSkills(root, 0)
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected one loadable skill, got %#v", skills)
	}
	if _, ok := skills["mock-skill"]; !ok {
		t.Fatalf("expected mock-skill to load, got %#v", skills)
	}
	if _, ok := skills["sample.example"]; ok {
		t.Fatalf("did not expect example skill to load, got %#v", skills)
	}
}

func TestLoadSkillsLoadsBashHooksAndRuntimeEnv(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "mock-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir bash hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(`{"NODE_ENV":"production","DEBUG":"0"}`), 0o644); err != nil {
		t.Fatalf("write runtime env: %v", err)
	}

	skills, err := loadSkills(root, 0)
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	got, ok := skills["mock-skill"]
	if !ok {
		t.Fatalf("expected mock-skill to load, got %#v", skills)
	}
	wantHooksDir, err := filepath.Abs(filepath.Join(skillDir, ".bash-hooks"))
	if err != nil {
		t.Fatalf("abs bash hooks dir: %v", err)
	}
	if got.BashHooksDir != wantHooksDir {
		t.Fatalf("BashHooksDir = %q, want %q", got.BashHooksDir, wantHooksDir)
	}
	if got.RuntimeEnv["NODE_ENV"] != "production" || got.RuntimeEnv["DEBUG"] != "0" {
		t.Fatalf("RuntimeEnv = %#v", got.RuntimeEnv)
	}
}

func TestLoadSkillsParsesFullFrontMatterMetadata(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "mock-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := strings.Join([]string{
		"---",
		`name: "Front Matter Name"`,
		`license: MIT`,
		"metadata:",
		`  version: "1.0.0"`,
		"  category: document-processing",
		"  author: MiniMaxAI",
		"  sources:",
		`    - "Spec A"`,
		`    - "Spec B"`,
		"description: >",
		"  Front matter description line 1.",
		"  Line 2 should fold into the same paragraph.",
		"",
		"  Line 4 should become a new paragraph.",
		"triggers:",
		"  - 报告",
		"  - docx",
		"---",
		"",
		"# Heading Should Not Leak",
		"",
		"Body line",
		"",
		"---",
		"",
		"Another section",
	}, "\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	skills, err := loadSkills(root, 0)
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	got := skills["mock-skill"]
	if got.Name != "Front Matter Name" {
		t.Fatalf("Name = %q", got.Name)
	}
	wantDescription := "Front matter description line 1. Line 2 should fold into the same paragraph.\n\nLine 4 should become a new paragraph."
	if got.Description != wantDescription {
		t.Fatalf("Description = %q", got.Description)
	}
	if strings.Contains(got.Name, "name:") || strings.Contains(got.Description, "description:") {
		t.Fatalf("unexpected front matter leakage: %#v", got)
	}
	if !reflect.DeepEqual(got.Triggers, []string{"报告", "docx"}) {
		t.Fatalf("Triggers = %#v", got.Triggers)
	}
	wantMetadata := map[string]any{
		"version":  "1.0.0",
		"category": "document-processing",
		"author":   "MiniMaxAI",
		"sources":  []any{"Spec A", "Spec B"},
	}
	if !reflect.DeepEqual(got.Metadata, wantMetadata) {
		t.Fatalf("Metadata = %#v", got.Metadata)
	}
	if !strings.Contains(got.Prompt, "\n---\n\nAnother section") {
		t.Fatalf("expected body separators to remain in prompt, got %q", got.Prompt)
	}
}

func TestResolveSkillDefinitionPrefersAgentLocalSkillBeforeMarket(t *testing.T) {
	agentDir := t.TempDir()
	marketDir := t.TempDir()
	localSkillDir := filepath.Join(agentDir, "skills", "mock-skill")
	marketSkillDir := filepath.Join(marketDir, "mock-skill")
	if err := os.MkdirAll(filepath.Join(localSkillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir local hooks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(marketSkillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir market hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localSkillDir, "SKILL.md"), []byte("---\nname: Local Skill\ndescription: Local Description\n---\n"), 0o644); err != nil {
		t.Fatalf("write local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localSkillDir, ".runtime-env.json"), []byte(`{"SOURCE":"local"}`), 0o644); err != nil {
		t.Fatalf("write local env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, "SKILL.md"), []byte("---\nname: Market Skill\ndescription: Market Description\n---\n"), 0o644); err != nil {
		t.Fatalf("write market skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, ".runtime-env.json"), []byte(`{"SOURCE":"market"}`), 0o644); err != nil {
		t.Fatalf("write market env: %v", err)
	}

	got, ok, err := ResolveSkillDefinition(agentDir, marketDir, "mock-skill")
	if err != nil {
		t.Fatalf("ResolveSkillDefinition() error = %v", err)
	}
	if !ok {
		t.Fatal("expected skill definition to resolve")
	}
	if got.Name != "Local Skill" || got.Description != "Local Description" {
		t.Fatalf("resolved local metadata = %#v", got)
	}
	if got.RuntimeEnv["SOURCE"] != "local" {
		t.Fatalf("RuntimeEnv = %#v", got.RuntimeEnv)
	}
	if got.BashHooksDir != filepath.Join(localSkillDir, ".bash-hooks") {
		t.Fatalf("BashHooksDir = %q", got.BashHooksDir)
	}
}

func TestResolveSkillDefinitionFallsBackToMarketSkill(t *testing.T) {
	marketDir := t.TempDir()
	marketSkillDir := filepath.Join(marketDir, "mock-skill")
	if err := os.MkdirAll(marketSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir market skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, "SKILL.md"), []byte("# Market Skill\n\nMarket Description"), 0o644); err != nil {
		t.Fatalf("write market skill: %v", err)
	}

	got, ok, err := ResolveSkillDefinition(t.TempDir(), marketDir, "mock-skill")
	if err != nil {
		t.Fatalf("ResolveSkillDefinition() error = %v", err)
	}
	if !ok {
		t.Fatal("expected market fallback to resolve")
	}
	if got.Name != "Market Skill" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Description != "Market Skill" {
		t.Fatalf("Description = %q", got.Description)
	}
}

func TestSkillsSummaryIncludesSafeMetadataAndTagMatchesTriggers(t *testing.T) {
	registry := &FileRegistry{
		skills: map[string]SkillDefinition{
			"minimax-docx": {
				Key:             "minimax-docx",
				Name:            "minimax-docx",
				Description:     "DOCX processor",
				Triggers:        []string{"报告", "docx"},
				Metadata:        map[string]any{"version": "1.0.0", "category": "document-processing", "author": "MiniMaxAI", "sources": []any{"Spec A"}},
				PromptTruncated: true,
			},
		},
	}

	items := registry.Skills("报告")
	if len(items) != 1 {
		t.Fatalf("expected trigger match, got %#v", items)
	}
	meta := items[0].Meta
	if meta["promptTruncated"] != true {
		t.Fatalf("promptTruncated = %#v", meta["promptTruncated"])
	}
	if !reflect.DeepEqual(meta["triggers"], []string{"报告", "docx"}) {
		t.Fatalf("triggers = %#v", meta["triggers"])
	}
	wantMetadata := map[string]any{
		"version":  "1.0.0",
		"category": "document-processing",
		"author":   "MiniMaxAI",
	}
	if !reflect.DeepEqual(meta["metadata"], wantMetadata) {
		t.Fatalf("metadata = %#v", meta["metadata"])
	}
}

func TestLoadSkillsRejectsInvalidRuntimeEnvJSON(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "mock-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(`{"NODE_ENV":}`), 0o644); err != nil {
		t.Fatalf("write runtime env: %v", err)
	}

	if _, err := loadSkills(root, 0); err == nil {
		t.Fatal("expected invalid runtime env error")
	}
}

func TestTeamsLogsInvalidAgentKeys(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	registry := &FileRegistry{
		agents: map[string]AgentDefinition{
			"agent_a": {Key: "agent_a"},
		},
		teams: map[string]TeamDefinition{
			"team_a": {
				TeamID:          "team_a",
				Name:            "Team A",
				AgentKeys:       []string{"agent_a", "missing_agent"},
				DefaultAgentKey: "agent_a",
			},
		},
	}

	items := registry.Teams()
	if len(items) != 1 {
		t.Fatalf("expected one team summary, got %#v", items)
	}
	if !strings.Contains(buf.String(), "invalidAgentKeys=[missing_agent]") {
		t.Fatalf("expected invalid agent key warning, got %q", buf.String())
	}
}

func TestAgentsSummaryIncludesCatalogFieldsAndFiltersScope(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project")
	registry := &FileRegistry{
		agents: map[string]AgentDefinition{
			"assistant": {
				Key:              "assistant",
				Name:             "Assistant",
				Icon:             map[string]any{"name": "bot", "color": "#336699"},
				Description:      "hidden from json",
				Role:             "Assistant role",
				Mode:             "REACT",
				ModelKey:         "agent-model",
				Tools:            []string{"bash", "file_read"},
				Skills:           []string{"browser"},
				Workspace:        AgentWorkspaceConfig{Root: workspace},
				VisibilityScopes: []string{"nav", "copilot"},
			},
			"worker": {
				Key:       "worker",
				Name:      "Worker",
				Role:      "Code worker",
				Greetings: []string{"I can inspect your codebase and plan the next change.", "I can run tests, explain failures, and patch the fix."},
				Wonders:   []string{"Review my workspace changes", "Run tests and fix failures"},
				Mode:      AgentModeCoder,
				ModelKey:  "agent-model",
				StageSettings: map[string]any{
					"plan": map[string]any{
						"modelConfig": map[string]any{
							"modelKey": "plan-model",
							"reasoning": map[string]any{
								"effort": "LOW",
							},
						},
					},
					"execute": map[string]any{
						"modelConfig": map[string]any{
							"modelKey": "execute-model",
							"reasoning": map[string]any{
								"effort": "HIGH",
							},
						},
					},
					"summary": map[string]any{
						"modelConfig": map[string]any{
							"modelKey": "summary-model",
							"reasoning": map[string]any{
								"effort": "MEDIUM",
							},
						},
					},
				},
				VisibilityScopes: []string{"internal"},
			},
			"invoker": {
				Key:              "invoker",
				Name:             "Invoker",
				Mode:             "PROXY",
				VisibilityScopes: []string{"invoke"},
			},
		},
	}

	items := registry.Agents("")
	if len(items) != 3 {
		t.Fatalf("default agents = %#v", items)
	}
	navItems := registry.Agents("nav")
	if len(navItems) != 1 || navItems[0].Key != "assistant" {
		t.Fatalf("nav agents = %#v", navItems)
	}
	if navItems[0].Mode != "REACT" || navItems[0].WorkspaceDir != workspace {
		t.Fatalf("summary mode/workspace = %#v", navItems[0])
	}
	if navItems[0].Meta["modelKey"] != "agent-model" || navItems[0].Meta["toolsCount"] != 2 || navItems[0].Meta["skillsCount"] != 1 {
		t.Fatalf("summary meta should include modelKey and list counts, got %#v", navItems[0].Meta)
	}
	visibility, ok := navItems[0].Meta["visibility"].(map[string]any)
	if !ok {
		t.Fatalf("summary meta should include visibility, got %#v", navItems[0].Meta)
	}
	if !reflect.DeepEqual(visibility["scopes"], []string{"nav", "copilot"}) {
		t.Fatalf("summary visibility scopes = %#v", visibility["scopes"])
	}
	data, err := json.Marshal(navItems[0])
	if err != nil {
		t.Fatalf("marshal agent summary: %v", err)
	}
	if strings.Contains(string(data), "description") || strings.Contains(string(data), "kanban") {
		t.Fatalf("summary json should omit backend fields, got %s", data)
	}
	if !strings.Contains(string(data), `"visibility":{"scopes":["nav","copilot"]}`) {
		t.Fatalf("summary json should include visibility meta, got %s", data)
	}
	if !strings.Contains(string(data), `"role":"Assistant role"`) {
		t.Fatalf("summary json should include role, got %s", data)
	}
	if strings.Contains(string(data), "defaultModelKey") || strings.Contains(string(data), "defaultReasoningEffort") {
		t.Fatalf("non-CODER summary should omit CODER defaults, got %s", data)
	}
	if _, err := NormalizeAgentSummaryScope("missing"); !errors.Is(err, ErrInvalidAgentSummaryScope) {
		t.Fatalf("invalid scope error = %v, want ErrInvalidAgentSummaryScope", err)
	}

	invokeItems := registry.Agents("invoke")
	if len(invokeItems) != 1 || invokeItems[0].Key != "invoker" {
		t.Fatalf("invoke agents = %#v", invokeItems)
	}
	allItems := registry.Agents("all")
	if len(allItems) != 3 {
		t.Fatalf("all agents = %#v", allItems)
	}
	var worker api.AgentSummary
	for _, item := range allItems {
		if item.Key == "worker" {
			worker = item
		}
	}
	if worker.DefaultModelKey != "execute-model" || worker.DefaultReasoningEffort != "HIGH" {
		t.Fatalf("CODER defaults should prefer execute settings, got %#v", worker)
	}
	if worker.Role != "Code worker" {
		t.Fatalf("CODER summary role = %q, want Code worker", worker.Role)
	}
	workerData, err := json.Marshal(worker)
	if err != nil {
		t.Fatalf("marshal CODER agent summary: %v", err)
	}
	if !strings.Contains(string(workerData), `"defaultModelKey":"execute-model"`) ||
		!strings.Contains(string(workerData), `"defaultReasoningEffort":"HIGH"`) ||
		!strings.Contains(string(workerData), `"role":"Code worker"`) {
		t.Fatalf("CODER summary JSON should include root defaults, got %s", workerData)
	}
	if strings.Contains(string(workerData), `"greetings"`) {
		t.Fatalf("CODER summary JSON should omit greetings, got %s", workerData)
	}
	if strings.Contains(string(workerData), `"wonders"`) {
		t.Fatalf("CODER summary JSON should omit wonders, got %s", workerData)
	}
}

func TestAgentSummaryCoderDefaultsFallbackToModelConfigAndMedium(t *testing.T) {
	modelKey, reasoningEffort := agentSummaryCoderDefaults(AgentDefinition{
		Key:      "coder",
		Mode:     AgentModeCoder,
		ModelKey: "agent-model",
	})
	if modelKey != "agent-model" || reasoningEffort != "MEDIUM" {
		t.Fatalf("unexpected CODER fallback defaults model=%q reasoning=%q", modelKey, reasoningEffort)
	}
}
