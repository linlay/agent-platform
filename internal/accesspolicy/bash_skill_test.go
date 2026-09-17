package accesspolicy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/skillsexec"
)

func skillFixture(t *testing.T) (*ExecutionContext, string) {
	t.Helper()
	ctx, _ := authoredFixture(t)
	ctx.AuthoredScripts = nil
	root := filepath.Join(ctx.Session.WorkspaceRoot, "selected")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "scripts", "task.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho skill\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root}})
	return ctx, file
}
func TestSkillExecutionFormsAndLevels(t *testing.T) {
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		for _, form := range []string{"sh selected/scripts/task.sh", "bash selected/scripts/task.sh", "./selected/scripts/task.sh", "env sh selected/scripts/task.sh"} {
			t.Run(level+"/"+form, func(t *testing.T) {
				ctx, _ := skillFixture(t)
				ctx.Session.AccessLevel = level
				p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, form, "", nil, ctx)
				if !p.Allowed() || p.AutoApproved() || p.RuleKey != "bash-access:skill-script" || len(ApprovalRules(p)) != 0 {
					t.Fatalf("%+v", p)
				}
			})
		}
	}
}
func TestSkillDoesNotGrantOtherRequirements(t *testing.T) {
	ctx, file := skillFixture(t)
	for _, command := range []string{"sh selected/scripts/task.sh; sh foreign.sh", "sh selected/scripts/task.sh > /outside-review/result", "sh selected/scripts/task.sh; touch /outside-review/result", "sh -c 'echo inline'"} {
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, ctx); !p.RequiresApproval() {
			t.Fatalf("lost requirement %s: %+v", command, p)
		}
	}
	ctx.Session.RunAccessRoots.ReadonlyRoots = []string{filepath.Dir(file)}
	p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "sh selected/scripts/task.sh > selected/scripts/out", "", nil, ctx)
	if !p.Blocked() {
		t.Fatalf("readonly lost: %+v", p)
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelDefault: {Approvals: config.AccessPolicyApprovalConfig{BashOpaqueCommand: "block"}}}}
	if p := ReviewBashCommand(cfg, ctx.Session, "sh selected/scripts/task.sh", "", nil, ctx); !p.Blocked() {
		t.Fatalf("admin block lost: %+v", p)
	}
	ctx.Session.RunID = "other"
	if p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "sh selected/scripts/task.sh", "", nil, ctx); !p.RequiresApproval() {
		t.Fatalf("cross-run grant: %+v", p)
	}
}
func TestSkillContainerUsesActualMappingAndDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	ctx, file := skillFixture(t)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeEnvironmentID = "container"
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	root := filepath.Dir(filepath.Dir(file))
	ctx.Session.RuntimeContext.LocalPaths.SkillsDir = filepath.Dir(root)
	ctx.Session.RuntimeContext.SandboxPaths.SkillsDir = "/skills"
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root, Guest: "/skills/selected"}})
	content, _ := os.ReadFile(file)
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	canonical := "/skills/selected/scripts/task.sh"
	env := &BashEnvironment{
		Resolve:   func(string, string, map[string]string) (string, error) { return "/usr/bin/dash", nil },
		Directory: func(string) (string, error) { return "/workspace", nil },
		Canonical: func(string, string) (string, error) { return canonical, nil },
		Inspect:   func(string) (string, string, error) { return string(content), digest, nil },
	}
	command := "sh /skills/selected/scripts/task.sh"
	p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, env, ctx)
	if !p.Allowed() || p.RuleKey != "bash-access:skill-script" {
		t.Fatalf("container grant: %+v", p)
	}
	canonical = "/skills/sibling/scripts/task.sh"
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, env, ctx); !p.RequiresApproval() {
		t.Fatalf("guest escape: %+v", p)
	}
	canonical = "/skills/selected/scripts/task.sh"
	digest = "foreign"
	if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, env, ctx); !p.RequiresApproval() {
		t.Fatalf("guest digest: %+v", p)
	}
}

func TestSkillNodeCJSUsesSkillProof(t *testing.T) {
	if systemExecutables["node"] == "" {
		t.Skip("node unavailable")
	}
	ctx, file := skillFixture(t)
	cjs := filepath.Join(filepath.Dir(file), "build-call.cjs")
	if err := os.WriteFile(cjs, []byte("console.log(JSON.stringify({expression: 'ok'}));\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(file))
	ctx.Session.SkillScripts = skillsexec.New(ctx.ScriptOwner(), []skillsexec.Root{{Host: root}})
	command := "node selected/scripts/build-call.cjs input.json"
	p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, ctx)
	if !p.Allowed() || p.RuleKey != "bash-access:skill-script" {
		t.Fatalf("cjs not covered by skill proof: %+v", p)
	}
	for _, command := range []string{"node -e 'console.log(1)'", "node -r ./foreign.cjs selected/scripts/build-call.cjs"} {
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, ctx); !p.RequiresApproval() {
			t.Fatalf("inline/preload unexpectedly exempt: %s %+v", command, p)
		}
	}
}
