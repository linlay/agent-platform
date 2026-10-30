package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
)

func TestUnsupportedHostShellNeedsExactHumanApprovalInAutoApprove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a real POSIX shell as the unsupported grammar fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "result")
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{
		AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true, ShellExecutable: "/bin/sh",
	}}}
	ctx := bashExecutionContext(root)
	ctx.Session.AccessLevel = AccessLevelAutoApprove
	scope := runenv.NewScope(runenv.Limits{})
	t.Cleanup(scope.Destroy)
	ctx.RunEnvironment = scope
	args := map[string]any{"command": "printf approved > result"}
	preflight := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	if !preflight.UnknownDialect || !preflight.RequiresApproval() {
		t.Fatalf("unsupported shell was not held for human review: %+v", preflight)
	}
	result, err := executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("unapproved invocation: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unapproved command executed")
	}
	accesspolicy.RegisterExactApproval(ctx, preflight.Fingerprint)
	// Changing run.env between approval and invocation must invalidate the grant.
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "REVIEW_VALUE", Value: "changed"}); err != nil {
		t.Fatal(err)
	}
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("old environment grant was reused: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("changed-environment command executed")
	}
	fresh := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	accesspolicy.RegisterExactApproval(ctx, fresh.Fingerprint)
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("approved invocation: %+v %v", result, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "approved" {
		t.Fatalf("approved output: %q %v", data, err)
	}
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "bash_access_approval_required" {
		t.Fatalf("exact grant was not one-shot: %+v %v", result, err)
	}
	executor.cfg.Bash.ShellFeaturesEnabled = false
	strict := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	if !strict.Blocked() {
		t.Fatalf("strict preflight must not request an unusable grammar approval: %+v", strict)
	}
	result, err = executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "command_not_allowed" {
		t.Fatalf("strict invocation must reject unsupported grammar: %+v %v", result, err)
	}
}

func TestSoftSecurityDoesNotAcceptCommandOnlyGrant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix xargs fixture")
	}
	executor := softSecurityExecutor()
	ctx := bashExecutionContext(t.TempDir())
	command := "printf ok | xargs printf"
	// A command-only hash from before launch binding cannot approve a launch.
	plain := accesspolicy.ReviewBashCommand(executor.cfg.AccessPolicy, ctx.Session, command, "", nil)
	security := plain.SecurityReview(command, nil)
	ctx.BashSecurityApprovals = map[string]int{security.Fingerprint: 1}
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": command}, ctx)
	if err != nil || result.Error != "bash_security_approval_required" {
		t.Fatalf("unbound security grant was accepted: %+v %v", result, err)
	}
}

func TestHostLaunchConsumesCombinedApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix Bash/Python fixture")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{
		Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true},
		AccessPolicy: config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
			AccessLevelAskForApproval: {Approvals: config.AccessPolicyApprovalConfig{BashWriteInWriteRoots: "hitl"}},
		}},
	}}
	ctx := bashExecutionContext(root)
	args := map[string]any{"command": `printf ok > marker; python3 -c 'print(1)'`}
	p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	if !p.RequiresApproval() || len(p.Requirements) < 2 {
		t.Fatalf("expected write and opaque execution requirements: %+v", p)
	}
	accesspolicy.RegisterExactApproval(ctx, accesspolicy.PendingBashPlan(ctx, p).Fingerprint)
	result, err := executor.invokeHostBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("combined approval did not survive final launch review: %+v %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(data) != "ok" {
		t.Fatalf("approved write: %q %v", data, err)
	}
	if len(ctx.AccessPolicyApprovals) != 0 {
		t.Fatal("combined exact approval was not consumed")
	}
}

func TestUnsafeRawTextIsRejectedBeforeTrimmingOrShellResolution(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{ShellExecutable: "/missing/shell", ShellFeaturesEnabled: true}}}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.AccessLevel = AccessLevelFullAccess
	for _, command := range []string{"echo '\u202ehidden'", "echo '\x1b[2Khidden'", "echo ok\x1b"} {
		args := map[string]any{"command": command}
		p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
		if !p.Blocked() {
			t.Fatalf("unsafe preflight passed: %+v", p)
		}
		result, err := executor.invokeHostBash(context.Background(), args, ctx)
		if err != nil || result.Error != "bash_security_blocked" {
			t.Fatalf("unsafe raw text reached shell launch: %+v %v", result, err)
		}
	}
}

func TestHostBashJudgesHiddenCharactersByQuoting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix Bash quoting fixture")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash unavailable")
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}}
	ctx := bashExecutionContext(t.TempDir())
	ctx.Session.AccessLevel = AccessLevelFullAccess
	// Edge whitespace is not silently trimmed away before review.
	for _, command := range []string{
		"\u00a0echo ok", "echo ok\u00a0", "echo\u3000ok", "echo ok\u200b",
		"echo \"$(echo\u3000ok)\"", "echo \"$(echo ok\u200b)\"",
	} {
		args := map[string]any{"command": command}
		if p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy); !p.Blocked() {
			t.Fatalf("unquoted hidden character passed preflight: %q %+v", command, p)
		}
		if result, err := executor.invokeHostBash(context.Background(), args, ctx); err != nil || result.Error != "bash_security_blocked" {
			t.Fatalf("unquoted hidden character reached execution: %q %+v %v", command, result, err)
		}
	}
	payload := "\u3000\u3000\U0001f468\u200d\U0001f4bb"
	result, err := executor.invokeHostBash(context.Background(), map[string]any{"command": "printf '%s' '" + payload + "'"}, ctx)
	if err != nil || result.Error != "" || !strings.Contains(result.Output, payload) {
		t.Fatalf("quoted data was not passed verbatim: %+v %v", result, err)
	}
}
