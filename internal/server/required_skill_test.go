package server

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

type testSkillCenter map[string]catalog.SkillDefinition

func (m testSkillCenter) Skills(_ string) []api.SkillSummary {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]api.SkillSummary, 0, len(keys))
	for _, key := range keys {
		items = append(items, api.SkillSummary{Key: key})
	}
	return items
}

func (m testSkillCenter) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	definition, ok := m[key]
	return definition, ok
}

func writeTestSkill(t *testing.T, root string, key string) {
	t.Helper()
	skillDir := filepath.Join(root, key)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+key+"\n\nFollow the workflow."), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestResolveMustUseSkillsSupportsConfiguredAndCenterSkills(t *testing.T) {
	centerDir := t.TempDir()
	runtimeDir := t.TempDir()
	writeTestSkill(t, filepath.Join(runtimeDir, "skills"), "design")
	writeTestSkill(t, centerDir, "pdf")
	def := catalog.AgentDefinition{
		Key:        "coder",
		RuntimeDir: runtimeDir,
		Skills:     []string{"design"},
	}
	center := testSkillCenter{
		"pdf": {Key: "pdf", Name: "PDF"},
	}

	got, err := resolveMustUseSkills(def, centerDir, center, []string{" design ", "DESIGN", " PDF ", "pdf", ""})
	if err != nil {
		t.Fatalf("resolve must-use skills: %v", err)
	}
	if strings.Join(got.Keys, ",") != "design,pdf" {
		t.Fatalf("unexpected must-use skills %#v", got.Keys)
	}
	if !got.HasExtraSkills || len(got.Skills) != 2 {
		t.Fatalf("unexpected resolution %#v", got)
	}
	if got.Skills[0].InstructionsPath != "@skills/design/SKILL.md" || got.Skills[0].Extra {
		t.Fatalf("configured skill = %#v", got.Skills[0])
	}
	if got.Skills[1].InstructionsPath != "@skills-center/pdf/SKILL.md" || !got.Skills[1].Extra {
		t.Fatalf("center skill = %#v", got.Skills[1])
	}
	designRoot, err := pathutil.Canonicalize(filepath.Join(runtimeDir, "skills", "design"))
	if err != nil {
		t.Fatal(err)
	}
	pdfRoot, err := pathutil.Canonicalize(filepath.Join(centerDir, "pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills[0].RootPath != designRoot.Host || got.Skills[1].RootPath != pdfRoot.Host {
		t.Fatalf("unexpected canonical skill roots: %#v", got.Skills)
	}
	runAccess, err := mustUseSkillRunAccess(got.Skills)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(runAccess.ReadRoots, ",") != strings.Join([]string{designRoot.Host, pdfRoot.Host}, ",") ||
		strings.Join(runAccess.ReadonlyRoots, ",") != strings.Join(runAccess.ReadRoots, ",") {
		t.Fatalf("unexpected must-use run access %#v", runAccess)
	}
	emptyAccess, err := mustUseSkillRunAccess(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyAccess.ReadRoots) != 0 || len(emptyAccess.ReadonlyRoots) != 0 {
		t.Fatalf("empty selection must not create run access roots: %#v", emptyAccess)
	}
	if _, err := resolveMustUseSkills(def, centerDir, center, []string{"missing"}); err == nil {
		t.Fatal("expected missing must-use skill to fail")
	}
}

func TestMustUseRejectsConnectorSkillWithoutMountEvenIfAlsoInConfiguredList(t *testing.T) {
	key := "wecomcli-shared"
	def := catalog.AgentDefinition{Skills: []string{key}, ConnectorSkills: []catalog.ConnectorSkill{{Key: key, ConnectorID: "wecom", Name: key}}}
	if _, err := resolveMustUseSkills(def, t.TempDir(), testSkillCenter{}, []string{strings.ToUpper(key)}); err == nil || !strings.Contains(err.Error(), "no valid agent mount") {
		t.Fatalf("connector skill accepted by mustUseSkills: %v", err)
	}
}

func TestResolveMustUseSkillsRevalidatesCenterContent(t *testing.T) {
	centerDir := t.TempDir()
	center := testSkillCenter{"pdf": {Key: "pdf"}}
	if _, err := resolveMustUseSkills(catalog.AgentDefinition{Key: "coder"}, centerDir, center, []string{"pdf"}); err == nil {
		t.Fatal("expected catalog entry without current SKILL.md to fail")
	}
}

func TestResolveMustUseSkillsRejectsSkillRootSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not portable on Windows")
	}
	centerDir := t.TempDir()
	outside := t.TempDir()
	writeTestSkill(t, outside, "pdf")
	if err := os.Symlink(filepath.Join(outside, "pdf"), filepath.Join(centerDir, "pdf")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	center := testSkillCenter{"pdf": {Key: "pdf"}}
	if _, err := resolveMustUseSkills(catalog.AgentDefinition{Key: "coder"}, centerDir, center, []string{"pdf"}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected escaped center skill root rejection, got %v", err)
	}
}

func TestBuildMustUseSkillConstraintIsMandatory(t *testing.T) {
	got := buildMustUseSkillConstraint([]resolvedMustUseSkill{{
		Key:              "design",
		InstructionsPath: "@skills/design/SKILL.md",
	}})
	for _, expected := range []string{
		"Must-use skills for this run:",
		"skillId: design",
		"path: @skills/design/SKILL.md",
		"must read the complete SKILL.md",
		"None may be skipped",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in constraint %q", expected, got)
		}
	}
}

