package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func TestKBaseEditingAdversarialWritesFollowCanonicalAccessPolicy(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	outside := filepath.Join(base, "outside")
	hostAccess := filepath.Join(base, "host-access")
	for _, dir := range []string{source, outside, hostAccess} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	executor := fileToolExecutor(source, false)
	executor.cfg.FileTools.RequireReadBeforeWrite = false
	baseContext := kbaseEditingExecutionContext(source)
	baseContext.Session.RuntimeHostAccess = contracts.HostAccessRoots{
		ReadRoots:  []string{hostAccess},
		WriteRoots: []string{hostAccess},
	}
	chatDir := baseContext.Session.RuntimeContext.LocalPaths.ChatAttachmentsDir
	otherChatDir := filepath.Join(filepath.Dir(chatDir), "chat-other")
	if err := os.MkdirAll(otherChatDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assertApprovalThenWrite := func(t *testing.T, rawPath string, target string) {
		t.Helper()
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelDefault
		args := map[string]any{
			"file_path":  rawPath,
			"content":    "approved",
			"pathScope":  "workspace",
			"path_scope": "workspace",
		}
		plan, err := filetools.BuildAccessPlanFromPolicy(
			executor.cfg.AccessPolicy,
			execCtx.Session,
			filetools.WriteAccess,
			rawPath,
		)
		if err != nil {
			t.Fatal(err)
		}
		canonicalTarget := filepath.Join(realPath(t, filepath.Dir(target)), filepath.Base(target))
		if plan.Blocked || plan.AllowedByWhitelist || plan.Path != canonicalTarget {
			t.Fatalf("expected canonical external approval plan, got %#v", plan)
		}
		result, err := executor.Invoke(context.Background(), "file_write", args, execCtx)
		if err != nil {
			t.Fatal(err)
		}
		if result.Structured["error"] != "file_write_path_approval_required" {
			t.Fatalf("external write bypassed common HITL: %#v", result)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("target changed before approval: %v", statErr)
		}
		filetools.RegisterExactAccessApproval(execCtx, plan.Fingerprint)
		result, err = executor.Invoke(context.Background(), "file_write", args, execCtx)
		if err != nil || result.Error != "" {
			t.Fatalf("approved canonical write failed: result=%#v err=%v", result, err)
		}
		if data, readErr := os.ReadFile(target); readErr != nil || string(data) != "approved" {
			t.Fatalf("approved write missed canonical target: data=%q err=%v", string(data), readErr)
		}
	}

	t.Run("absolute_external", func(t *testing.T) {
		assertApprovalThenWrite(t, filepath.Join(outside, "absolute.txt"), filepath.Join(outside, "absolute.txt"))
	})
	t.Run("parent_traversal", func(t *testing.T) {
		assertApprovalThenWrite(t, filepath.Join("..", "outside", "traversal.txt"), filepath.Join(outside, "traversal.txt"))
	})
	t.Run("other_chat_id", func(t *testing.T) {
		assertApprovalThenWrite(t, filepath.Join(otherChatDir, "other.txt"), filepath.Join(otherChatDir, "other.txt"))
	})

	t.Run("host_access_root", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelDefault
		target := filepath.Join(hostAccess, "allowed.txt")
		result, err := executor.Invoke(context.Background(), "file_write", map[string]any{
			"file_path": target,
			"content":   "host allowed",
		}, execCtx)
		if err != nil || result.Error != "" {
			t.Fatalf("hostAccess write failed: result=%#v err=%v", result, err)
		}
	})

	t.Run("configured_write_root", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelDefault
		execCtx.Session.RuntimeHostAccess = contracts.HostAccessRoots{}
		level := executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault]
		level.WriteRoots = append(level.WriteRoots, outside)
		configuredExecutor := fileToolExecutor(source, false)
		configuredExecutor.cfg.FileTools.RequireReadBeforeWrite = false
		configuredExecutor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault] = level
		target := filepath.Join(outside, "configured-root.txt")
		result, err := configuredExecutor.Invoke(context.Background(), "file_write", map[string]any{
			"file_path": target,
			"content":   "configured root",
		}, execCtx)
		if err != nil || result.Error != "" {
			t.Fatalf("configured writeRoots write failed: result=%#v err=%v", result, err)
		}
	})

	t.Run("full_access", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelFullAccess
		target := filepath.Join(outside, "full-access.txt")
		result, err := executor.Invoke(context.Background(), "file_write", map[string]any{
			"file_path": target,
			"content":   "full access",
		}, execCtx)
		if err != nil || result.Error != "" {
			t.Fatalf("full_access write failed: result=%#v err=%v", result, err)
		}
	})

	t.Run("source_symlink_uses_real_target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink setup is not portable on Windows")
		}
		link := filepath.Join(source, "external-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertApprovalThenWrite(t, filepath.Join(link, "symlink.txt"), filepath.Join(outside, "symlink.txt"))
	})

	t.Run("administrator_block_remains_final", func(t *testing.T) {
		blockedExecutor := fileToolExecutor(source, false)
		level := blockedExecutor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault]
		level.Approvals.WriteOutsideRoots = "block"
		blockedExecutor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault] = level
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelDefault
		target := filepath.Join(outside, "blocked.txt")
		result, err := blockedExecutor.Invoke(context.Background(), "file_write", map[string]any{
			"file_path": target,
			"content":   "must not write",
		}, execCtx)
		if err != nil {
			t.Fatal(err)
		}
		if result.Structured["error"] != "file_write_path_blocked" {
			t.Fatalf("administrator block did not remain final: %#v", result)
		}
	})
}

