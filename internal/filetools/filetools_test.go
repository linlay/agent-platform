package filetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestBuildAccessPlanFromPolicyAllowedByWhitelist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	plan := mustAccessPlan(t, accessPolicyForRoot(root), ReadAccess, "notes.txt")
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

func TestBuildAccessPlanFromPolicyDeniedInfersNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	nested := filepath.Join(outside, "missing", "new.txt")

	plan := mustAccessPlan(t, accessPolicyForRoot(root), WriteAccess, nested)
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

func TestBuildAccessAndWritePlansUseCanonicalKeysForEquivalentForms(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	accessCfg := accessPolicyForRoot(root)
	fileCfg := config.FileToolsConfig{MaxWriteBytes: 1024}

	relativeReadAccess := mustAccessPlan(t, accessCfg, ReadAccess, "notes.txt")
	absoluteReadAccess := mustAccessPlan(t, accessCfg, ReadAccess, filepath.Join(root, ".", "notes.txt"))
	if relativeReadAccess.Path != absoluteReadAccess.Path || relativeReadAccess.Path != filepath.Join(realPathForTest(t, root), "notes.txt") {
		t.Fatalf("expected host paths to remain stable, relative=%#v absolute=%#v", relativeReadAccess, absoluteReadAccess)
	}
	if relativeReadAccess.CommandText != "file_read "+relativeReadAccess.Path {
		t.Fatalf("expected command text to use host path, got %#v", relativeReadAccess)
	}
	if relativeReadAccess.Fingerprint != absoluteReadAccess.Fingerprint || relativeReadAccess.RuleKey != absoluteReadAccess.RuleKey {
		t.Fatalf("expected equivalent path forms to share canonical access keys, relative=%#v absolute=%#v", relativeReadAccess, absoluteReadAccess)
	}

	relativeWriteAccess := mustAccessPlan(t, accessCfg, WriteAccess, "notes.txt")
	absoluteWriteAccess := mustAccessPlan(t, accessCfg, WriteAccess, filepath.Join(root, ".", "notes.txt"))
	relativeWrite, err := BuildWritePlanWithAccess(relativeWriteAccess, fileCfg, map[string]any{"file_path": "notes.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("build relative write plan: %v", err)
	}
	absoluteWrite, err := BuildWritePlanWithAccess(absoluteWriteAccess, fileCfg, map[string]any{"file_path": filepath.Join(root, ".", "notes.txt"), "content": "hello"})
	if err != nil {
		t.Fatalf("build absolute write plan: %v", err)
	}
	if relativeWrite.FilePath != absoluteWrite.FilePath {
		t.Fatalf("expected write host paths to match, relative=%#v absolute=%#v", relativeWrite, absoluteWrite)
	}
	if relativeWrite.Fingerprint != absoluteWrite.Fingerprint || relativeWrite.RuleKey != absoluteWrite.RuleKey {
		t.Fatalf("expected equivalent path forms to share canonical write keys, relative=%#v absolute=%#v", relativeWrite, absoluteWrite)
	}
}

func TestBuildEditPlanWithAccessUsesEditFingerprintAndRuleKey(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	access := mustAccessPlan(t, accessPolicyForRoot(root), WriteAccess, "notes.txt")
	cfg := config.FileToolsConfig{MaxWriteBytes: 1024}

	plan, err := BuildEditPlanWithAccess(access, cfg, map[string]any{
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

	changed, err := BuildEditPlanWithAccess(access, cfg, map[string]any{
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

func TestBuildAccessPlanFromPolicyUsesSessionAliases(t *testing.T) {
	workspace := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
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

	workspacePlan, err := BuildAccessPlanFromPolicy(config.AccessPolicyConfig{}, session, WriteAccess, filepath.Join(workspace, "artifact.md"))
	if err != nil {
		t.Fatalf("build workspace plan: %v", err)
	}
	if !workspacePlan.AllowedByWhitelist || workspacePlan.Root != realPathForTest(t, workspace) {
		t.Fatalf("expected workspace write allowed, got %#v", workspacePlan)
	}
	chatPlan, err := BuildAccessPlanFromPolicy(config.AccessPolicyConfig{}, session, WriteAccess, filepath.Join(chatDir, "artifact.md"))
	if err != nil {
		t.Fatalf("build chat plan: %v", err)
	}
	if !chatPlan.AllowedByWhitelist || chatPlan.Root != realPathForTest(t, chatDir) {
		t.Fatalf("expected chat write allowed, got %#v", chatPlan)
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

func TestBuildWritePlanWithAccessWithoutDescription(t *testing.T) {
	root := t.TempDir()
	access := mustAccessPlan(t, accessPolicyForRoot(root), WriteAccess, "notes.txt")
	cfg := config.FileToolsConfig{MaxWriteBytes: 1024}

	plan, err := BuildWritePlanWithAccess(access, cfg, map[string]any{
		"file_path": "notes.txt",
		"content":   "hello",
	})
	if err != nil {
		t.Fatalf("build write plan without description: %v", err)
	}
	if plan.Description != "" {
		t.Fatalf("expected empty description, got %q", plan.Description)
	}
	if plan.FilePath == "" || plan.Content == nil || plan.Fingerprint == "" {
		t.Fatalf("unexpected write plan metadata: %#v", plan)
	}
}

func TestBuildEditPlanWithAccessWithoutDescription(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	access := mustAccessPlan(t, accessPolicyForRoot(root), WriteAccess, "notes.txt")
	cfg := config.FileToolsConfig{MaxWriteBytes: 1024}

	plan, err := BuildEditPlanWithAccess(access, cfg, map[string]any{
		"file_path":  "notes.txt",
		"old_string": "hello",
		"new_string": "hi",
	})
	if err != nil {
		t.Fatalf("build edit plan without description: %v", err)
	}
	if plan.Description != "" {
		t.Fatalf("expected empty description, got %q", plan.Description)
	}
	if plan.FilePath == "" || plan.OldString != "hello" || plan.NewString != "hi" {
		t.Fatalf("unexpected edit plan fields: %#v", plan)
	}
}

func accessPolicyForRoot(root string) config.AccessPolicyConfig {
	return config.AccessPolicyConfig{
		WorkingDirectory: root,
		Levels: map[string]config.AccessPolicyLevelConfig{
			contracts.AccessLevelDefault: {
				ReadRoots:  []string{"."},
				WriteRoots: []string{"."},
			},
		},
	}
}

func mustAccessPlan(t *testing.T, cfg config.AccessPolicyConfig, mode AccessMode, rawPath string) AccessPlan {
	t.Helper()
	plan, err := BuildAccessPlanFromPolicy(cfg, contracts.QuerySession{}, mode, rawPath)
	if err != nil {
		t.Fatalf("build access plan: %v", err)
	}
	return plan
}

func realPathForTest(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}
