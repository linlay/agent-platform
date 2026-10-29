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
	for _, target := range []string{filepath.Join(root, "agents", "a", "agent.yml"), filepath.Join(root, "agents"), root} {
		write, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, WriteAccess, target)
		if err != nil || !write.Blocked() {
			t.Fatalf("write %s: %+v %v", target, write, err)
		}
		read, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, ReadAccess, target)
		if err != nil || read.Blocked() {
			t.Fatalf("read %s: %+v %v", target, read, err)
		}
	}
	write, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, WriteAccess, filepath.Join(root, "chat", "output.txt"))
	if err != nil || write.Blocked() {
		t.Fatalf("unrelated write blocked: %+v %v", write, err)
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