func TestKBaseEditingAdversarialExternalReadsRequireCommonPolicyApproval(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{source, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("classified needle"), 0o644); err != nil {
		t.Fatal(err)
	}

	executor := fileToolExecutor(source, false)
	for _, toolName := range []string{"file_read", "file_glob", "file_grep"} {
		t.Run(toolName+"_without_approval", func(t *testing.T) {
			execCtx := kbaseEditingExecutionContext(source)
			execCtx.Session.AccessLevel = contracts.AccessLevelDefault
			args := kbaseAdversarialReadArgs(toolName, outsideFile, outside)
			result, err := executor.Invoke(context.Background(), toolName, args, execCtx)
			if err != nil {
				t.Fatal(err)
			}
			if result.Structured["error"] != "file_read_approval_required" {
				t.Fatalf("external %s bypassed HITL: %#v", toolName, result)
			}
			if strings.Contains(result.Output, "classified") {
				t.Fatalf("external %s leaked file content before approval: %#v", toolName, result)
			}
		})

		t.Run(toolName+"_with_exact_approval", func(t *testing.T) {
			execCtx := kbaseEditingExecutionContext(source)
			execCtx.Session.AccessLevel = contracts.AccessLevelDefault
			args := kbaseAdversarialReadArgs(toolName, outsideFile, outside)
			rawPath := outsideFile
			if toolName != "file_read" {
				rawPath = outside
			}
			plan, err := filetools.BuildAccessPlanFromPolicy(
				executor.cfg.AccessPolicy,
				execCtx.Session,
				filetools.ReadAccess,
				rawPath,
			)
			if err != nil {
				t.Fatal(err)
			}
			filetools.RegisterExactReadApproval(execCtx, plan.Fingerprint)
			result, err := executor.Invoke(context.Background(), toolName, args, execCtx)
			if err != nil {
				t.Fatal(err)
			}
			if result.Error != "" {
				t.Fatalf("approved external %s failed: %#v", toolName, result)
			}
		})
	}
}

