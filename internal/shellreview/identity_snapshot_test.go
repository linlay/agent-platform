package shellreview

import (
	"runtime"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/identity"
)

func TestHostSystemSnapshotIsScopedToItsReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	vars, environment := fakeSystemPrograms(t, "env", "ls")
	const command = "env ls notes.txt"
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, environment, nil); !p.Allowed() {
		t.Fatalf("explicit system snapshot did not recognize its installations: %+v", p)
	}
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars); !p.RequiresApproval() {
		t.Fatalf("a different review inherited the injected system trust: %+v", p)
	}
	// The inventory returned to callers is a copy, including the builtin and
	// wrapper maps; changing it must not change future identity decisions.
	programs := identity.SystemPrograms()
	programs["env"] = environment.SystemPrograms["env"]
	if identity.SystemPrograms()["env"] == programs["env"] {
		t.Fatal("system installation inventory exposed mutable process trust")
	}
	wrappers := identity.WrapperNames()
	delete(wrappers, "env")
	if !identity.IsWrapper("env") {
		t.Fatal("wrapper inventory exposed mutable process classification")
	}
}
