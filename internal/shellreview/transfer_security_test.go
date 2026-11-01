package shellreview

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestTransferProtectsActualDestinationAndHardlinkSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	vars, env := fakeSystemPrograms(t, "cp", "mv", "ln", "mkdir")
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		for _, dir := range []string{"sources", "dest", "links"} {
			if err := os.Mkdir(filepath.Join(f.workspace, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range []string{"sources/protected", "dest/protected", "protected"} {
			if err := os.WriteFile(filepath.Join(f.workspace, file), []byte("protected"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct{ command, readonly string }{
			{"cp sources/protected dest", "dest/protected"},
			{"mv sources/protected dest", "dest/protected"},
			{"ln sources/protected dest", "dest/protected"},
			{"cp -t dest sources/protected", "dest/protected"},
			{"mv --target-directory=dest sources/protected", "dest/protected"},
			{"ln -t dest sources/protected", "dest/protected"},
			{"cp --unknown sources/protected dest", "dest/protected"},
			{"mv --unknown sources/protected dest", "dest/protected"},
			{"ln sources/protected new-alias", "sources/protected"},
			{"cp -l sources/protected new-alias", "sources/protected"},
			{"ln -s ../protected links/alias", "protected"},
			{"mkdir future; cp sources/protected future", "future/protected"},
			{"cp -b sources/protected dest", "dest/protected~"},
		} {
			session := f.session
			session.RunAccessRoots.ReadonlyRoots = []string{filepath.Join(f.workspace, tc.readonly)}
			plan := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, tc.command, f.workspace, vars, env, nil)
			if !plan.Blocked() {
				t.Fatalf("%s at %s bypassed %s: %+v", tc.command, level, tc.readonly, plan)
			}
		}
		// Ordinary copies from readonly sources remain reads; creating a hard alias
		// requires stronger source protection because later writes share its inode.
		session := f.session
		session.RunAccessRoots.ReadonlyRoots = []string{filepath.Join(f.workspace, "sources/protected")}
		if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, "cp sources/protected copied", f.workspace, vars, env, nil); p.Blocked() {
			t.Fatalf("readonly copy source became a write: %+v", p)
		}
	}
}

func TestExplicitAndGeneratedCommandOutputsProtectReadonly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	vars, env := fakeSystemPrograms(t, "git", "curl", "wget", "date", "ls")
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		f.session.RunAccessRoots.ReadonlyRoots = []string{filepath.Join(f.workspace, "protected")}
		for _, command := range []string{
			"git diff --output=protected", "git diff --unknown=./protected", "git reset --hard", "git clean -fd", "git config --file=protected name value", "curl -O https://example.invalid/protected", "wget https://example.invalid/protected", "ls --unknown .",
		} {
			p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
			if !p.Blocked() {
				t.Fatalf("%s at %s bypassed readonly: %+v", command, level, p)
			}
		}
		for _, command := range []string{"wget --no-config -O safe https://example.invalid/x", "curl -q --output=safe https://example.invalid/x"} {
			p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
			if p.Blocked() {
				t.Fatalf("explicit safe output acquired unrelated subtree write: %s: %+v", command, p)
			}
		}
	}
}