func TestKBaseEditingAdversarialReadApprovalCannotBeReusedForWrite(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{source, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(outside, "approved-read.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	executor := fileToolExecutor(source, false)
	executor.cfg.FileTools.RequireReadBeforeWrite = false
	execCtx := kbaseEditingExecutionContext(source)
	execCtx.Session.AccessLevel = contracts.AccessLevelDefault
	readPlan, err := filetools.BuildAccessPlanFromPolicy(
		executor.cfg.AccessPolicy,
		execCtx.Session,
		filetools.ReadAccess,
		path,
	)
	if err != nil {
		t.Fatal(err)
	}
	filetools.RegisterRuleReadApproval(execCtx, readPlan.RuleKey)
	readResult, err := executor.Invoke(context.Background(), "file_read", map[string]any{
		"file_path":        path,
		"add_line_numbers": false,
	}, execCtx)
	if err != nil || readResult.Error != "" || !strings.Contains(readResult.Output, "original") {
		t.Fatalf("approved read failed: result=%#v err=%v", readResult, err)
	}

	writeResult, err := executor.Invoke(context.Background(), "file_write", map[string]any{
		"file_path": path,
		"content":   "compromised",
	}, execCtx)
	if err != nil {
		t.Fatal(err)
	}
	if writeResult.Structured["error"] != "file_write_path_approval_required" {
		t.Fatalf("read approval widened write permission: %#v", writeResult)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "original" {
		t.Fatalf("read approval attack changed target: data=%q err=%v", string(data), readErr)
	}
}

func TestKBaseAdversarialForbiddenToolsCannotBeInjectedInEitherStage(t *testing.T) {
	source := t.TempDir()
	executor := fileToolExecutor(source, false)
	for _, editing := range []bool{false, true} {
		stage := "main"
		if editing {
			stage = "editing"
		}
		t.Run(stage, func(t *testing.T) {
			execCtx := kbaseExecutionContext(source, editing)
			for _, toolName := range []string{
				"bash",
				"artifact_publish",
				"desktop_action",
				"memory_search",
				"run_query",
				"agent_invoke",
			} {
				t.Run(toolName, func(t *testing.T) {
					result, err := executor.Invoke(context.Background(), toolName, map[string]any{}, execCtx)
					if err != nil {
						t.Fatal(err)
					}
					if result.Error != "kbase_editing_tool_unsupported" {
						t.Fatalf("injected tool %q was not rejected by the executor: %#v", toolName, result)
					}
				})
			}
		})
	}
}

func TestKBaseReadOnlySourceMutationGateCannotBeWidened(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	chatDir := filepath.Join(base, "chats", "chat-1")
	for _, dir := range []string{source, chatDir, filepath.Join(source, "nested")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(source, "policy.txt")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseExecutor := fileToolExecutor(source, false)
	baseExecutor.cfg.FileTools.RequireReadBeforeWrite = false
	baseContext := kbaseExecutionContext(source, false)
	baseContext.Session.RuntimeContext.LocalPaths.ChatAttachmentsDir = chatDir
	baseContext.Session.AccessLevel = contracts.AccessLevelDefault

	assertSourceGate := func(t *testing.T, executor *RuntimeToolExecutor, execCtx *contracts.ExecutionContext, toolName string, args map[string]any) {
		t.Helper()
		result, err := executor.Invoke(context.Background(), toolName, args, execCtx)
		if err != nil {
			t.Fatal(err)
		}
		if result.Structured["error"] != "kbase_editing_mode_required" {
			t.Fatalf("source mutation escaped editingMode gate: %#v", result)
		}
	}

	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "relative_existing_write",
			tool: "file_write",
			args: map[string]any{"file_path": "policy.txt", "content": "blocked"},
		},
		{
			name: "absolute_existing_edit",
			tool: "file_edit",
			args: map[string]any{"file_path": sourcePath, "old_string": "original", "new_string": "blocked"},
		},
		{
			name: "absolute_new_write",
			tool: "file_write",
			args: map[string]any{"file_path": filepath.Join(source, "new.txt"), "content": "blocked"},
		},
		{
			name: "canonical_parent_traversal",
			tool: "file_write",
			args: map[string]any{"file_path": filepath.Join("nested", "..", "traversal.txt"), "content": "blocked"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertSourceGate(t, baseExecutor, cloneKBaseAdversarialContext(baseContext), test.tool, test.args)
		})
	}

	t.Run("full_access", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelFullAccess
		assertSourceGate(t, baseExecutor, execCtx, "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		})
	})

	t.Run("auto_approve", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.AccessLevel = contracts.AccessLevelAutoApprove
		assertSourceGate(t, baseExecutor, execCtx, "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		})
	})

	t.Run("host_access", func(t *testing.T) {
		execCtx := cloneKBaseAdversarialContext(baseContext)
		execCtx.Session.RuntimeHostAccess.WriteRoots = []string{source}
		assertSourceGate(t, baseExecutor, execCtx, "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		})
	})

	t.Run("configured_write_root", func(t *testing.T) {
		executor := fileToolExecutor(source, false)
		executor.cfg.FileTools.RequireReadBeforeWrite = false
		level := executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault]
		level.WriteRoots = []string{source}
		executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault] = level
		assertSourceGate(t, executor, cloneKBaseAdversarialContext(baseContext), "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		})
	})

	t.Run("existing_approval", func(t *testing.T) {
		executor := fileToolExecutor(source, false)
		executor.cfg.FileTools.RequireReadBeforeWrite = false
		level := executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault]
		level.WriteRoots = []string{}
		executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault] = level
		execCtx := cloneKBaseAdversarialContext(baseContext)
		plan, err := filetools.BuildAccessPlanFromPolicy(
			executor.cfg.AccessPolicy,
			execCtx.Session,
			filetools.WriteAccess,
			sourcePath,
		)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Blocked || plan.AllowedByWhitelist || plan.AutoApproved {
			t.Fatalf("expected source AccessPolicy approval plan, got %#v", plan)
		}
		filetools.RegisterExactAccessApproval(execCtx, plan.Fingerprint)
		filetools.RegisterRuleAccessApproval(execCtx, plan.RuleKey)
		assertSourceGate(t, executor, execCtx, "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		})
	})

	t.Run("chat_symlink_into_source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink setup is not portable on Windows")
		}
		link := filepath.Join(chatDir, "source-link")
		if err := os.Symlink(source, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertSourceGate(t, baseExecutor, cloneKBaseAdversarialContext(baseContext), "file_write", map[string]any{
			"file_path": filepath.Join(link, "policy.txt"),
			"content":   "blocked",
		})
	})

	t.Run("administrator_block_precedes_source_gate", func(t *testing.T) {
		executor := fileToolExecutor(source, false)
		level := executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault]
		level.ReadonlyRoots = append(level.ReadonlyRoots, "@workspace")
		executor.cfg.AccessPolicy.Levels[contracts.AccessLevelDefault] = level
		result, err := executor.Invoke(context.Background(), "file_write", map[string]any{
			"file_path": sourcePath,
			"content":   "blocked",
		}, cloneKBaseAdversarialContext(baseContext))
		if err != nil {
			t.Fatal(err)
		}
		if result.Structured["error"] != "file_write_path_blocked" {
			t.Fatalf("administrator block must remain the first visible decision: %#v", result)
		}
	})

	if data, err := os.ReadFile(sourcePath); err != nil || string(data) != "original" {
		t.Fatalf("read-only source changed: data=%q err=%v", string(data), err)
	}
	for _, name := range []string{"new.txt", "traversal.txt"} {
		if _, err := os.Stat(filepath.Join(source, name)); !os.IsNotExist(err) {
			t.Fatalf("read-only source artifact %q was created: %v", name, err)
		}
	}
}

