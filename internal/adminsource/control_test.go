package adminsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type controlRegistry struct {
	catalog.Registry
	fail bool
}

func (r *controlRegistry) Reload(context.Context, string) error {
	if r.fail {
		r.fail = false
		return errors.New("test reload failure")
	}
	return nil
}
func (r *controlRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{}, true
}
func controlFixture(t *testing.T) *ControlService {
	t.Helper()
	root := t.TempDir()
	return &ControlService{Mutations: NewService(), Config: config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills"), ConnectorsCenterDir: filepath.Join(root, "connectors")}}, Registry: &controlRegistry{}}
}
func putControlFile(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestControlVersionAndApprovalChanges(t *testing.T) {
	s := controlFixture(t)
	path := filepath.Join(s.Config.Paths.AgentsDir, "other", "AGENTS.md")
	putControlFile(t, path, "old")
	target := ControlTarget{ResourceType: "agent", ResourceKey: "other", Path: "AGENTS.md"}
	view, e := s.Read(target)
	if e != nil {
		t.Fatal(e)
	}
	c := ControlChange{ControlTarget: target, Action: "apply", Content: "new", BaseRevision: view.BaseRevision}
	p, e := s.Prepare(c, "caller")
	if e != nil {
		t.Fatal(e)
	}
	putControlFile(t, path, "changed while awaiting")
	if _, e = s.Apply(context.Background(), c, "caller", p.Digest); e == nil {
		t.Fatal("stale review wrote source")
	}
	view, _ = s.Read(target)
	c.BaseRevision = view.BaseRevision
	p, _ = s.Prepare(c, "caller")
	changed := c
	changed.Content = "unreviewed"
	if _, e = s.Apply(context.Background(), changed, "caller", p.Digest); e == nil {
		t.Fatal("changed candidate reused approval")
	}
	result, e := s.Apply(context.Background(), c, "caller", p.Digest)
	if e != nil || result["status"] != "applied" {
		t.Fatalf("%v %v", result, e)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new" {
		t.Fatal(string(b))
	}
}
func TestControlRollbackAndProtection(t *testing.T) {
	s := controlFixture(t)
	target := ControlTarget{ResourceType: "agent", ResourceKey: "other", Path: "SOUL.md"}
	path := filepath.Join(s.Config.Paths.AgentsDir, "other", "SOUL.md")
	putControlFile(t, path, "old")
	view, _ := s.Read(target)
	c := ControlChange{ControlTarget: target, Action: "apply", Content: "new", BaseRevision: view.BaseRevision}
	p, e := s.Prepare(c, "caller")
	if e != nil {
		t.Fatal(e)
	}
	s.Registry.(*controlRegistry).fail = true
	result, e := s.Apply(context.Background(), c, "caller", p.Digest)
	if e != nil || result["status"] != "rolled_back" {
		t.Fatalf("%v %v", result, e)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "old" {
		t.Fatal("rollback lost old source")
	}
	if _, e = s.Prepare(c, "other"); e == nil {
		t.Fatal("self mutation allowed")
	}
	for _, path := range []string{"../SOUL.md", "/tmp/out", "sub/../../out"} {
		bad := target
		bad.Path = path
		if _, e = s.Read(bad); e == nil {
			t.Fatal("unsafe path", path)
		}
	}
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Join(s.Config.Paths.AgentsDir, "linked")); e != nil {
		t.Fatal(e)
	}
	target.ResourceKey = "linked"
	if _, e = s.Read(target); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestControlPreserveEnvironment(t *testing.T) {
	s := controlFixture(t)
	target := ControlTarget{ResourceType: "agent", ResourceKey: "other", Path: "agent.yml"}
	path := filepath.Join(s.Config.Paths.AgentsDir, "other", "agent.yml")
	content := "key: other\nname: Other\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  env:\n    TOKEN: top-secret-value\n"
	putControlFile(t, path, content)
	view, e := s.Read(target)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(view.Content, "top-secret-value") || len(view.RedactedPaths) != 1 {
		t.Fatal(view)
	}
	c := ControlChange{ControlTarget: target, Action: "apply", Content: view.Content, BaseRevision: view.BaseRevision, PreservePaths: []string{"runtimeConfig.env.TOKEN"}}
	p, e := s.Prepare(c, "caller")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(p.Change.Content, "top-secret-value") || strings.Contains(p.After, "top-secret-value") || strings.Contains(p.Before, "top-secret-value") {
		t.Fatal("preserve or redaction broken")
	}
	c.PreservePaths = nil
	if _, e = s.Prepare(c, "caller"); e == nil {
		t.Fatal("redacted placeholder accepted")
	}
}

func TestControlPackageMembershipAndCreateRace(t *testing.T) {
	s := controlFixture(t)
	root := filepath.Join(s.Config.Paths.SkillsCenterDir, "package")
	putControlFile(t, filepath.Join(root, "package.json"), `{"name":"package","skills":[{"id":"old"}]}`)
	putControlFile(t, filepath.Join(root, "old", "SKILL.md"), "---\nname: old\ndescription: Old skill\n---\nOld.")
	revision, _, err := treeRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	change := ControlChange{ControlTarget: ControlTarget{ResourceType: "skill", ResourceKey: "package/new"}, Action: "apply", BaseRevision: revision, Content: "---\nname: new\ndescription: New skill\n---\nNew."}
	plan, err := s.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), change, "caller", plan.Digest); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil || !strings.Contains(string(manifest), `"new"`) {
		t.Fatalf("membership: %s %v", manifest, err)
	}
	if _, err = s.Apply(context.Background(), change, "caller", plan.Digest); err == nil {
		t.Fatal("stale package baseline applied twice")
	}
	fresh := ControlChange{ControlTarget: ControlTarget{ResourceType: "skill", ResourceKey: "race"}, Action: "apply", Content: change.Content}
	pending, err := s.Prepare(fresh, "caller")
	if err != nil {
		t.Fatal(err)
	}
	putControlFile(t, filepath.Join(s.Config.Paths.SkillsCenterDir, "race", "SKILL.md"), change.Content)
	if _, err = s.Apply(context.Background(), fresh, "caller", pending.Digest); err == nil {
		t.Fatal("create overwrote concurrent source")
	}
}

func TestControlRejectsBarePackageAndCaseVariants(t *testing.T) {
	s := controlFixture(t)
	root := filepath.Join(s.Config.Paths.SkillsCenterDir, "bundle")
	putControlFile(t, filepath.Join(root, "package.json"), `{"name":"bundle","skills":[{"id":"member"}]}`)
	putControlFile(t, filepath.Join(root, "member", "SKILL.md"), "---\nname: member\ndescription: Member\n---\nContent")
	member, err := s.Read(ControlTarget{ResourceType: "skill", ResourceKey: "bundle/member"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "member/SKILL.md"} {
		target := ControlTarget{ResourceType: "skill", ResourceKey: "bundle", Path: path}
		if _, err := s.Read(target); err == nil {
			t.Fatal("bare package read accepted")
		}
		for _, action := range []string{"apply", "delete"} {
			if _, err := s.Prepare(ControlChange{ControlTarget: target, Action: action, BaseRevision: member.BaseRevision, Content: "overwrite"}, "caller"); err == nil {
				t.Fatal("bare package mutation accepted")
			}
		}
	}
	for _, key := range []string{"CALLER", "Builtin.protected"} {
		for _, path := range []string{"agent.yml", "SOUL.md", "AGENTS.md"} {
			if _, err := s.Prepare(ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: key, Path: path}, Action: "apply", Content: "changed"}, "caller"); err == nil {
				t.Fatal("protected identity accepted", key, path)
			}
		}
	}
}

