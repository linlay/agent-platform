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

func TestKBaseEditingAdversarialWriteEscapeAttemptsAreHardBlocked(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	outside := filepath.Join(base, "outside")
	hostAccess := filepath.Join(base, "host-access")
	for _, dir := range []string{source, outside, hostAccess} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	executor := fileToolExecutor(source, true)
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

	outsideVictim := filepath.Join(outside, "victim.txt")
	hostVictim := filepath.Join(hostAccess, "victim.txt")
	otherChatVictim := filepath.Join(otherChatDir, "victim.txt")
	for _, path := range []string{outsideVictim, hostVictim, otherChatVictim} {
		if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sourceLink := filepath.Join(source, "source-link")
	chatLink := filepath.Join(chatDir, "chat-link")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinksAvailable := runtime.GOOS != "windows"
	if symlinksAvailable {
		if err := os.Symlink(outside, sourceLink); err != nil {
			symlinksAvailable = false
			t.Logf("source symlink attack skipped: %v", err)
		}
	}
	if symlinksAvailable {
		if err := os.Symlink(outside, chatLink); err != nil {
			symlinksAvailable = false
			t.Logf("chat symlink attack skipped: %v", err)
		}
	}

	attacks := []struct {
		name    string
		rawPath string
		target  string
	}{
		{name: "absolute_external", rawPath: outsideVictim, target: outsideVictim},
		{name: "parent_traversal", rawPath: filepath.Join("..", "outside", "victim.txt"), target: outsideVictim},
		{name: "host_access_write_root", rawPath: hostVictim, target: hostVictim},
		{name: "other_chat_id", rawPath: otherChatVictim, target: otherChatVictim},
	}
	if symlinksAvailable {
		attacks = append(attacks,
			struct {
				name    string
				rawPath string
				target  string
			}{name: "source_symlink_escape", rawPath: filepath.Join(sourceLink, "victim.txt"), target: outsideVictim},
			struct {
				name    string
				rawPath string
				target  string
			}{name: "chat_symlink_escape", rawPath: filepath.Join(chatLink, "victim.txt"), target: outsideVictim},
		)
	}

	for _, level := range []string{
		contracts.AccessLevelDefault,
		contracts.AccessLevelAutoApprove,
		contracts.AccessLevelFullAccess,
	} {
		for _, attack := range attacks {
			for _, toolName := range []string{"file_write", "file_edit"} {
				t.Run(level+"/"+attack.name+"/"+toolName, func(t *testing.T) {
					execCtx := cloneKBaseAdversarialContext(baseContext)
					execCtx.Session.AccessLevel = level

					// Attempt to forge both common spellings of a caller supplied
					// path classification. File tools must ignore them and derive
					// scope from the canonical path inside the service.
					args := map[string]any{
						"file_path":  attack.rawPath,
						"pathScope":  "workspace",
						"path_scope": "workspace",
					}
					if toolName == "file_edit" {
						args["old_string"] = "original"
						args["new_string"] = "compromised"
					} else {
						args["content"] = "compromised"
					}

					accessPlan, err := filetools.BuildAccessPlanFromPolicy(
						executor.cfg.AccessPolicy,
						execCtx.Session,
						filetools.WriteAccess,
						attack.rawPath,
					)
					if err != nil {
						t.Fatal(err)
					}
					if !accessPlan.Blocked {
						t.Fatalf("attack did not receive KBASE hard ceiling: %#v", accessPlan)
					}

					// Simulate a compromised caller replaying both exact and rule
					// approvals for the real canonical target.
					filetools.RegisterExactAccessApproval(execCtx, accessPlan.Fingerprint)
					filetools.RegisterRuleAccessApproval(execCtx, accessPlan.RuleKey)
					var writePlan filetools.WritePlan
					if toolName == "file_edit" {
						writePlan, err = filetools.BuildEditPlanWithAccess(accessPlan, executor.cfg.FileTools, args)
					} else {
						writePlan, err = filetools.BuildWritePlanWithAccess(accessPlan, executor.cfg.FileTools, args)
					}
					if err != nil {
						t.Fatal(err)
					}
					filetools.RegisterExactWriteApproval(execCtx, writePlan.Fingerprint)
					filetools.RegisterRuleWriteApproval(execCtx, writePlan.RuleKey)

					result, err := executor.Invoke(context.Background(), toolName, args, execCtx)
					if err != nil {
						t.Fatal(err)
					}
					wantError := toolName + "_path_blocked"
					if result.Structured["error"] != wantError {
						t.Fatalf("attack result error = %#v, want %q; result=%#v", result.Structured["error"], wantError, result)
					}
					data, readErr := os.ReadFile(attack.target)
					if readErr != nil || string(data) != "original" {
						t.Fatalf("attack changed target: data=%q err=%v", string(data), readErr)
					}
				})
			}
		}
	}
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
	if writeResult.Structured["error"] != "file_write_path_blocked" {
		t.Fatalf("read approval widened write permission: %#v", writeResult)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "original" {
		t.Fatalf("read approval attack changed target: data=%q err=%v", string(data), readErr)
	}
}

func TestKBaseEditingAdversarialForbiddenToolsCannotBeInjected(t *testing.T) {
	source := t.TempDir()
	executor := fileToolExecutor(source, false)
	execCtx := kbaseEditingExecutionContext(source)
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
		policy.AllowedExtensions = append([]string(nil), base.Session.ScopedFilePolicy.AllowedExtensions...)
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