func cloneKBaseAdversarialContext(base *contracts.ExecutionContext) *contracts.ExecutionContext {
	cloned := *base
	cloned.Session = base.Session
	cloned.Session.ToolNames = append([]string(nil), base.Session.ToolNames...)
	cloned.Session.RuntimeHostAccess = contracts.HostAccessRoots{
		ReadRoots:  append([]string(nil), base.Session.RuntimeHostAccess.ReadRoots...),
		WriteRoots: append([]string(nil), base.Session.RuntimeHostAccess.WriteRoots...),
	}
	if base.Session.ScopedFilePolicy != nil {
		policy := *base.Session.ScopedFilePolicy
		cloned.Session.ScopedFilePolicy = &policy
	}
	cloned.ReadFileState = nil
	cloned.FileReadApprovals = nil
	cloned.FileReadRuleApprovals = nil
	cloned.FileAccessApprovals = nil
	cloned.FileAccessRuleApprovals = nil
	cloned.FileWriteApprovals = nil
	cloned.FileWriteRuleApprovals = nil
	return &cloned
}

func kbaseAdversarialReadArgs(toolName string, filePath string, dir string) map[string]any {
	switch toolName {
	case "file_glob":
		return map[string]any{"path": dir, "pattern": "*.txt"}
	case "file_grep":
		return map[string]any{"path": dir, "pattern": "needle"}
	default:
		return map[string]any{"file_path": filePath, "add_line_numbers": false}
	}
}
