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
	key := "builtin-dbx"
	def := catalog.AgentDefinition{RuntimeDir: filepath.Join(root, "ru-agents", "demo"), Connectors: []string{"builtin.dbx"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: key, ConnectorID: "builtin.dbx", Name: "builtin-dbx", RuntimeDir: skill}}, ConnectorMounts: []catalog.ConnectorMount{{ID: "builtin.dbx", Dir: pkg}}}
	prompt := buildSkillCatalogPrompt(def, "", contracts.DefaultPromptAppendConfig())
	alias := "@connectors/builtin.dbx/skills/builtin-dbx/SKILL.md"
	if !strings.Contains(prompt, "skillId: builtin-dbx\n") || !strings.Contains(prompt, "instructionsPath: "+alias) || strings.Contains(prompt, "@skills/"+key) || strings.Contains(prompt, "connector-11-") {
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

func TestWecomConnectorSkillUsesOriginalIDAndSharedPath(t *testing.T) {
	for _, layout := range []string{"single", "multiple"} {
		t.Run(layout, func(t *testing.T) {
			const key = "wecomcli-shared"
			fixture := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
				pkg := filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "wecom")
				skillDir := filepath.Join(pkg, "skills")
				if layout == "multiple" {
					skillDir = filepath.Join(skillDir, key)
				}
				if err := os.MkdirAll(skillDir, 0o755); err != nil {
					t.Fatal(err)
				}
				for path, content := range map[string]string{
					filepath.Join(pkg, "connector.json"): `{"id":"wecom","name":"WeCom","version":"1.0.0","type":"cli","auth_mode":"none"}`,
					filepath.Join(pkg, "cli.json"):       `{}`,
					filepath.Join(skillDir, "SKILL.md"):  "---\nname: wecomcli-shared\ndescription: WeCom prerequisite\n---\n",
				} {
					if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				// A center skill with the same name must not become a selectable
				// substitute for this Agent's mounted connector skill.
				writeTestSkill(t, cfg.Paths.SkillsCenterDir, key)
				path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - wecom\n")...)
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}})
			def, ok := fixture.server.deps.Registry.AgentDefinition("mock-agent")
			if !ok || len(def.ConnectorSkills) != 1 || def.ConnectorSkills[0].Key != key {
				t.Fatalf("connector skill ID = %#v", def.ConnectorSkills)
			}
			alias := "@connectors/wecom/skills/"
			if layout == "multiple" {
				alias += key + "/"
			}
			alias += "SKILL.md"
			prompt := buildSkillCatalogPrompt(def, "", contracts.DefaultPromptAppendConfig())
			if !strings.Contains(prompt, "skillId: "+key+"\ninstructionsPath: "+alias+"\n") || strings.Contains(prompt, "connector-5-") || strings.Contains(prompt, "@skills/"+key) {
				t.Fatal(prompt)
			}
			if _, err := os.Stat(filepath.Join(def.RuntimeDir, "skills", key)); !os.IsNotExist(err) {
				t.Fatalf("connector skill must stay in shared runtime: %v", err)
			}
			result, err := fixture.server.listSkillsForAgent("mock-agent")
			if err != nil {
				t.Fatal(err)
			}
			for _, skill := range result.Skills {
				if skill.Key == key {
					t.Fatal("mounted connector skill is selectable through its center namesake")
				}
			}
			if _, err := resolveMustUseSkills(def, fixture.server.deps.Config.Paths.SkillsCenterDir, fixture.server.deps.Registry, []string{key}); err == nil || !strings.Contains(err.Error(), "cannot be selected") {
				t.Fatalf("connector skill accepted by mustUseSkills: %v", err)
			}
		})
	}
}
