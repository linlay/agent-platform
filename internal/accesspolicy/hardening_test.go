package accesspolicy

import (
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"os"
	"path/filepath"
	"testing"
)

func policyFixture(t *testing.T) QuerySession {
	t.Helper()
	root := t.TempDir()
	s := QuerySession{AccessLevel: AccessLevelDefault, WorkspaceRoot: filepath.Join(root, "workspace"), TempRoot: filepath.Join(root, "temp")}
	s.TempRoots = []string{s.TempRoot}
	for _, dir := range []string{s.WorkspaceRoot, s.TempRoot} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

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
	protected := filepath.Join(s.WorkspaceRoot, ".state")
	s.ProtectedPaths = []string{protected}
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			p, err := BuildPathPlan(config.AccessPolicyConfig{}, s, mode, filepath.Join(protected, "identity", "token"))
			if err != nil || !p.Blocked() {
				t.Fatalf("%s %s: %+v %v", level, mode, p, err)
			}
		}
		for _, command := range []string{"curl file://" + protected + "/identity/token", "tar -cf archive.tar .", "rg token ."} {
			if p := ReviewBashCommand(config.AccessPolicyConfig{}, s, command, s.WorkspaceRoot, nil); !p.Blocked() {
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
	review := func() BashPlan {
		return ReviewBashCommand(config.AccessPolicyConfig{}, s, "sh task.sh", s.WorkspaceRoot, nil, ctx)
	}
	first := review()
	RegisterRuleApproval(ctx, first.RuleKey)
	if !HasApproval(ctx, review()) {
		t.Fatal("same version lost grant")
	}
	if err := os.WriteFile(script, []byte("echo two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if HasApproval(ctx, review()) {
		t.Fatal("changed content reused grant")
	}
}
