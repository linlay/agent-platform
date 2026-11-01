package shellreview

import (
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDependencyFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func quoteDependency(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'" }
func TestMutableHomeConfigCannotBeApprovedAsPrecise(t *testing.T) {
	vars, env := fakeSystemPrograms(t, "curl")
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove} {
		f := newCapabilityFixture(t, level)
		writeDependencyFixture(t, filepath.Join(f.workspace, ".curlrc"), "url=https://example.invalid\n")
		command := "HOME=" + quoteDependency(f.workspace) + " curl -o /dev/null https://example.invalid"
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
		if !p.RequiresApproval() || !hasNullWriteRequirement(p) {
			t.Fatalf("mutable HOME config borrowed precise/null semantics at %s: %+v", level, p)
		}
	}
}
func TestConfigurationContentVersionInvalidatesExactApproval(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	vars, env := fakeSystemPrograms(t, "curl", "wget", "rg", "sed")
	cases := []struct{ path, command string }{
		{".curlrc", "HOME=. curl https://example.invalid"},
		{"xdg/curlrc", "XDG_CONFIG_HOME=xdg curl https://example.invalid"},
		{"chosen.cfg", "curl -K chosen.cfg https://example.invalid"},
		{".wgetrc", "HOME=. wget -O output https://example.invalid"},
		{"chosen.wget", "WGETRC=chosen.wget wget -O output https://example.invalid"},
		{"system.wget", "SYSTEM_WGETRC=system.wget wget -O output https://example.invalid"},
		{"rg.cfg", "RIPGREP_CONFIG_PATH=rg.cfg rg needle"},
		{"prog.sed", "sed -f prog.sed input"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			file := filepath.Join(f.workspace, tc.path)
			writeDependencyFixture(t, file, "aaaa\n")
			ctx := &contracts.ExecutionContext{Session: f.session}
			review := func() shellplan.BashPlan {
				return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, tc.command, f.workspace, vars, env, ctx)
			}
			first := review()
			if !first.RequiresApproval() {
				t.Fatalf("config/program must be scoped: %+v", first)
			}
			shellplan.RegisterExactApproval(ctx, first.Fingerprint)
			if !shellplan.HasApproval(ctx, review()) {
				t.Fatal("unchanged config lost its exact grant")
			}
			info, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			writeDependencyFixture(t, file, "bbbb\n")
			if err := os.Chtimes(file, time.Now(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			changed := review()
			if first.Fingerprint == changed.Fingerprint || shellplan.HasApproval(ctx, changed) {
				t.Fatalf("same-size/same-mtime config content reused grant: %+v", changed)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRemovedConfigVariablesAndMissingFilesDoNotStayOpaque(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	vars, env := fakeSystemPrograms(t, "env", "curl", "wget", "tar")
	readonlyHome := t.TempDir()
	writeDependencyFixture(t, filepath.Join(readonlyHome, ".curlrc"), "readonly config")
	writeDependencyFixture(t, filepath.Join(readonlyHome, ".wgetrc"), "readonly config")
	mutable := filepath.Join(f.workspace, "mutable")
	writeDependencyFixture(t, filepath.Join(mutable, ".curlrc"), "mutable config")
	writeDependencyFixture(t, filepath.Join(mutable, "wgetrc"), "mutable config")
	vars["HOME"] = readonlyHome
	vars["CURL_HOME"] = mutable
	vars["WGETRC"] = filepath.Join(mutable, "wgetrc")
	vars["TAR_OPTIONS"] = "--to-command=sh"
	for _, command := range []string{
		"env -u CURL_HOME curl https://example.invalid",
		"env --unset=WGETRC wget -O result https://example.invalid",
		"env -u TAR_OPTIONS tar -tf archive.tar",
		"env -i HOME=" + quoteDependency(readonlyHome) + " " + quoteDependency(env.SystemPrograms["curl"]) + " https://example.invalid",
		"CURL_HOME=missing HOME=" + quoteDependency(readonlyHome) + " curl https://example.invalid",
	} {
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
		if !p.Allowed() {
			t.Fatalf("removed/missing mutable config stayed opaque: %s: %+v", command, p)
		}
	}
}
func TestEarlierEffectsCannotCreateAnUnreviewedConfiguration(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	vars, env := fakeSystemPrograms(t, "touch", "curl", "bash")
	// There is no .curlrc yet. Before the standalone invocation this is safe;
	// after an earlier write the same absence cannot prove the runtime state.
	direct := "HOME=. curl -o /dev/null https://example.invalid"
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, direct, f.workspace, vars, env, nil); !p.Allowed() {
		t.Fatal(p)
	}
	for _, command := range []string{"touch .curlrc; " + direct, "unregistered; " + direct, "bash -c 'touch .curlrc'; " + direct, "touch .curlrc; bash -c '" + direct + "'"} {
		if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil); !hasNullWriteRequirement(p) || !p.RequiresApproval() {
			t.Fatalf("prior mutation borrowed absent-config proof: %+v", p)
		}
	}
}

func TestNestedShellKeepsEffectiveConfigEnvironment(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	vars, env := fakeSystemPrograms(t, "env", "bash", "curl")
	writeDependencyFixture(t, filepath.Join(f.workspace, ".curlrc"), "mutable config")
	for _, command := range []string{
		"env HOME=. bash -c 'curl -o /dev/null https://example.invalid'",
		"HOME=. bash -c 'curl -o /dev/null https://example.invalid'",
	} {
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
		if !p.RequiresApproval() || !hasNullWriteRequirement(p) {
			t.Fatalf("nested shell lost effective environment: %s: %+v", command, p)
		}
	}
}
