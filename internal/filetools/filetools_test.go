package filetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

func TestBuildAccessPlanAllowedByWhitelist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg := config.FileToolsConfig{
		WorkingDirectory:  root,
		AllowedReadPaths:  []string{"."},
		AllowedWritePaths: []string{"."},
	}

	plan, err := BuildAccessPlan(cfg, ReadAccess, "notes.txt")
	if err != nil {
		t.Fatalf("build access plan: %v", err)
	}
	if !plan.AllowedByWhitelist {
		t.Fatalf("expected whitelist match, got %#v", plan)
	}
	if plan.Path != filepath.Join(realPathForTest(t, root), "notes.txt") {
		t.Fatalf("unexpected path: %#v", plan)
	}
	if plan.Root != realPathForTest(t, root) {
		t.Fatalf("unexpected root: %#v", plan)
	}
	if !strings.HasPrefix(plan.RuleKey, "file-read::") || plan.Fingerprint == "" || plan.CommandText != "read "+plan.Path {
		t.Fatalf("unexpected access metadata: %#v", plan)
	}
}

func TestBuildAccessPlanDeniedInfersNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	nested := filepath.Join(outside, "missing", "new.txt")
	cfg := config.FileToolsConfig{
		WorkingDirectory:  root,
		AllowedReadPaths:  []string{"."},
		AllowedWritePaths: []string{"."},
	}

	plan, err := BuildAccessPlan(cfg, WriteAccess, nested)
	if err != nil {
		t.Fatalf("build access plan: %v", err)
	}
	if plan.AllowedByWhitelist {
		t.Fatalf("expected denied path, got %#v", plan)
	}
	if plan.Root != realPathForTest(t, outside) {
		t.Fatalf("expected nearest existing ancestor root, got %#v", plan)
	}
	if !strings.HasPrefix(plan.RuleKey, "file-write::") || plan.Fingerprint == "" {
		t.Fatalf("unexpected access metadata: %#v", plan)
	}
}

func TestConfigWithSessionReadRootsOnlyExtendsReadAccess(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent-a")
	skillsDir := filepath.Join(agentDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	cfg := config.FileToolsConfig{
		WorkingDirectory:  root,
		AllowedReadPaths:  []string{"."},
		AllowedWritePaths: []string{"."},
	}
	session := contracts.QuerySession{RuntimeContext: contracts.RuntimeRequestContext{
		LocalPaths: contracts.LocalPaths{
			AgentDir:  agentDir,
			SkillsDir: skillsDir,
		},
	}}

	readCfg := ConfigWithSessionReadRoots(cfg, ReadAccess, session)
	if len(readCfg.AllowedReadPaths) != 3 {
		t.Fatalf("expected session read roots appended, got %#v", readCfg.AllowedReadPaths)
	}
	writeCfg := ConfigWithSessionReadRoots(cfg, WriteAccess, session)
	if strings.Join(writeCfg.AllowedReadPaths, ",") != "." || strings.Join(writeCfg.AllowedWritePaths, ",") != "." {
		t.Fatalf("expected write access config unchanged, got %#v", writeCfg)
	}
	if strings.Join(cfg.AllowedReadPaths, ",") != "." {
		t.Fatalf("expected original config unchanged, got %#v", cfg.AllowedReadPaths)
	}
}

func TestReadApprovalExactAndRule(t *testing.T) {
	plan := AccessPlan{
		Fingerprint: "fp-read",
		RuleKey:     "file-read::root",
	}
	execCtx := &contracts.ExecutionContext{}

	if HasReadApproval(execCtx, plan) || ConsumeReadApproval(execCtx, plan) {
		t.Fatalf("did not expect approval before registration")
	}
	RegisterExactReadApproval(execCtx, plan.Fingerprint)
	if !HasReadApproval(execCtx, plan) {
		t.Fatalf("expected exact read approval")
	}
	if !ConsumeReadApproval(execCtx, plan) {
		t.Fatalf("expected exact read approval to consume")
	}
	if HasReadApproval(execCtx, plan) || ConsumeReadApproval(execCtx, plan) {
		t.Fatalf("expected exact read approval to be consumed")
	}
	RegisterRuleReadApproval(execCtx, plan.RuleKey)
	if !HasReadApproval(execCtx, plan) || !ConsumeReadApproval(execCtx, plan) || !ConsumeReadApproval(execCtx, plan) {
		t.Fatalf("expected rule read approval to persist")
	}
}

func TestAccessApprovalExactAndRule(t *testing.T) {
	plan := AccessPlan{
		Fingerprint: "fp-write-path",
		RuleKey:     "file-write::root",
	}
	execCtx := &contracts.ExecutionContext{}

	if HasAccessApproval(execCtx, plan) || ConsumeAccessApproval(execCtx, plan) {
		t.Fatalf("did not expect approval before registration")
	}
	RegisterExactAccessApproval(execCtx, plan.Fingerprint)
	if !HasAccessApproval(execCtx, plan) {
		t.Fatalf("expected exact access approval")
	}
	if !ConsumeAccessApproval(execCtx, plan) {
		t.Fatalf("expected exact access approval to consume")
	}
	if HasAccessApproval(execCtx, plan) || ConsumeAccessApproval(execCtx, plan) {
		t.Fatalf("expected exact access approval to be consumed")
	}
	RegisterRuleAccessApproval(execCtx, plan.RuleKey)
	if !HasAccessApproval(execCtx, plan) || !ConsumeAccessApproval(execCtx, plan) || !ConsumeAccessApproval(execCtx, plan) {
		t.Fatalf("expected rule access approval to persist")
	}
}

func realPathForTest(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}