func TestControlPreservesSourcePermissions(t *testing.T) {
	s := controlFixture(t)
	root := filepath.Join(s.Config.Paths.AgentsDir, "other")
	path := filepath.Join(root, "SOUL.md")
	putControlFile(t, path, "old")
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	target := ControlTarget{ResourceType: "agent", ResourceKey: "other", Path: "SOUL.md"}
	view, err := s.Read(target)
	if err != nil {
		t.Fatal(err)
	}
	c := ControlChange{ControlTarget: target, Action: "apply", Content: "new", BaseRevision: view.BaseRevision}
	plan, err := s.Prepare(c, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), c, "caller", plan.Digest); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal(info.Mode())
	}
	dir, _ := os.Stat(root)
	if dir.Mode().Perm() != 0750 {
		t.Fatal(dir.Mode())
	}
	body, _ := os.ReadFile(path)
	if string(body) != "new" {
		t.Fatal("source not changed")
	}
}

func TestControlCreatesCompleteHTTPMCPPackage(t *testing.T) {
	for _, mode := range []string{"mcp", "no_auth"} {
		s := controlFixture(t)
		c := ControlChange{ControlTarget: ControlTarget{ResourceType: "connector", ResourceKey: "remote"}, Action: "apply", MCPURL: "https://example.com/mcp", Content: `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp","auth_mode":"` + mode + `"}`}
		plan, err := s.Prepare(c, "caller")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.GeneratedFiles["mcp.json"], c.MCPURL) || !strings.Contains(plan.After, "--- generated mcp.json ---") {
			t.Fatal("generated component missing from review")
		}
		changed := c
		changed.MCPURL = "https://example.com/other"
		if _, err := s.Apply(context.Background(), changed, "caller", plan.Digest); err == nil {
			t.Fatal("URL changed after approval")
		}
		if _, err := s.Apply(context.Background(), c, "caller", plan.Digest); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(s.Config.Paths.ConnectorsCenterDir, "remote", "mcp.json"))
		if err != nil || !strings.Contains(string(b), c.MCPURL) {
			t.Fatalf("%s %v", b, err)
		}
		if _, err := s.Prepare(c, "caller"); err == nil {
			t.Fatal("creation overwritten")
		}
	}
}

func TestControlMutationErrorsIdentifyPreflight(t *testing.T) {
	s := controlFixture(t)
	c := ControlChange{ControlTarget: ControlTarget{ResourceType: "agent", ResourceKey: "caller", Path: "AGENTS.md"}, Action: "apply", Content: "changed"}
	_, err := s.Apply(context.Background(), c, "caller", "approval")
	var failure *contracts.MutationError
	if !errors.As(err, &failure) || failure.State != "not_started" {
		t.Fatalf("preflight marked uncertain: %v", err)
	}
}
