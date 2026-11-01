package accesspolicy

import (
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
