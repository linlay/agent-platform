package accesspolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestControlSourceIsReadonlyEvenAtFullAccess(t *testing.T) {
	root := t.TempDir()
	s := contracts.QuerySession{AccessLevel: "full_access", ControlReadonlyRoots: []string{filepath.Join(root, "agents")}}
	for _, target := range []string{filepath.Join(root, "agents", "a", "agent.yml"), filepath.Join(root, "agents")} {
		write, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, WriteAccess, target)
		if err != nil || !write.Blocked() {
			t.Fatalf("write %s: %+v %v", target, write, err)
		}
	}
	for _, target := range []string{filepath.Join(root, "agents", "a", "agent.yml"), filepath.Join(root, "agents"), root} {
		read, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, ReadAccess, target)
		if err != nil || read.Blocked() {
			t.Fatalf("read %s: %+v %v", target, read, err)
		}
	}
	if subtree := BuildSubtreePlan(config.DefaultAccessPolicyConfig(), s, WriteAccess, root); !subtree.Blocked() {
		t.Fatalf("recursive ancestor write permitted: %+v", subtree)
	}
	if subtree := BuildSubtreePlan(config.DefaultAccessPolicyConfig(), s, ReadAccess, root); subtree.Blocked() {
		t.Fatalf("recursive ancestor read blocked: %+v", subtree)
	}
	for _, target := range []string{root, filepath.Join(root, "chat", "output.txt")} {
		write, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, WriteAccess, target)
		if err != nil || write.Blocked() {
			t.Fatalf("unrelated write %s blocked: %+v %v", target, write, err)
		}
	}
}

// A Workspace that contains the runtime root must keep ordinary writes into
// its own directories; only tree-wide changes reach the protected source.
func TestControlSourceAllowsPlainWritesIntoAncestor(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "runtime", "agents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s := contracts.QuerySession{AccessLevel: "full_access", WorkspaceRoot: root, ControlReadonlyRoots: []string{agents}}
	for _, command := range []string{"cp a.txt .", "cp a.txt b.txt", "touch .", "mv a.txt c.txt", "rm -rf build"} {
		if plan := ReviewBashCommand(config.DefaultAccessPolicyConfig(), s, command, root, nil); plan.Blocked() {
			t.Fatalf("plain write into ancestor blocked: %s: %+v", command, plan)
		}
	}
	for _, command := range []string{"rm -rf .", "chmod -R u+w .", "mv runtime moved", "cp -r " + filepath.Join(root, "a.txt") + " runtime", "tar -xf x.tar -C ."} {
		if plan := ReviewBashCommand(config.DefaultAccessPolicyConfig(), s, command, root, nil); !plan.Blocked() {
			t.Fatalf("tree-wide change permitted: %s: %+v", command, plan)
		}
	}
}

func TestControlSourceBashCannotMoveDeleteOrReplaceProtectedTrees(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(agents, "agent.yml")
	if err := os.WriteFile(file, []byte("key: a"), 0600); err != nil {
		t.Fatal(err)
	}
	s := contracts.QuerySession{AccessLevel: "full_access", ControlReadonlyRoots: []string{agents}}
	for _, command := range []string{
		"touch " + file,
		"mv " + file + " " + filepath.Join(root, "moved.yml"),
		"mv " + agents + " " + filepath.Join(root, "renamed"),
		"rm -rf " + root,
		"cp " + filepath.Join(root, "replacement") + " " + file,
	} {
		if plan := ReviewBashCommand(config.DefaultAccessPolicyConfig(), s, command, root, nil); !plan.Blocked() {
			t.Fatalf("protected mutation permitted: %s: %+v", command, plan)
		}
	}
}
