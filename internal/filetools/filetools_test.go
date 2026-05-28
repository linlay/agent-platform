package filetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
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
	if !strings.HasPrefix(plan.RuleKey, "file-read::") || plan.Fingerprint == "" || plan.CommandText != "file_read "+plan.Path {
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

func TestBuildEditPlanUsesEditFingerprintAndRuleKey(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg := config.FileToolsConfig{
		WorkingDirectory:  root,
		AllowedReadPaths:  []string{"."},
		AllowedWritePaths: []string{"."},
		MaxWriteBytes:     1024,
	}

	plan, err := BuildEditPlan(cfg, map[string]any{
		"file_path":   "notes.txt",
		"old_string":  "hello",
		"new_string":  "hi",
		"replace_all": true,
		"description": "编辑 notes",
	})
	if err != nil {
		t.Fatalf("build edit plan: %v", err)
	}
	if plan.ToolName != "file_edit" || plan.Operation != "edit" || !plan.ReplaceAll {
		t.Fatalf("unexpected edit plan metadata: %#v", plan)
	}
	if plan.FilePath != filepath.Join(realPathForTest(t, root), "notes.txt") || plan.OldString != "hello" || plan.NewString != "hi" {
		t.Fatalf("unexpected edit plan fields: %#v", plan)
	}
	if !strings.HasPrefix(plan.RuleKey, "file-edit::") || plan.Fingerprint == "" || !strings.HasPrefix(plan.CommandText, "file_edit ") {
		t.Fatalf("unexpected edit approval metadata: %#v", plan)
	}

	changed, err := BuildEditPlan(cfg, map[string]any{
		"file_path":   "notes.txt",
		"old_string":  "hello",
		"new_string":  "hi!",
		"description": "编辑 notes",
	})
	if err != nil {
		t.Fatalf("build changed edit plan: %v", err)
	}
	if changed.Fingerprint == plan.Fingerprint {
		t.Fatalf("expected fingerprint to include replacement content, got %#v and %#v", plan, changed)
	}
	if changed.RuleKey != plan.RuleKey {
		t.Fatalf("expected same root rule key for same file root, got %q and %q", plan.RuleKey, changed.RuleKey)
	}
}

func TestConfigWithSessionReadRootsOnlyExtendsReadAccess(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	agentDir := filepath.Join(t.TempDir(), "agent-a")
	skillsDir := filepath.Join(agentDir, "skills")
	skillsMarketDir := filepath.Join(t.TempDir(), "skills-market")
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
			WorkspaceDir:       root,
			ChatAttachmentsDir: chatDir,
			AgentDir:           agentDir,
			SkillsDir:          skillsDir,
			SkillsMarketDir:    skillsMarketDir,
		},
	}}

	readCfg := ConfigWithSessionReadRoots(cfg, ReadAccess, session)
	if len(readCfg.AllowedReadPaths) != 4 {
		t.Fatalf("expected session read roots appended, got %#v", readCfg.AllowedReadPaths)
	}
	if !hasString(readCfg.AllowedReadPaths, filepath.Clean(chatDir)) {
		t.Fatalf("expected chat dir in read roots, got %#v", readCfg.AllowedReadPaths)
	}
	for _, root := range readCfg.AllowedReadPaths {
		if root == filepath.Clean(skillsMarketDir) {
			t.Fatalf("expected skills market dir to stay out of session read roots, got %#v", readCfg.AllowedReadPaths)
		}
	}
	writeCfg := ConfigWithSessionReadRoots(cfg, WriteAccess, session)
	if strings.Join(writeCfg.AllowedReadPaths, ",") != "." || strings.Join(writeCfg.AllowedWritePaths, ",") != "." {
		t.Fatalf("expected write access config unchanged, got %#v", writeCfg)
	}
	if strings.Join(cfg.AllowedReadPaths, ",") != "." {
		t.Fatalf("expected original config unchanged, got %#v", cfg.AllowedReadPaths)
	}
}

func TestConfigWithSessionWriteRootsIncludesChatDir(t *testing.T) {
	workspace := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	cfg := config.FileToolsConfig{
		WorkingDirectory:  workspace,
		AllowedReadPaths:  []string{"."},
		AllowedWritePaths: []string{"."},
	}
	session := contracts.QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       workspace,
				ChatAttachmentsDir: chatDir,
			},
		},
	}

	writeCfg := ConfigWithSessionWriteRoots(cfg, session)
	if writeCfg.WorkingDirectory != workspace {
		t.Fatalf("working directory = %q, want %q", writeCfg.WorkingDirectory, workspace)
	}
	if !hasString(writeCfg.AllowedWritePaths, workspace) || !hasString(writeCfg.AllowedWritePaths, filepath.Clean(chatDir)) {
		t.Fatalf("expected workspace and chat dir write roots, got %#v", writeCfg.AllowedWritePaths)
	}
}

func TestPathInSessionWorkspaceAllowsRootWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.md")
	session := contracts.QuerySession{WorkspaceRoot: string(os.PathSeparator)}
	if !PathInSessionWorkspace(session, path) {
		t.Fatalf("expected %s to be inside root workspace", path)
	}
}

func TestPathInSessionWorkspaceAllowsChatDirWithExplicitWorkspace(t *testing.T) {
	workspace := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	path := filepath.Join(chatDir, "artifact.md")
	session := contracts.QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       workspace,
				ChatAttachmentsDir: chatDir,
			},
		},
	}
	if !PathInSessionWorkspace(session, path) {
		t.Fatalf("expected %s to be inside session chat dir", path)
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

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
