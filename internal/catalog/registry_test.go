package catalog

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
			"contextTags:\n"+
			"  - execution_policy\n"+
			"  - agent_identity\n"+
			"budget:\n"+
			"  runTimeoutMs: 1000\n"+
			"stageSettings:\n"+
			"  stage: alpha\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.ContextTags) != 1 || def.ContextTags[0] != "context" || def.Budget["runTimeoutMs"] != int64(1000) && def.Budget["runTimeoutMs"] != 1000 {
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

func TestParseAgentFileNormalizesJavaContextTagsAndRuntimePrompts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: runtime_prompts\n"+
			"name: Runtime Prompts\n"+
			"mode: ONESHOT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextTags:\n"+
			"  - agent_identity\n"+
			"  - run_session\n"+
			"  - memory_context\n"+
			"runtimePrompts:\n"+
			"  planExecute:\n"+
			"    taskExecutionPromptTemplate: TASK={{task_id}}\n"+
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
	if got := strings.Join(def.ContextTags, ","); got != "context,memory" {
		t.Fatalf("expected normalized context tags, got %q", got)
	}
	if def.RuntimePrompts.Skill.CatalogHeader != "skills-header-override" {
		t.Fatalf("expected skill prompt override, got %#v", def.RuntimePrompts)
	}
	if def.RuntimePrompts.ToolAppendix.ToolDescriptionTitle != "tool-desc-title-override" {
		t.Fatalf("expected tool appendix override, got %#v", def.RuntimePrompts)
	}
	if def.StageSettings["taskExecutionPromptTemplate"] != "TASK={{task_id}}" {
		t.Fatalf("expected task execution prompt template merged into stage settings, got %#v", def.StageSettings)
	}
}

func TestParseAgentFilePrefersContextConfigTags(t *testing.T) {
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
			"    - context\n"+
			"    - owner\n"+
			"    - auth\n"+
			"contextTags:\n"+
			"  - execution_policy\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if got := strings.Join(def.ContextTags, ","); got != "system,context,owner,auth" {
		t.Fatalf("expected contextConfig tags to win, got %q", got)
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
	if got := strings.Join(def.ContextTags, ","); got != "system,memory" {
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
			"sandboxConfig:\n"+
			"  environmentId: shell\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	agents, err := loadAgents(agentsDir, marketDir, true)
	if err != nil {
		t.Fatalf("loadAgents: %v", err)
	}
	def := agents["zenmi"]
	if got := strings.Join(def.ContextTags, ","); got != "memory" {
		t.Fatalf("expected sandbox tag to be removed from loaded agent, got %q", got)
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
	if def.StageSettings["reasoningEnabled"] != true {
		t.Fatalf("expected reasoningEnabled default, got %#v", def.StageSettings)
	}
	if def.StageSettings["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected reasoningEffort default, got %#v", def.StageSettings)
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
			"    reasoningEnabled: false\n"+
			"    reasoningEffort: LOW\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	execute, _ := def.StageSettings["execute"].(map[string]any)
	if execute["reasoningEnabled"] != false {
		t.Fatalf("expected explicit execute reasoningEnabled to win, got %#v", execute)
	}
	if execute["reasoningEffort"] != "LOW" {
		t.Fatalf("expected explicit execute reasoningEffort to win, got %#v", execute)
	}
}

func TestLoadTeamsSupportsYAMLAndSkipsExampleFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "default.yaml"), []byte(
		"name: Default Team\n"+
			"defaultAgentKey: runner\n"+
			"agentKeys:\n"+
			"  - runner\n",
	), 0o644); err != nil {
		t.Fatalf("write yaml team: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "example.example.yml"), []byte(
		"name: Example Team\n"+
			"agentKeys:\n"+
			"  - runner\n",
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

func TestLoadSkillsLoadsBashHooksAndSandboxEnv(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "mock-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, ".bash-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir bash hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".sandbox-env.json"), []byte(`{"NODE_ENV":"production","DEBUG":"0"}`), 0o644); err != nil {
		t.Fatalf("write sandbox env: %v", err)
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
	if got.SandboxEnv["NODE_ENV"] != "production" || got.SandboxEnv["DEBUG"] != "0" {
		t.Fatalf("SandboxEnv = %#v", got.SandboxEnv)
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
	if err := os.WriteFile(filepath.Join(localSkillDir, ".sandbox-env.json"), []byte(`{"SOURCE":"local"}`), 0o644); err != nil {
		t.Fatalf("write local env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, "SKILL.md"), []byte("---\nname: Market Skill\ndescription: Market Description\n---\n"), 0o644); err != nil {
		t.Fatalf("write market skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketSkillDir, ".sandbox-env.json"), []byte(`{"SOURCE":"market"}`), 0o644); err != nil {
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
	if got.SandboxEnv["SOURCE"] != "local" {
		t.Fatalf("SandboxEnv = %#v", got.SandboxEnv)
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

func TestLoadSkillsRejectsInvalidSandboxEnvJSON(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "mock-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".sandbox-env.json"), []byte(`{"NODE_ENV":}`), 0o644); err != nil {
		t.Fatalf("write sandbox env: %v", err)
	}

	if _, err := loadSkills(root, 0); err == nil {
		t.Fatal("expected invalid sandbox env error")
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
