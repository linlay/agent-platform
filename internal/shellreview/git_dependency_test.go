package shellreview

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/identity"
	shellplan "agent-platform/internal/shellreview/plan"
)

func TestGitRechecksSameSizeSameMtimeConfigAndIncludedContent(t *testing.T) {
	git := identity.SystemPrograms()["git"]
	if git == "" {
		t.Skip("system Git unavailable")
	}
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	home := t.TempDir()
	vars := map[string]string{"PATH": os.Getenv("PATH"), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "GIT_CONFIG_NOSYSTEM": "1"}
	cmd := exec.Command(git, "init", "-q", f.workspace)
	cmd.Env = []string{"PATH=" + vars["PATH"], "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	ctx := &contracts.ExecutionContext{Session: f.session}
	review := func(command string) shellplan.BashPlan {
		return ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, ctx)
	}
	configFile := filepath.Join(f.workspace, ".git", "config")
	original, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	clean := append(append([]byte(nil), original...), []byte("\n[core]\n fsmonitor = false\n")...)
	writeDependencyFixture(t, configFile, string(clean))
	if p := review("git status"); !p.Allowed() {
		t.Fatal(p)
	}
	info, err := os.Stat(configFile)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := append(append([]byte(nil), original...), []byte("\n[core]\n fsmonitor = ./run\n")...)
	if len(clean) != len(unsafe) {
		t.Fatal("fixture must preserve byte size")
	}
	writeDependencyFixture(t, configFile, string(unsafe))
	if err := os.Chtimes(configFile, time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if p := review("git status"); !p.RequiresApproval() {
		t.Fatalf("stale clean Git state reused: %+v", p)
	}
	included := filepath.Join(f.workspace, "included.gitconfig")
	writeDependencyFixture(t, included, "[core]\n fsmonitor = ./one\n")
	writeDependencyFixture(t, configFile, string(original)+"\n[include]\n path = ../included.gitconfig\n")
	for _, command := range []string{"git status", "git custom-alias"} {
		first := review(command)
		if !first.RequiresApproval() {
			t.Fatal(first)
		}
		shellplan.RegisterExactApproval(ctx, first.Fingerprint)
		info, err := os.Stat(included)
		if err != nil {
			t.Fatal(err)
		}
		writeDependencyFixture(t, included, "[core]\n fsmonitor = ./two\n")
		if err := os.Chtimes(included, time.Now(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		changed := review(command)
		if first.Fingerprint == changed.Fingerprint || shellplan.HasApproval(ctx, changed) {
			t.Fatalf("included config reused exact grant: %+v", changed)
		}
		writeDependencyFixture(t, included, "[core]\n fsmonitor = ./one\n")
	}
	hook := filepath.Join(f.workspace, ".git", "hooks", "pre-commit")
	writeDependencyFixture(t, hook, "#!/bin/sh\necho one\n")
	if err := os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	first := review("git commit -m x")
	shellplan.RegisterExactApproval(ctx, first.Fingerprint)
	info, err = os.Stat(hook)
	if err != nil {
		t.Fatal(err)
	}
	writeDependencyFixture(t, hook, "#!/bin/sh\necho two\n")
	if err := os.Chtimes(hook, time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if p := review("git commit -m x"); first.Fingerprint == p.Fingerprint || shellplan.HasApproval(ctx, p) {
		t.Fatal("changed hook reused exact approval")
	}
}

func TestStartupCodeCannotBorrowPreciseOrNestedScriptReview(t *testing.T) {
	f := newCapabilityFixture(t, contracts.AccessLevelAskForApproval)
	vars, env := fakeSystemPrograms(t, "curl", "env", "bash", "git")
	for _, command := range []string{
		"LD_PRELOAD=./loader.so curl -q -o /dev/null https://example.invalid",
		"LD_PRELOAD=./loader.so env -u LD_PRELOAD curl -q -o /dev/null https://example.invalid",
		"BASH_ENV=./startup.sh bash -c 'echo safe'",
		"GIT_TRACE=/dev/null git status",
	} {
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, nil)
		if !p.RequiresApproval() {
			t.Fatalf("startup injection borrowed precise execution: %s %+v", command, p)
		}
	}
	readonly := f.session
	readonly.WorkspaceReadOnly = true
	for _, command := range []string{"LD_PRELOAD=./loader.so curl -q https://example.invalid", "BASH_ENV=./startup.sh bash -c 'echo safe'", "GIT_TRACE=./trace git status"} {
		if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, readonly, command, f.workspace, vars, env, nil); !p.Blocked() {
			t.Fatalf("startup code bypassed readonly workspace: %+v", p)
		}
	}

	writeDependencyFixture(t, filepath.Join(f.workspace, "startup.sh"), "echo one\n")
	command := "BASH_ENV=./startup.sh bash -c 'echo safe'"
	ctx := &contracts.ExecutionContext{Session: f.session}
	first := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, ctx)
	shellplan.RegisterExactApproval(ctx, first.Fingerprint)
	writeDependencyFixture(t, filepath.Join(f.workspace, "startup.sh"), "echo two\n")
	changed := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, env, ctx)
	if shellplan.HasApproval(ctx, changed) {
		t.Fatal("startup script bytes did not invalidate exact grant")
	}
}
