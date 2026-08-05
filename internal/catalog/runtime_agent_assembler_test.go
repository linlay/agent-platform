package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestRuntimeAgentAssemblerClearsRuntimeRootAtStartup(t *testing.T) {
	root := t.TempDir()
	ruAgentsDir := filepath.Join(root, "ru-agents")
	writeRuntimeAssemblerFile(t, filepath.Join(ruAgentsDir, "stale-agent", "agent.yml"), "key: stale-agent\n")
	writeRuntimeAssemblerFile(t, filepath.Join(ruAgentsDir, ".staging", "abandoned", "partial"), "stale")
	writeRuntimeAssemblerFile(t, filepath.Join(ruAgentsDir, "unexpected.txt"), "stale")

	if _, err := newRuntimeAgentAssembler(ruAgentsDir, filepath.Join(root, "skills-center")); err != nil {
		t.Fatalf("new assembler: %v", err)
	}
	entries, err := os.ReadDir(ruAgentsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".staging" || !entries[0].IsDir() {
		t.Fatalf("startup must reset ru-agents before rebuilding: %#v", entries)
	}
	if mode := mustRuntimeAssemblerMode(t, ruAgentsDir); mode.Perm() != 0o700 {
		t.Fatalf("ru-agents permissions = %o", mode.Perm())
	}
}

func TestRuntimeAgentAssemblerRejectsNonDirectoryRootWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	ruAgentsPath := filepath.Join(root, "ru-agents")
	writeRuntimeAssemblerFile(t, ruAgentsPath, "preserve")

	if _, err := newRuntimeAgentAssembler(ruAgentsPath, filepath.Join(root, "skills-center")); err == nil ||
		!strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("expected non-directory root rejection, got %v", err)
	}
	assertRuntimeAssemblerContent(t, ruAgentsPath, "preserve")
}

func TestRuntimeAgentAssemblerUsesLocalSkillAndMergesConfig(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	centerDir := filepath.Join(root, "skills-center")
	ruAgentsDir := filepath.Join(root, "ru-agents")
	writeRuntimeAssemblerAgent(t, agentsDir, "writer", []string{"office"})
	writeRuntimeAssemblerSkill(t, filepath.Join(centerDir, "office"), "Center Office")
	writeRuntimeAssemblerSkill(t, filepath.Join(agentsDir, "writer", "skills", "office"), "Local Office")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", ".runtime-env.json"), `{"OFFICE_SOURCE":"local"}`)
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", "scripts", "run.sh"), "#!/bin/sh\n")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", "references", "guide.md"), "guide")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", "assets", "icon.txt"), "asset")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", ".config", "httpx", "bridge.toml"), "skill-default")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", ".config", "httpx", "other.toml"), "skill-other")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", ".config", "httpx", "tree", "old.toml"), "stale")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "skills", "office", ".config", "service"), "skill-file")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", ".config", "httpx", "bridge.toml"), "agent-override")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", ".config", "httpx", "tree"), "agent-file")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", ".config", "service", "current.toml"), "agent-directory")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "SOUL.md"), "runtime soul")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "AGENTS.md"), "runtime agents")
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "writer", "templates", "prompt.md"), "template")

	assembler, err := newRuntimeAgentAssembler(ruAgentsDir, centerDir)
	if err != nil {
		t.Fatalf("new assembler: %v", err)
	}
	agents, admin, err := loadAgentsWithAdminAssembler(
		agentsDir,
		centerDir,
		filepath.Join(root, "chats"),
		true,
		assembler,
	)
	if err != nil {
		t.Fatalf("load agents: %v", err)
	}
	def, ok := agents["writer"]
	if !ok {
		t.Fatalf("assembled agent missing; admin=%#v", admin["writer"])
	}
	if def.AgentDir != filepath.Join(agentsDir, "writer") {
		t.Fatalf("source AgentDir = %q", def.AgentDir)
	}
	if def.RuntimeDir != filepath.Join(ruAgentsDir, "writer") {
		t.Fatalf("RuntimeDir = %q", def.RuntimeDir)
	}
	if def.SoulPrompt != "runtime soul" || def.AgentsPrompt != "runtime agents" {
		t.Fatalf("runtime prompts = soul:%q agents:%q", def.SoulPrompt, def.AgentsPrompt)
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, "skills", "office", "SKILL.md"), "# Local Office\n\nInstructions")
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, ".config", "httpx", "bridge.toml"), "agent-override")
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, ".config", "httpx", "other.toml"), "skill-other")
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, ".config", "httpx", "tree"), "agent-file")
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, ".config", "service", "current.toml"), "agent-directory")
	for _, relative := range []string{
		"skills/office/scripts/run.sh",
		"skills/office/references/guide.md",
		"skills/office/assets/icon.txt",
		"templates/prompt.md",
	} {
		if _, err := os.Stat(filepath.Join(def.RuntimeDir, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("runtime resource %s missing: %v", relative, err)
		}
	}
	skill, found, err := ResolveRuntimeSkillDefinition(def.RuntimeDir, "office")
	if err != nil || !found {
		t.Fatalf("resolve assembled skill: found=%v err=%v", found, err)
	}
	if skill.RuntimeEnv["OFFICE_SOURCE"] != "local" {
		t.Fatalf("runtime env = %#v", skill.RuntimeEnv)
	}
	if admin["writer"].Source.AgentDir != def.AgentDir {
		t.Fatalf("admin source changed to runtime directory: %#v", admin["writer"].Source)
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), def.RuntimeDir) || strings.Contains(string(encoded), "RuntimeDir") {
		t.Fatalf("RuntimeDir leaked through JSON: %s", encoded)
	}
	if mode := mustRuntimeAssemblerMode(t, ruAgentsDir); mode.Perm() != 0o700 {
		t.Fatalf("ru-agents permissions = %o", mode.Perm())
	}
	if mode := mustRuntimeAssemblerMode(t, filepath.Join(def.RuntimeDir, ".config", "httpx", "bridge.toml")); mode.Perm()&0o077 != 0 {
		t.Fatalf("runtime config is not private: %o", mode.Perm())
	}
	if err := os.RemoveAll(def.AgentDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(centerDir); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ResolveRuntimeSkillDefinition(def.RuntimeDir, "office"); err != nil || !found {
		t.Fatalf("assembled runtime unexpectedly depends on source directories: found=%v err=%v", found, err)
	}
}

