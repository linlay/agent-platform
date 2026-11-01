package shellreview

import (
	"os"
	"path/filepath"
	"testing"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
)

func TestImplicitFilenameAndPhysicalParentTraversal(t *testing.T) {
	s := policyFixture(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(s.WorkspaceRoot, "plain.txt")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(outside, "child"), filepath.Join(s.WorkspaceRoot, "jump")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"cat plain.txt", "cat ./plain.txt", "cat jump/../secret", "cat '" + s.WorkspaceRoot + "/jump/../secret'"} {
		p := ReviewBashCommand(config.AccessPolicyConfig{}, s, cmd, s.WorkspaceRoot, nil)
		if !p.RequiresApproval() {
			t.Fatalf("%s bypassed: %+v", cmd, p)
		}
	}
}

func TestReadWriteOperandSeparationAndProtectedDescendants(t *testing.T) {
	s := policyFixture(t)
	s.AccessLevel = AccessLevelAutoApprove
	outside := filepath.Join(t.TempDir(), "result")
	for _, cmd := range []string{"curl -o '" + outside + "' https://example.invalid", "touch '" + outside + "'", "cp local '" + outside + "'"} {
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, s, cmd, s.WorkspaceRoot, nil); !p.RequiresApproval() {
			t.Fatalf("outside write: %s: %+v", cmd, p)
		}
	}
	s.RunAccessRoots.ReadonlyRoots = []string{filepath.Join(s.WorkspaceRoot, "readonly")}
	if err := os.Mkdir(s.RunAccessRoots.ReadonlyRoots[0], 0700); err != nil {
		t.Fatal(err)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, s, "rm -rf .", s.WorkspaceRoot, nil); !p.Blocked() {
		t.Fatalf("readonly descendant: %+v", p)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, s, "rm -rf *", s.WorkspaceRoot, nil); !p.Blocked() {
		t.Fatalf("glob escaped readonly subtree: %+v", p)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, s, "unknown-program --output='"+outside+"'", s.WorkspaceRoot, nil); !p.RequiresApproval() {
		t.Fatalf("opaque argument escaped write check: %+v", p)
	}
}

func TestPlatformStateHardBlockAcrossLevels(t *testing.T) {
	s := policyFixture(t)
	vars, environment := fakeSystemPrograms(t, "curl", "tar", "rg")
	protected := filepath.Join(s.WorkspaceRoot, ".state")
	s.ProtectedPaths = []string{protected}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			p, err := BuildPathPlan(config.AccessPolicyConfig{}, s, mode, filepath.Join(protected, "identity", "token"))
			if err != nil || !p.Blocked() {
				t.Fatalf("%s %s: %+v %v", level, mode, p, err)
			}
		}
		for _, command := range []string{"curl file://" + protected + "/identity/token", "tar -cf archive.tar .", "rg token ."} {
			if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, s, command, s.WorkspaceRoot, vars, environment, nil); !p.Blocked() {
				t.Fatalf("protected indirect access %s at %s: %+v", command, level, p)
			}
		}
	}
}

func TestRunGrantsDoNotBroadenToParentOrChangedScript(t *testing.T) {
	s := policyFixture(t)
	outside := t.TempDir()
	a, _ := BuildPathPlan(config.AccessPolicyConfig{}, s, WriteAccess, filepath.Join(outside, "new", "note"))
	b, _ := BuildPathPlan(config.AccessPolicyConfig{}, s, WriteAccess, filepath.Join(outside, "other"))
	if a.RuleKey == b.RuleKey {
		t.Fatal("new file grant expanded to parent")
	}
	ctx := &ExecutionContext{Session: s}
	script := filepath.Join(s.WorkspaceRoot, "task.sh")
	if err := os.WriteFile(script, []byte("echo one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	review := func() shellplan.BashPlan {
		return ReviewBashCommand(config.AccessPolicyConfig{}, s, "sh task.sh", s.WorkspaceRoot, nil, ctx)
	}
	first := review()
	shellplan.RegisterRuleApproval(ctx, first.RuleKey)
	if !shellplan.HasApproval(ctx, review()) {
		t.Fatal("same version lost grant")
	}
	if err := os.WriteFile(script, []byte("echo two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if shellplan.HasApproval(ctx, review()) {
		t.Fatal("changed content reused grant")
	}
}

func TestPreviewRecordsHardBlockAcrossLevels(t *testing.T) {
	s := policyFixture(t)
	preview := filepath.Join(s.WorkspaceRoot, "ru-previews")
	if err := os.MkdirAll(preview, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(preview, "record.json")
	if err := os.WriteFile(record, []byte(`{"shareUrl":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.ProtectedPaths = PlatformProtectedPaths(config.Config{Paths: config.PathsConfig{
		StateDir: filepath.Join(t.TempDir(), ".state"), RUPreviewsDir: preview,
	}})
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			plan, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, mode, record)
			if err != nil || !plan.Blocked() {
				t.Fatalf("%s %s: %+v %v", level, mode, plan, err)
			}
		}
		for _, command := range []string{"cat ru-previews/record.json", "touch ru-previews/new.json", "tar -cf archive.tar ru-previews"} {
			if plan := ReviewBashCommand(config.DefaultAccessPolicyConfig(), s, command, s.WorkspaceRoot, nil); !plan.Blocked() {
				t.Fatalf("preview access %s at %s: %+v", command, level, plan)
			}
		}
	}
}

func TestWebAppsHardBlockAcrossLevelsWhenDisabled(t *testing.T) {
	s := policyFixture(t)
	webapps := filepath.Join(s.WorkspaceRoot, "webapps")
	data := filepath.Join(s.WorkspaceRoot, "webapp-data", "test-app", "data.json")
	for _, path := range []string{
		filepath.Join(webapps, "test-app", "app", "webapp.json"),
		filepath.Join(webapps, "test-app", "policy.json"),
		filepath.Join(webapps, ".staging", "install", "journal.json"),
		data,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		WebApps: config.WebAppsConfig{Enabled: false},
		Paths: config.PathsConfig{
			StateDir:      filepath.Join(t.TempDir(), ".state"),
			WebAppsDir:    webapps,
			WebAppDataDir: filepath.Join(s.WorkspaceRoot, "webapp-data"),
		},
	}
	s.ProtectedPaths = PlatformProtectedPaths(cfg)
	found := false
	for _, path := range s.ProtectedPaths {
		if path == webapps {
			found = true
		}
	}
	if !found {
		t.Fatal("disabled WebApp host left code and authorization records unprotected")
	}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			for _, path := range []string{
				webapps,
				filepath.Join(webapps, "test-app", "app", "webapp.json"),
				filepath.Join(webapps, "test-app", "policy.json"),
				filepath.Join(webapps, ".staging", "install", "journal.json"),
			} {
				plan, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, mode, path)
				if err != nil || !plan.Blocked() {
					t.Fatalf("%s %s %s: %+v %v", level, mode, path, plan, err)
				}
			}
			plan, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, mode, data)
			if err != nil || plan.Blocked() || plan.RequiresApproval() {
				t.Fatalf("application data did not retain ordinary workspace access at %s %s: %+v %v", level, mode, plan, err)
			}
		}
		for _, command := range []string{"cat webapps/test-app/app/webapp.json", "touch webapps/test-app/install.json", "tar -cf archive.tar webapps"} {
			if plan := ReviewBashCommand(config.DefaultAccessPolicyConfig(), s, command, s.WorkspaceRoot, nil); !plan.Blocked() {
				t.Fatalf("WebApp access %s at %s: %+v", command, level, plan)
			}
		}
	}
}