func TestRuntimeExtraMountsForMustUseSkillsAddsOneReadonlyCenterMount(t *testing.T) {
	mounts := runtimeExtraMountsForMustUseSkills([]map[string]any{
		{"platform": "teams", "mode": "ro"},
	}, true)
	if len(mounts) != 2 || mounts[1] != (contracts.SandboxExtraMount{Platform: "skills-center", Mode: "ro"}) {
		t.Fatalf("mounts = %#v", mounts)
	}

	mounts = runtimeExtraMountsForMustUseSkills([]map[string]any{
		{"platform": "skills-center", "mode": "rw"},
	}, true)
	if len(mounts) != 1 || mounts[0].Mode != "ro" {
		t.Fatalf("deduplicated mounts = %#v", mounts)
	}
}

func TestMustUseRejectsLegacyBuiltinSkillNames(t *testing.T) {
	for _, key := range []string{"builtin-httpx", "builtin-dbx", "BUILTIN-DBX"} {
		def := catalog.AgentDefinition{Skills: []string{key}}
		if _, err := resolveMustUseSkills(def, t.TempDir(), testSkillCenter{}, []string{key}); err == nil || !strings.Contains(err.Error(), "cannot be selected") {
			t.Fatalf("legacy builtin skill accepted: %s %v", key, err)
		}
	}
}

func TestMustUseMountedConnectorSkillPrecedesOrdinaryAndCenter(t *testing.T) {
	const key = "wecomcli-sheet"
	runtimeDir, center := t.TempDir(), t.TempDir()
	pkg := filepath.Join(runtimeDir, "connectors", "wecom")
	script := writeSkillScript(t, filepath.Join(pkg, "skills"), key)
	writeTestSkill(t, filepath.Join(runtimeDir, "skills"), key)
	writeTestSkill(t, center, key)
	def := catalog.AgentDefinition{RuntimeDir: runtimeDir, Skills: []string{key}, Connectors: []string{"wecom"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: key, ConnectorID: "wecom", RuntimeDir: filepath.Join(pkg, "skills", key)}}, ConnectorMounts: []catalog.ConnectorMount{{ID: "wecom", Dir: pkg}}}
	got, err := resolveMustUseSkills(def, center, testSkillCenter{key: {Key: key}}, []string{strings.ToUpper(key), key})
	if err != nil || len(got.Skills) != 1 || got.Keys[0] != key || got.HasExtraSkills || got.Skills[0].Extra {
		t.Fatalf("resolution = %#v, %v", got, err)
	}
	root, err := pathutil.Canonicalize(filepath.Join(pkg, "skills", key))
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills[0].RootPath != root.Host || got.Skills[0].InstructionsPath != "@connectors/wecom/skills/"+key+"/SKILL.md" {
		t.Fatalf("connector source lost: %#v", got.Skills[0])
	}
	access, err := mustUseSkillRunAccess(got.Skills)
	if err != nil || len(access.ReadRoots) != 1 || access.ReadRoots[0] != root.Host || len(access.ReadonlyRoots) != 1 || access.ReadonlyRoots[0] != root.Host {
		t.Fatalf("readonly roots = %#v, %v", access, err)
	}
	session := contracts.QuerySession{AgentKey: "a", RunID: "r"}
	scope := buildSkillScriptScope(session, def, got.Skills)
	ctx := contracts.ExecutionContext{Session: session}
	if len(scope.Roots()) != 0 || scope.Matches(ctx.ScriptOwner(), script, "", false) {
		t.Fatal("connector gained ordinary skill script execution privileges")
	}
}

func TestMustUseConnectorRejectsInvalidMountWithoutCenterFallback(t *testing.T) {
	for _, scenario := range []string{"undeclared", "unmounted", "empty-mount", "empty-skill", "missing", "missing-instructions", "outside", "mount-root", "symlink-root", "symlink-instructions"} {
		t.Run(scenario, func(t *testing.T) {
			const key = "wecomcli-sheet"
			pkg, center := t.TempDir(), t.TempDir()
			writeTestSkill(t, filepath.Join(pkg, "skills"), key)
			writeTestSkill(t, center, key)
			def := catalog.AgentDefinition{Connectors: []string{"wecom"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: key, ConnectorID: "wecom", RuntimeDir: filepath.Join(pkg, "skills", key)}}, ConnectorMounts: []catalog.ConnectorMount{{ID: "wecom", Dir: pkg}}}
			switch scenario {
			case "undeclared":
				def.Connectors = nil
			case "unmounted":
				def.ConnectorMounts = nil
			case "empty-mount":
				def.ConnectorMounts[0].Dir = ""
			case "empty-skill":
				def.ConnectorSkills[0].RuntimeDir = ""
			case "missing":
				def.ConnectorSkills[0].RuntimeDir = filepath.Join(pkg, "skills", "missing")
			case "missing-instructions":
				if err := os.Remove(filepath.Join(def.ConnectorSkills[0].RuntimeDir, "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "outside":
				def.ConnectorSkills[0].RuntimeDir = filepath.Join(center, key)
			case "mount-root":
				def.ConnectorSkills[0].RuntimeDir = pkg
			case "symlink-root":
				link := filepath.Join(pkg, "skills", "escape")
				if err := os.Symlink(filepath.Join(center, key), link); err != nil {
					t.Skip(err)
				}
				def.ConnectorSkills[0].RuntimeDir = link
			case "symlink-instructions":
				file := filepath.Join(def.ConnectorSkills[0].RuntimeDir, "SKILL.md")
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(center, key, "SKILL.md"), file); err != nil {
					t.Skip(err)
				}
			}
			if got, err := resolveMustUseSkills(def, center, testSkillCenter{key: {Key: key}}, []string{key}); err == nil {
				t.Fatalf("invalid connector fell back to center: %#v", got)
			}
		})
	}
}
