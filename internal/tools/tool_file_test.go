package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
)

func TestInvokeReadReadsAllowedFileWithLineRange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{
		"file_path": "notes.txt",
		"offset":    float64(2),
		"limit":     float64(1),
	})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected read success, got %#v", result)
	}
	if result.Structured["content"] != "two\n" {
		t.Fatalf("unexpected content: %#v", result.Structured)
	}
}

func TestInvokeReadRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{"file_path": filepath.Join("link", "secret.txt")})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_denied" {
		t.Fatalf("expected read denial, got %#v", result)
	}
}

func TestInvokeReadReturnsBase64ForBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{"file_path": "data.bin"})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Structured["encoding"] != "base64" {
		t.Fatalf("expected base64 encoding, got %#v", result.Structured)
	}
	if result.Structured["contentBase64"] != base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0x01}) {
		t.Fatalf("unexpected base64 content: %#v", result.Structured)
	}
}

func TestInvokeWriteRequiresApprovalByDefault(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeWrite(map[string]any{
		"file_path":   "owner.md",
		"content":     "hello",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_approval_required" {
		t.Fatalf("expected approval required, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "owner.md")); !os.IsNotExist(err) {
		t.Fatalf("expected file not to be written without approval")
	}
}

func TestInvokeWriteConsumesExactApprovalAndCreatesParents(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	args := map[string]any{
		"file_path":   filepath.Join("nested", "owner.md"),
		"content":     "hello",
		"description": "写入 owner 文档",
	}
	plan, err := filetools.BuildWritePlan(executor.cfg.FileTools, args)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterExactWriteApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeWrite(args, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "nested", "owner.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
	if len(execCtx.FileWriteApprovals) != 0 {
		t.Fatalf("expected exact approval to be consumed, got %#v", execCtx.FileWriteApprovals)
	}
}

func TestInvokeWriteUsesPrefixApproval(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	args := map[string]any{
		"file_path":   "owner.md",
		"content":     "hello",
		"description": "写入 owner 文档",
	}
	plan, err := filetools.BuildWritePlan(executor.cfg.FileTools, args)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterRuleWriteApproval(execCtx, plan.RuleKey)

	result, err := executor.invokeWrite(args, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
}

func TestInvokeWriteRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeWrite(map[string]any{
		"file_path":   filepath.Join("link", "owner.md"),
		"content":     "hello",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_invalid_plan" {
		t.Fatalf("expected invalid plan, got %#v", result)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("expected outside dir to stay empty")
	}
}

func fileToolExecutor(root string, requireApproval bool) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{
		cfg: config.Config{
			FileTools: config.FileToolsConfig{
				WorkingDirectory:     root,
				AllowedReadPaths:     []string{"."},
				AllowedWritePaths:    []string{"."},
				MaxReadBytes:         1024,
				MaxWriteBytes:        1024,
				MaxBatchOps:          20,
				RequireWriteApproval: requireApproval,
			},
		},
	}
}

func TestInvokeWriteMaxBytes(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.MaxWriteBytes = 3

	result, err := executor.Invoke(context.Background(), "write", map[string]any{
		"file_path":   "too-big.txt",
		"content":     strings.Repeat("x", 4),
		"description": "写入测试文件",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke write: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_invalid_plan" {
		t.Fatalf("expected max bytes failure, got %#v", result)
	}
}