func TestRuntimeAgentAssemblerRejectsSkillConfigConflictsButAllowsIdenticalFiles(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	centerDir := filepath.Join(root, "skills-center")
	ruAgentsDir := filepath.Join(root, "ru-agents")
	writeRuntimeAssemblerAgent(t, agentsDir, "conflict", []string{"alpha", "beta"})
	writeRuntimeAssemblerAgent(t, agentsDir, "identical", []string{"same-a", "same-b"})
	writeRuntimeAssemblerAgent(t, agentsDir, "healthy", nil)
	for _, id := range []string{"alpha", "beta", "same-a", "same-b"} {
		writeRuntimeAssemblerSkill(t, filepath.Join(centerDir, id), id)
	}
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "alpha", ".config", "httpx", "bridge.toml"), "alpha")
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "beta", ".config", "httpx", "bridge.toml"), "beta")
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "same-a", ".config", "httpx", "bridge.toml"), "same")
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "same-b", ".config", "httpx", "bridge.toml"), "same")

	assembler, err := newRuntimeAgentAssembler(ruAgentsDir, centerDir)
	if err != nil {
		t.Fatal(err)
	}
	agents, admin, err := loadAgentsWithAdminAssembler(agentsDir, centerDir, filepath.Join(root, "chats"), true, assembler)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agents["conflict"]; ok {
		t.Fatal("conflicting Agent must not be published")
	}
	if diagnostic := firstRuntimeAssemblerDiagnostic(admin["conflict"]); diagnostic.Code != "runtime_agent_assembly_failed" ||
		!strings.Contains(diagnostic.Message, "content conflict") {
		t.Fatalf("conflict diagnostic = %#v", diagnostic)
	}
	if _, ok := agents["identical"]; !ok {
		t.Fatalf("identical config files should be accepted: %#v", admin["identical"])
	}
	if _, ok := agents["healthy"]; !ok {
		t.Fatalf("single Agent failure isolated healthy Agent: %#v", admin["healthy"])
	}
}

func TestRuntimeAgentAssemblerRejectsCaseFoldAndStructuralConflicts(t *testing.T) {
	tests := []struct {
		name     string
		alphaRel string
		betaRel  string
	}{
		{name: "case-fold", alphaRel: "HTTPX/bridge.toml", betaRel: "httpx/bridge.toml"},
		{name: "file-directory", alphaRel: "httpx", betaRel: "httpx/bridge.toml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			agentsDir := filepath.Join(root, "agents")
			centerDir := filepath.Join(root, "skills-center")
			writeRuntimeAssemblerAgent(t, agentsDir, "broken", []string{"alpha", "beta"})
			for _, id := range []string{"alpha", "beta"} {
				writeRuntimeAssemblerSkill(t, filepath.Join(centerDir, id), id)
			}
			writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "alpha", ".config", filepath.FromSlash(tc.alphaRel)), "alpha")
			writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "beta", ".config", filepath.FromSlash(tc.betaRel)), "beta")

			assembler, err := newRuntimeAgentAssembler(filepath.Join(root, "ru-agents"), centerDir)
			if err != nil {
				t.Fatal(err)
			}
			agents, admin, err := loadAgentsWithAdminAssembler(agentsDir, centerDir, filepath.Join(root, "chats"), true, assembler)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := agents["broken"]; ok {
				t.Fatal("conflicting Agent must not be published")
			}
			if diagnostic := firstRuntimeAssemblerDiagnostic(admin["broken"]); !strings.Contains(diagnostic.Message, "conflict") &&
				!strings.Contains(diagnostic.Message, "collision") {
				t.Fatalf("diagnostic = %#v", diagnostic)
			}
		})
	}
}

