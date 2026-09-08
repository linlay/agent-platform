package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

func TestSharedConnectorSkillsPromptSettingsAndPathIsolation(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "ru-connectors", "builtin.dbx")
	if err := connector.WriteBuiltin(pkg, "dbx", "1.0.0", "darwin"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(pkg, "skills", "builtin-dbx")
	if err := os.Mkdir(filepath.Join(skill, ".bash-hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, ".runtime-env.json"), []byte(`{"SHARED_SETTING":"shared"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	key := connector.SkillKey("builtin.dbx", "builtin-dbx")
	def := catalog.AgentDefinition{RuntimeDir: filepath.Join(root, "ru-agents", "demo"), Connectors: []string{"builtin.dbx"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: key, ConnectorID: "builtin.dbx", Name: "builtin-dbx", RuntimeDir: skill}}, ConnectorMounts: []catalog.ConnectorMount{{ID: "builtin.dbx", Dir: pkg}}}
	prompt := buildSkillCatalogPrompt(def, "", contracts.DefaultPromptAppendConfig())
	alias := "@connectors/builtin.dbx/skills/builtin-dbx/SKILL.md"
	if !strings.Contains(prompt, "instructionsPath: "+alias) || strings.Contains(prompt, "@skills/"+key) {
		t.Fatal(prompt)
	}
	hooks, env, err := resolveSkillRuntimeSettings(map[string]string{"SHARED_SETTING": "agent"}, def.RuntimeDir, "", def.EffectiveSkills(), def)
	if err != nil || len(hooks) != 1 || hooks[0] != filepath.Join(skill, ".bash-hooks") || env["SHARED_SETTING"] != "shared" {
		t.Fatalf("shared skill settings: %v %#v %v", hooks, env, err)
	}
	session := contracts.QuerySession{ConnectorDirs: runtimeConnectorDirs(def), AccessLevel: contracts.AccessLevelFullAccess}
	if err := addConnectorAccessRoots(&session.RunAccessRoots, def); err != nil {
		t.Fatal(err)
	}
	policy := config.AccessPolicyConfig{}
	plan, err := accesspolicy.BuildPathPlan(policy, session, accesspolicy.ReadAccess, alias)
	if err != nil || !plan.Allowed() || plan.Path == "" {
		t.Fatalf("shared read: %#v %v", plan, err)
	}
	plan, err = accesspolicy.BuildPathPlan(policy, session, accesspolicy.WriteAccess, alias)
	if err != nil || !plan.Blocked() {
		t.Fatalf("shared write: %#v %v", plan, err)
	}
	for _, path := range []string{"@connectors", "@connectors/unmounted/skills/usage/SKILL.md", "@connectors/builtin.dbx/../other/SKILL.md"} {
		if _, err := accesspolicy.ResolveSessionPath(session, path); err == nil {
			t.Fatalf("unmounted or escaping alias accepted: %s", path)
		}
	}
	session.AgentHasRuntimeSandbox = true
	resolved, err := accesspolicy.ResolveSessionPath(session, "/connectors/builtin.dbx/skills/builtin-dbx/SKILL.md")
	if err != nil || resolved != filepath.Join(skill, "SKILL.md") {
		t.Fatalf("container path: %s %v", resolved, err)
	}
	if err := os.Symlink(root, filepath.Join(skill, "escape")); err == nil {
		if _, err := accesspolicy.ResolveSessionPath(session, "@connectors/builtin.dbx/skills/builtin-dbx/escape/outside"); err == nil {
			t.Fatal("connector symlink escape accepted")
		}
	}
}