func TestRuntimeAgentAssemblerDoesNotFallbackWhenLocalSkillIsInvalid(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	centerDir := filepath.Join(root, "skills-center")
	writeRuntimeAssemblerAgent(t, agentsDir, "writer", []string{"office"})
	writeRuntimeAssemblerSkill(t, filepath.Join(centerDir, "office"), "Center Office")
	if err := os.MkdirAll(filepath.Join(agentsDir, "writer", "skills", "office"), 0o755); err != nil {
		t.Fatal(err)
	}

	assembler, err := newRuntimeAgentAssembler(filepath.Join(root, "ru-agents"), centerDir)
	if err != nil {
		t.Fatal(err)
	}
	agents, admin, err := loadAgentsWithAdminAssembler(agentsDir, centerDir, filepath.Join(root, "chats"), true, assembler)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agents["writer"]; ok {
		t.Fatal("invalid local skill must not fall back to center")
	}
	if diagnostic := firstRuntimeAssemblerDiagnostic(admin["writer"]); !strings.Contains(diagnostic.Message, "agent-local skill") {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
}

func TestRuntimeAgentAssemblerSupportsStandaloneAgentAndStableHotUpdate(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	centerDir := filepath.Join(root, "skills-center")
	ruAgentsDir := filepath.Join(root, "ru-agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, "flat.yml"), strings.Join([]string{
		"key: flat",
		"name: Flat",
		"mode: REACT",
		"modelConfig:",
		"  modelKey: demo-model",
		"skillConfig:",
		"  skills:",
		"    - office",
	}, "\n"))
	writeRuntimeAssemblerSkill(t, filepath.Join(centerDir, "office"), "Office v1")

	cfg := config.Config{
		Paths: config.PathsConfig{
			AgentsDir:       agentsDir,
			RUAgentsDir:     ruAgentsDir,
			SkillsCenterDir: centerDir,
			ChatsDir:        filepath.Join(root, "chats"),
		},
	}
	registry, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def, ok := registry.AgentDefinition("flat")
	if !ok {
		t.Fatal("standalone Agent was not assembled")
	}
	if def.AgentDir != "" || def.RuntimeDir != filepath.Join(ruAgentsDir, "flat") {
		t.Fatalf("standalone paths = source:%q runtime:%q", def.AgentDir, def.RuntimeDir)
	}
	if _, err := os.Stat(filepath.Join(def.RuntimeDir, "agent.yml")); err != nil {
		t.Fatalf("canonical standalone entry missing: %v", err)
	}
	before, err := os.Stat(def.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "office", "SKILL.md"), "# Office v2\n")
	if err := registry.Reload(nil, "agents"); err != nil {
		t.Fatalf("reload agents: %v", err)
	}
	after, err := os.Stat(def.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("stable runtime Agent directory was replaced during hot reload")
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, "skills", "office", "SKILL.md"), "# Office v2")
	if err := os.Remove(filepath.Join(centerDir, "office", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(nil, "agents"); err != nil {
		t.Fatalf("reload invalid candidate: %v", err)
	}
	if _, ok := registry.AgentDefinition("flat"); ok {
		t.Fatal("invalid candidate Agent remains published")
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, "skills", "office", "SKILL.md"), "# Office v2")
	writeRuntimeAssemblerFile(t, filepath.Join(centerDir, "office", "SKILL.md"), "# Office v3\n")
	if err := registry.Reload(nil, "agents"); err != nil {
		t.Fatalf("reload repaired candidate: %v", err)
	}
	if _, ok := registry.AgentDefinition("flat"); !ok {
		t.Fatal("repaired Agent was not republished")
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, "skills", "office", "SKILL.md"), "# Office v3")
	if err := os.Remove(filepath.Join(agentsDir, "flat.yml")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(nil, "agents"); err != nil {
		t.Fatalf("reload deleted agent: %v", err)
	}
	if _, err := os.Stat(def.RuntimeDir); !os.IsNotExist(err) {
		t.Fatalf("deleted runtime Agent remains: %v", err)
	}
}

func writeRuntimeAssemblerAgent(t *testing.T, agentsDir, key string, skills []string) {
	t.Helper()
	lines := []string{
		"key: " + key,
		"name: " + key,
		"mode: REACT",
		"modelConfig:",
		"  modelKey: demo-model",
	}
	if len(skills) > 0 {
		lines = append(lines, "skillConfig:", "  skills:")
		for _, skill := range skills {
			lines = append(lines, "    - "+skill)
		}
	}
	writeRuntimeAssemblerFile(t, filepath.Join(agentsDir, key, "agent.yml"), strings.Join(lines, "\n"))
}

func writeRuntimeAssemblerSkill(t *testing.T, dir, title string) {
	t.Helper()
	writeRuntimeAssemblerFile(t, filepath.Join(dir, "SKILL.md"), "# "+title+"\n\nInstructions")
}

func writeRuntimeAssemblerFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertRuntimeAssemblerContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(content)); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func mustRuntimeAssemblerMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

func firstRuntimeAssemblerDiagnostic(agent AdminAgent) AdminAgentDiagnostic {
	if len(agent.Diagnostics) == 0 {
		return AdminAgentDiagnostic{}
	}
	return agent.Diagnostics[0]
}
