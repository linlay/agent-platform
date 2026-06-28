package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func TestInvokeReadReadsAllowedFileWithLineRange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{
		"file_path":        "notes.txt",
		"offset":           float64(2),
		"limit":            float64(1),
		"add_line_numbers": false,
	}, &contracts.ExecutionContext{})
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

func TestInvokeReadPathEscapeRequiresApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{"file_path": filepath.Join("link", "secret.txt")}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_approval_required" {
		t.Fatalf("expected read approval requirement, got %#v", result)
	}
	if result.Structured["fingerprint"] == "" || result.Structured["ruleKey"] == "" || result.Structured["root"] == "" {
		t.Fatalf("expected approval metadata, got %#v", result.Structured)
	}
}

func TestInvokeReadOutsideWorkspaceAutoApproveReadsFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeRead(map[string]any{
		"file_path":        filepath.Join(outside, "secret.txt"),
		"add_line_numbers": false,
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected auto-approved read success, got %#v", result)
	}
	if result.Structured["content"] != "secret\n" {
		t.Fatalf("unexpected content: %#v", result.Structured)
	}
	meta, _ := result.Structured["accessPolicy"].(map[string]any)
	if meta["decision"] != "auto_approved" || meta["accessLevel"] != contracts.AccessLevelAutoApprove {
		t.Fatalf("expected auto approval metadata, got %#v", result.Structured["accessPolicy"])
	}
}

func TestInvokeReadConsumesExactPathApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	plan := fileToolAccessPlan(t, executor, filetools.ReadAccess, filepath.Join(outside, "secret.txt"))
	filetools.RegisterExactReadApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeRead(map[string]any{
		"file_path":        filepath.Join(outside, "secret.txt"),
		"add_line_numbers": false,
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.Structured["content"] != "secret\n" {
		t.Fatalf("expected approved read, got %#v", result)
	}
	if len(execCtx.FileReadApprovals) != 0 {
		t.Fatalf("expected exact read approval consumed, got %#v", execCtx.FileReadApprovals)
	}
}

func TestInvokeReadUsesRulePathApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	plan := fileToolAccessPlan(t, executor, filetools.ReadAccess, filepath.Join(outside, "secret.txt"))
	filetools.RegisterRuleReadApproval(execCtx, plan.RuleKey)

	result, err := executor.invokeRead(map[string]any{
		"file_path":        filepath.Join(outside, "secret.txt"),
		"add_line_numbers": false,
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.Structured["content"] != "secret\n" {
		t.Fatalf("expected approved read, got %#v", result)
	}
}

func TestInvokeReadAllowsSessionAgentDir(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent-a")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	agentFile := filepath.Join(agentDir, "AGENTS.md")
	if err := os.WriteFile(agentFile, []byte("agent notes\n"), 0o644); err != nil {
		t.Fatalf("write agent fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{AgentDir: agentDir},
		},
	}}

	result, err := executor.invokeRead(map[string]any{
		"file_path":        agentFile,
		"add_line_numbers": false,
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 || result.Structured["content"] != "agent notes\n" {
		t.Fatalf("expected session agent read success, got %#v", result)
	}
	if len(execCtx.FileReadApprovals) != 0 || len(execCtx.FileReadRuleApprovals) != 0 {
		t.Fatalf("expected no read approvals consumed, exact=%#v rule=%#v", execCtx.FileReadApprovals, execCtx.FileReadRuleApprovals)
	}
}

func TestInvokeReadAllowsSessionSkillsDir(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(t.TempDir(), "agent-a", "skills", "automation")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	skillFile := filepath.Join(skillsDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# Automation\n\nUse calendars.\n"), 0o644); err != nil {
		t.Fatalf("write skill fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{SkillsDir: filepath.Dir(skillsDir)},
		},
	}}

	result, err := executor.invokeRead(map[string]any{
		"file_path":        skillFile,
		"add_line_numbers": false,
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 || !strings.Contains(fmt.Sprint(result.Structured["content"]), "# Automation") {
		t.Fatalf("expected session skills read success, got %#v", result)
	}
}

func TestInvokeReadReturnsRawContentByDefaultAndCanAddLineNumbers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{"file_path": "notes.txt"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Structured["lineNumbered"] == true {
		t.Fatalf("expected raw default read, got %#v", result.Structured)
	}
	if result.Structured["content"] != "one\ntwo\n" {
		t.Fatalf("unexpected default content: %#v", result.Structured["content"])
	}

	numbered, err := executor.invokeRead(map[string]any{"file_path": "notes.txt", "add_line_numbers": true}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeRead numbered: %v", err)
	}
	if numbered.Structured["lineNumbered"] != true {
		t.Fatalf("expected lineNumbered, got %#v", numbered.Structured)
	}
	if numbered.Structured["content"] != "     1\tone\n     2\ttwo\n" {
		t.Fatalf("unexpected numbered content: %#v", numbered.Structured["content"])
	}
}

func TestInvokeReadRejectsBinaryExtension(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeRead(map[string]any{"file_path": "data.bin"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_binary_unsupported" {
		t.Fatalf("expected binary rejection, got %#v", result.Structured)
	}
}

func TestInvokeReadReturnsImagePayload(t *testing.T) {
	root := t.TempDir()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(filepath.Join(root, "tiny.png"), png, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}

	result, err := executor.invokeRead(map[string]any{"file_path": "tiny.png"}, execCtx)
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.Structured["kind"] != "image" || result.Structured["mimeType"] != "image/png" {
		t.Fatalf("expected image payload, got %#v", result.Structured)
	}
	if result.Structured["contentBase64"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("unexpected image base64")
	}
	if _, ok := execCtx.ReadFileState[filepath.Join(realPath(t, root), "tiny.png")]; !ok {
		t.Fatalf("expected read snapshot")
	}
}

func TestInvokeReadRejectsBlockedDevice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device paths are Unix-specific")
	}
	executor := &RuntimeToolExecutor{cfg: config.Config{FileTools: config.FileToolsConfig{
		WorkingDirectory:       "/",
		MaxReadBytes:           1024,
		MaxWriteBytes:          1024,
		RequireWriteApproval:   true,
		RequireReadBeforeWrite: true,
	}}}
	result, err := executor.invokeRead(map[string]any{"file_path": "/dev/null"}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeRead: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_read_device_blocked" {
		t.Fatalf("expected device rejection, got %#v", result.Structured)
	}
}

func TestInvokeReadDedupsUnchangedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "notes.txt"}, execCtx); err != nil {
		t.Fatalf("first read: %v", err)
	}
	result, err := executor.invokeRead(map[string]any{"file_path": "notes.txt"}, execCtx)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if result.Structured["kind"] != "unchanged" {
		t.Fatalf("expected unchanged payload, got %#v", result.Structured)
	}
}

func TestInvokeReadDedupRespectsLineNumberOption(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("\tindented\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "notes.txt", "add_line_numbers": true}, execCtx); err != nil {
		t.Fatalf("first read: %v", err)
	}
	result, err := executor.invokeRead(map[string]any{"file_path": "notes.txt", "add_line_numbers": false}, execCtx)
	if err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if result.Structured["kind"] == "unchanged" {
		t.Fatalf("expected raw content instead of unchanged payload, got %#v", result.Structured)
	}
	if result.Structured["content"] != "\tindented\n" {
		t.Fatalf("unexpected raw content: %#v", result.Structured["content"])
	}
}

func TestInvokeReadLineRangeCanStartBeyondInitialReadLimit(t *testing.T) {
	root := t.TempDir()
	var builder strings.Builder
	for i := 1; i <= 20; i++ {
		builder.WriteString(fmt.Sprintf("line-%02d\n", i))
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	executor.cfg.FileTools.MaxReadBytes = len("line-20\n")

	result, err := executor.invokeRead(map[string]any{
		"file_path":        "notes.txt",
		"offset":           float64(20),
		"limit":            float64(1),
		"add_line_numbers": false,
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("range read: %v", err)
	}
	if result.Error != "" || result.Structured["content"] != "line-20\n" {
		t.Fatalf("expected high-offset line, got %#v", result.Structured)
	}
	if result.Structured["truncated"] != false {
		t.Fatalf("expected selected range not to be truncated, got %#v", result.Structured["truncated"])
	}
}

func TestInvokeWriteRequiresApprovalByDefault(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeWrite(context.Background(), map[string]any{
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

func TestInvokeWriteOutsideWorkspaceAutoApproveRequiresPathApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		AccessLevel:   contracts.AccessLevelAutoApprove,
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join(outside, "owner.md"),
		"content":     "hello",
		"description": "写入 workspace 外文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_path_approval_required" {
		t.Fatalf("expected outside write path approval, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "owner.md")); !os.IsNotExist(err) {
		t.Fatalf("expected outside file not to be written without approval")
	}
}

func TestInvokeWriteInsideSessionWorkspaceBypassesWriteApproval(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot: root,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: root},
		},
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "hello",
		"description": "写入 workspace 文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "owner.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestInvokeWriteInsideSessionHostAccessBypassesApprovals(t *testing.T) {
	root := t.TempDir()
	owner := t.TempDir()
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot: root,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: root,
				OwnerDir:     owner,
			},
		},
		RuntimeHostAccess: contracts.HostAccessRoots{
			WriteRoots: []string{"@owner"},
		},
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join(owner, "greeting.md"),
		"content":     "hello",
		"description": "写入 owner 文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected hostAccess write success, got %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(owner, "greeting.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestInvokeWriteInsideSessionChatDirBypassesWriteApproval(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot: root,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       root,
				ChatAttachmentsDir: chatDir,
			},
		},
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join(chatDir, "artifact.md"),
		"content":     "hello",
		"description": "写入 chat 产物",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected chat write success, got %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(chatDir, "artifact.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestInvokeEditInsideSessionChatDirBypassesWriteApproval(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(t.TempDir(), "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	path := filepath.Join(chatDir, "artifact.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot: root,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       root,
				ChatAttachmentsDir: chatDir,
			},
		},
	}}
	if _, err := executor.invokeRead(map[string]any{"file_path": path}, execCtx); err != nil {
		t.Fatalf("invokeRead: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   path,
		"old_string":  "hello",
		"new_string":  "hi",
		"description": "编辑 chat 产物",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected chat edit success, got %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestInvokeWriteOutsideSessionWorkspaceRequiresPathApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{WorkspaceRoot: root}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join(outside, "owner.md"),
		"content":     "hello",
		"description": "写入 workspace 外文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_path_approval_required" {
		t.Fatalf("expected outside workspace path approval, got %#v", result)
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
	plan := fileToolWritePlan(t, executor, args)
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterExactWriteApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeWrite(context.Background(), args, execCtx)
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
	plan := fileToolWritePlan(t, executor, args)
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterRuleWriteApproval(execCtx, plan.RuleKey)

	result, err := executor.invokeWrite(context.Background(), args, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
}

func TestInvokeWritePathEscapeRequiresApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join("link", "owner.md"),
		"content":     "hello",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_path_approval_required" {
		t.Fatalf("expected write path approval requirement, got %#v", result)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("expected outside dir to stay empty")
	}
}

func TestInvokeWriteDoesNotUseSessionReadRootsForPathApproval(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent-a")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{AgentDir: agentDir, SkillsDir: filepath.Join(agentDir, "skills")},
		},
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   filepath.Join(agentDir, "AGENTS.md"),
		"content":     "new",
		"description": "写入 agent 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_path_approval_required" {
		t.Fatalf("expected session read root not to allow write path, got %#v", result)
	}
}

func TestInvokeWriteConsumesExactPathApprovalBeforeWriting(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	executor := fileToolExecutor(root, false)
	args := map[string]any{
		"file_path":   filepath.Join(outside, "owner.md"),
		"content":     "hello",
		"description": "写入 owner 文档",
	}
	plan := fileToolAccessPlan(t, executor, filetools.WriteAccess, filepath.Join(outside, "owner.md"))
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterExactAccessApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeWrite(context.Background(), args, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "owner.md")); err != nil || string(got) != "hello" {
		t.Fatalf("unexpected outside write %q err=%v", string(got), err)
	}
	if len(execCtx.FileAccessApprovals) != 0 {
		t.Fatalf("expected exact access approval consumed, got %#v", execCtx.FileAccessApprovals)
	}
}

func TestInvokeWriteUsesRulePathApprovalBeforeWriting(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	executor := fileToolExecutor(root, false)
	args := map[string]any{
		"file_path":   filepath.Join(outside, "owner.md"),
		"content":     "hello",
		"description": "写入 owner 文档",
	}
	plan := fileToolAccessPlan(t, executor, filetools.WriteAccess, filepath.Join(outside, "owner.md"))
	execCtx := &contracts.ExecutionContext{}
	filetools.RegisterRuleAccessApproval(execCtx, plan.RuleKey)

	result, err := executor.invokeWrite(context.Background(), args, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "owner.md")); err != nil || string(got) != "hello" {
		t.Fatalf("unexpected outside write %q err=%v", string(got), err)
	}
}

func fileToolExecutor(root string, requireApproval bool) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{
		cfg: config.Config{
			AccessPolicy: config.AccessPolicyConfig{
				WorkingDirectory: root,
				Levels: map[string]config.AccessPolicyLevelConfig{
					contracts.AccessLevelDefault: {
						ReadRoots:     []string{"@workspace", "@chat", "@agent", "@skills"},
						WriteRoots:    []string{"@workspace", "@chat"},
						ReadonlyRoots: []string{"@agent", "@skills"},
					},
					contracts.AccessLevelAutoApprove: {
						Inherit: contracts.AccessLevelDefault,
						Approvals: config.AccessPolicyApprovalConfig{
							ReadOutsideRoots:      "auto",
							WriteOutsideRoots:     "hitl",
							BashComplexFilesystem: "auto",
							BashOpaqueCommand:     "auto",
							BashWriteInWriteRoots: "allow",
						},
					},
				},
			},
			FileTools: config.FileToolsConfig{
				WorkingDirectory:       root,
				MaxReadBytes:           1024,
				MaxWriteBytes:          1024,
				MaxBatchOps:            20,
				RequireWriteApproval:   requireApproval,
				RequireReadBeforeWrite: true,
			},
		},
	}
}

func fileToolAccessPlan(t *testing.T, executor *RuntimeToolExecutor, mode filetools.AccessMode, rawPath string) filetools.AccessPlan {
	t.Helper()
	plan, err := filetools.BuildAccessPlanFromPolicy(executor.cfg.AccessPolicy, contracts.QuerySession{}, mode, rawPath)
	if err != nil {
		t.Fatalf("build access plan: %v", err)
	}
	return plan
}

func fileToolWritePlan(t *testing.T, executor *RuntimeToolExecutor, args map[string]any) filetools.WritePlan {
	t.Helper()
	access := fileToolAccessPlan(t, executor, filetools.WriteAccess, stringArg(args, "file_path"))
	plan, err := filetools.BuildWritePlanWithAccess(access, executor.cfg.FileTools, args)
	if err != nil {
		t.Fatalf("build write plan: %v", err)
	}
	return plan
}

func fileToolEditPlan(t *testing.T, executor *RuntimeToolExecutor, args map[string]any) filetools.WritePlan {
	t.Helper()
	access := fileToolAccessPlan(t, executor, filetools.WriteAccess, stringArg(args, "file_path"))
	plan, err := filetools.BuildEditPlanWithAccess(access, executor.cfg.FileTools, args)
	if err != nil {
		t.Fatalf("build edit plan: %v", err)
	}
	return plan
}

func assertResultLineStats(t *testing.T, result contracts.ToolExecutionResult, added int, deleted int, edited int) {
	t.Helper()
	stats, ok := result.Structured["lineStats"].(map[string]any)
	if !ok {
		t.Fatalf("expected lineStats payload, got %#v", result.Structured["lineStats"])
	}
	if stats["addedLines"] != added || stats["deletedLines"] != deleted || stats["editedLines"] != edited {
		t.Fatalf("expected lineStats +%d -%d edited=%d, got %#v", added, deleted, edited, stats)
	}
}

func assertErrorMessageContains(t *testing.T, result contracts.ToolExecutionResult, want string) {
	t.Helper()
	message, _ := result.Structured["message"].(string)
	if !strings.Contains(message, want) {
		t.Fatalf("expected error message to contain %q, got %#v", want, result.Structured["message"])
	}
}

func assertLineStats(t *testing.T, stats contracts.LineDiffStats, added int, deleted int, edited int) {
	t.Helper()
	if stats.AddedLines != added || stats.DeletedLines != deleted || stats.EditedLines != edited {
		t.Fatalf("expected line stats +%d -%d edited=%d, got %#v", added, deleted, edited, stats)
	}
}

type recordingFileChangeHook struct {
	events []contracts.FileChangeEvent
	result contracts.FileChangeHookResult
}

func (h *recordingFileChangeHook) AfterFileChange(_ context.Context, event contracts.FileChangeEvent) contracts.FileChangeHookResult {
	h.events = append(h.events, event)
	return h.result
}

func TestInvokeWriteRunsFileChangeHookForCoderWorkspace(t *testing.T) {
	root := t.TempDir()
	hook := &recordingFileChangeHook{result: contracts.FileChangeHookResult{
		Name:       "lsp_diagnostics",
		Status:     "ok",
		LanguageID: "go",
		FilePath:   filepath.Join(root, "main.go"),
		Diagnostics: []contracts.LSPDiagnostic{{
			Severity: "error",
			Message:  "bad package",
		}},
	}}
	executor := fileToolExecutor(root, false).WithFileChangeHooks(hook)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		Mode:          "CODER",
		WorkspaceRoot: root,
	}}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "main.go",
		"content":     "package main\n",
		"description": "写入 Go 文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	if len(hook.events) != 1 {
		t.Fatalf("expected one hook event, got %#v", hook.events)
	}
	event := hook.events[0]
	if event.Operation != "write" || event.WorkspaceRoot != root || filepath.Base(event.FilePath) != "main.go" || string(event.Content) != "package main" {
		t.Fatalf("unexpected hook event: %#v", event)
	}
	assertLineStats(t, event.LineStats, 1, 0, 0)
	hooks, ok := result.Structured["hooks"].([]contracts.FileChangeHookResult)
	if !ok || len(hooks) != 1 || hooks[0].Status != "ok" || len(hooks[0].Diagnostics) != 1 {
		t.Fatalf("unexpected hook payload: %#v", result.Structured["hooks"])
	}
}

func TestInvokeEditRunsFileChangeHookForCoderWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package bad\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	hook := &recordingFileChangeHook{result: contracts.FileChangeHookResult{Name: "lsp_diagnostics", Status: "ok"}}
	executor := fileToolExecutor(root, false).WithFileChangeHooks(hook)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		Mode:          "CODER",
		WorkspaceRoot: root,
	}}
	if _, err := executor.invokeRead(map[string]any{"file_path": "main.go", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "main.go",
		"old_string":  "bad",
		"new_string":  "main",
		"description": "编辑 Go 文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	if len(hook.events) != 1 || hook.events[0].Operation != "edit" || string(hook.events[0].Content) != "package main\n" {
		t.Fatalf("unexpected hook events: %#v", hook.events)
	}
	assertLineStats(t, hook.events[0].LineStats, 1, 1, 1)
}

func TestFileChangeHookSkipsNonCoderMissingWorkspaceAndFailedWrite(t *testing.T) {
	root := t.TempDir()
	hook := &recordingFileChangeHook{result: contracts.FileChangeHookResult{Name: "lsp_diagnostics", Status: "ok"}}
	executor := fileToolExecutor(root, false).WithFileChangeHooks(hook)

	if _, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "notes.txt",
		"content":     "hello",
		"description": "写入普通文件",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{Mode: "REACT", WorkspaceRoot: root}}); err != nil {
		t.Fatalf("invokeWrite non coder: %v", err)
	}
	if len(hook.events) != 0 {
		t.Fatalf("expected non-CODER write to skip hooks, got %#v", hook.events)
	}

	if _, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "notes2.txt",
		"content":     "hello",
		"description": "写入普通文件",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{Mode: "CODER"}}); err != nil {
		t.Fatalf("invokeWrite missing workspace: %v", err)
	}
	if len(hook.events) != 0 {
		t.Fatalf("expected missing workspace to skip hooks, got %#v", hook.events)
	}

	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing fixture: %v", err)
	}
	failed, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "existing.txt",
		"content":     "new",
		"description": "写入已有文件",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{Mode: "CODER", WorkspaceRoot: root}})
	if err != nil {
		t.Fatalf("invokeWrite failed write: %v", err)
	}
	if failed.ExitCode == 0 {
		t.Fatalf("expected failed write, got %#v", failed)
	}
	if len(hook.events) != 0 {
		t.Fatalf("expected failed write to skip hooks, got %#v", hook.events)
	}
}

func TestInvokeWriteRejectsExistingFileThatWasNotRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "owner.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_not_read" {
		t.Fatalf("expected not-read rejection, got %#v", result.Structured)
	}
}

func TestInvokeWriteRejectsFileModifiedSinceRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_modified_since_read" {
		t.Fatalf("expected modified rejection, got %#v", result.Structured)
	}
}

func TestInvokeWriteAllowsChatScopedSnapshotAcrossRuns(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"

	readCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-read"}}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, readCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	ledgerPath := filepath.Join(store.ChatDir("chat-file-state"), chat.ToolRootDirName, chat.ToolStateDirName, chat.FileVersionsFileName)
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("expected file version ledger: %v", err)
	}

	writeCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-write"}}
	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, writeCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success from chat-scoped snapshot, got %#v", result)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("unexpected written content %q err=%v", string(got), err)
	}
}

func TestInvokeWriteDoesNotReuseChatSnapshotForDifferentChat(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "owner.md"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"

	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-a", RunID: "run-read"}}); err != nil {
		t.Fatalf("read: %v", err)
	}
	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-b", RunID: "run-write"}})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_not_read" {
		t.Fatalf("expected not-read rejection for different chat, got %#v", result.Structured)
	}
}

func TestInvokeWriteRejectsModifiedFileAfterChatScopedSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-read"}}); err != nil {
		t.Fatalf("read: %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := os.WriteFile(path, []byte("external change"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-write"}})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_modified_since_read" {
		t.Fatalf("expected modified rejection, got %#v", result.Structured)
	}
}

func TestInvokeWriteRejectsSameStatDifferentSHAAfterChatScopedSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"
	readCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-read"}}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, readCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	resolvedPath := filepath.Join(realPath(t, root), "owner.md")
	snap := readCtx.ReadFileState[resolvedPath]
	if err := os.WriteFile(path, []byte("bad"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	modTime := time.UnixMilli(snap.ModifiedUnixMs)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-file-state", RunID: "run-write"}})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.ExitCode == 0 || result.Structured["error"] != "file_modified_since_read" {
		t.Fatalf("expected sha-based modified rejection, got %#v", result.Structured)
	}
}

func TestInvokeEditRefreshesChatScopedSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-edit-state", RunID: "run-read"}}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "old",
		"new_string":  "edited",
		"description": "编辑 owner 文档",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-edit-state", RunID: "run-edit"}}); err != nil {
		t.Fatalf("invokeEdit: %v", err)
	} else if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "final",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-edit-state", RunID: "run-write"}})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success after edit refreshed chat snapshot, got %#v", result)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "final" {
		t.Fatalf("unexpected written content %q err=%v", string(got), err)
	}
}

func TestInvokeWriteAllowsReadThenWriteAndRefreshesSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("unexpected written content %q err=%v", string(got), err)
	}
	resolvedPath := filepath.Join(realPath(t, root), "owner.md")
	if snap := execCtx.ReadFileState[resolvedPath]; snap.SHA256 != fileSHA256(path) {
		t.Fatalf("expected refreshed snapshot, got %#v", snap)
	}
}

func TestInvokeReadAfterWriteAndEditReturnsFreshContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md"}, execCtx); err != nil {
		t.Fatalf("initial read: %v", err)
	}
	if result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new\n",
		"description": "写入 owner 文档",
	}, execCtx); err != nil {
		t.Fatalf("invokeWrite: %v", err)
	} else if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	afterWrite, err := executor.invokeRead(map[string]any{"file_path": "owner.md"}, execCtx)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if afterWrite.Structured["kind"] == "unchanged" || !strings.Contains(fmt.Sprint(afterWrite.Structured["content"]), "new") {
		t.Fatalf("expected fresh read after write, got %#v", afterWrite.Structured)
	}
	if result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "new",
		"new_string":  "edited",
		"description": "编辑 owner 文档",
	}, execCtx); err != nil {
		t.Fatalf("invokeEdit: %v", err)
	} else if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	afterEdit, err := executor.invokeRead(map[string]any{"file_path": "owner.md"}, execCtx)
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if afterEdit.Structured["kind"] == "unchanged" || !strings.Contains(fmt.Sprint(afterEdit.Structured["content"]), "edited") {
		t.Fatalf("expected fresh read after edit, got %#v", afterEdit.Structured)
	}
}

func TestInvokeWriteAndEditRejectPartialOrTruncatedRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "write.md"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "edit.md"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("edit fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "truncated.md"), []byte("abcdef\n"), 0o644); err != nil {
		t.Fatalf("truncated fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)

	writeCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "write.md", "limit": float64(1), "add_line_numbers": false}, writeCtx); err != nil {
		t.Fatalf("partial read for write: %v", err)
	}
	writeResult, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "write.md",
		"content":     "new\n",
		"description": "写入 owner 文档",
	}, writeCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if writeResult.ExitCode == 0 || writeResult.Structured["error"] != "file_write_partial_read" {
		t.Fatalf("expected partial-read write rejection, got %#v", writeResult.Structured)
	}
	assertErrorMessageContains(t, writeResult, "fully read")

	editCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "edit.md", "limit": float64(1), "add_line_numbers": false}, editCtx); err != nil {
		t.Fatalf("partial read for edit: %v", err)
	}
	editResult, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "edit.md",
		"old_string":  "one",
		"new_string":  "uno",
		"description": "编辑 owner 文档",
	}, editCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if editResult.ExitCode == 0 || editResult.Structured["error"] != "file_edit_partial_read" {
		t.Fatalf("expected partial-read edit rejection, got %#v", editResult.Structured)
	}
	assertErrorMessageContains(t, editResult, "fully read")

	executor.cfg.FileTools.MaxReadBytes = 3
	truncatedCtx := &contracts.ExecutionContext{}
	readResult, err := executor.invokeRead(map[string]any{"file_path": "truncated.md", "add_line_numbers": false}, truncatedCtx)
	if err != nil {
		t.Fatalf("truncated read: %v", err)
	}
	if readResult.Structured["truncated"] != true {
		t.Fatalf("expected truncated read, got %#v", readResult.Structured)
	}
	truncatedWrite, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "truncated.md",
		"content":     "new\n",
		"description": "写入 owner 文档",
	}, truncatedCtx)
	if err != nil {
		t.Fatalf("invokeWrite after truncated read: %v", err)
	}
	if truncatedWrite.ExitCode == 0 || truncatedWrite.Structured["error"] != "file_write_partial_read" {
		t.Fatalf("expected truncated-read write rejection, got %#v", truncatedWrite.Structured)
	}
	assertErrorMessageContains(t, truncatedWrite, "fully read")
}

func TestInvokeWriteReportsLineStatsForNewFile(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, false)

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "one\ntwo",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	assertResultLineStats(t, result, 2, 0, 0)
}

func TestInvokeWriteReportsLineStatsForOverwrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "one\nTWO\nthree\nfour\n",
		"description": "写入 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
	assertResultLineStats(t, result, 2, 1, 1)
}

func TestInvokeWriteAllowsConsecutiveWritesAfterSnapshotRefresh(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md"}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, content := range []string{"one", "two"} {
		result, err := executor.invokeWrite(context.Background(), map[string]any{
			"file_path":   "owner.md",
			"content":     content,
			"description": "写入 owner 文档",
		}, execCtx)
		if err != nil {
			t.Fatalf("invokeWrite: %v", err)
		}
		if result.Error != "" || result.ExitCode != 0 {
			t.Fatalf("expected write success for %q, got %#v", content, result)
		}
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "two" {
		t.Fatalf("unexpected written content %q err=%v", string(got), err)
	}
}

func TestInvokeWriteCanDisableReadBeforeWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.RequireReadBeforeWrite = false

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"content":     "new",
		"description": "写入 owner 文档",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected write success, got %#v", result)
	}
}

func TestInvokeWriteMaxBytes(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.MaxWriteBytes = 3

	result, err := executor.Invoke(context.Background(), "file_write", map[string]any{
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

func TestInvokeEditReplacesUniqueStringAndRefreshesSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "world",
		"new_string":  "agent",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 || result.Structured["replacements"] != 1 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	assertResultLineStats(t, result, 1, 1, 1)
	if got, err := os.ReadFile(path); err != nil || string(got) != "hello agent\n" {
		t.Fatalf("unexpected edited content %q err=%v", string(got), err)
	}
	resolvedPath := filepath.Join(realPath(t, root), "owner.md")
	if snap := execCtx.ReadFileState[resolvedPath]; snap.SHA256 != fileSHA256(path) {
		t.Fatalf("expected refreshed snapshot, got %#v", snap)
	}
}

func TestInvokeEditLineStatsIgnoreUnchangedContext(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("alpha\nold value\nomega\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "alpha\nold value\nomega\n",
		"new_string":  "alpha\nnew value\nomega\n",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	assertResultLineStats(t, result, 1, 1, 1)
}

func TestInvokeEditReplaceAllAndMultipleMatchRejection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("one\nsame\none\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	rejected, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "one",
		"new_string":  "two",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit reject: %v", err)
	}
	if rejected.ExitCode == 0 || rejected.Structured["error"] != "file_edit_multiple_matches" {
		t.Fatalf("expected multiple match rejection, got %#v", rejected.Structured)
	}

	edited, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "one",
		"new_string":  "two",
		"replace_all": true,
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit replace all: %v", err)
	}
	if edited.Error != "" || edited.Structured["replacements"] != 2 {
		t.Fatalf("expected replace_all success, got %#v", edited)
	}
	assertResultLineStats(t, edited, 2, 2, 2)
	if got, err := os.ReadFile(path); err != nil || string(got) != "two\nsame\ntwo\n" {
		t.Fatalf("unexpected edited content %q err=%v", string(got), err)
	}
}

func TestInvokeEditRejectsMissingStringAndIdenticalStrings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	missing, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "absent",
		"new_string":  "new",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit missing: %v", err)
	}
	if missing.ExitCode == 0 || missing.Structured["error"] != "file_edit_string_not_found" {
		t.Fatalf("expected missing string rejection, got %#v", missing.Structured)
	}

	same, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "hello",
		"new_string":  "hello",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit same: %v", err)
	}
	if same.ExitCode == 0 || same.Structured["error"] != "file_edit_invalid_plan" {
		t.Fatalf("expected identical string rejection, got %#v", same.Structured)
	}
}

func TestInvokeEditMissingStringReportsDiagnostics(t *testing.T) {
	root := t.TempDir()
	indentPath := filepath.Join(root, "indent.go")
	if err := os.WriteFile(indentPath, []byte("\t\tif ok {\n\t\t\treturn nil\n\t\t}\n"), 0o644); err != nil {
		t.Fatalf("write indent fixture: %v", err)
	}
	appliedPath := filepath.Join(root, "applied.txt")
	if err := os.WriteFile(appliedPath, []byte("hello agent\n"), 0o644); err != nil {
		t.Fatalf("write applied fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)

	indentCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "indent.go", "add_line_numbers": false}, indentCtx); err != nil {
		t.Fatalf("read indent: %v", err)
	}
	indentResult, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "indent.go",
		"old_string":  "\t\t\tif ok {\n\t\t\t\treturn nil\n\t\t\t}\n",
		"new_string":  "\t\tif ok {\n\t\t\treturn nil\n\t\t}\n",
		"description": "编辑缩进测试",
	}, indentCtx)
	if err != nil {
		t.Fatalf("invokeEdit indent: %v", err)
	}
	if indentResult.ExitCode == 0 || indentResult.Structured["error"] != "file_edit_string_not_found" {
		t.Fatalf("expected missing string rejection, got %#v", indentResult.Structured)
	}
	indentDiagnostics, ok := indentResult.Structured["diagnostics"].(map[string]any)
	if !ok {
		t.Fatalf("expected diagnostics payload, got %#v", indentResult.Structured)
	}
	if indentDiagnostics["lineNumberedIndentLikely"] != true || indentDiagnostics["candidateMatchesAfterRemovingOneLeadingTab"] != 1 {
		t.Fatalf("expected line-numbered indent diagnostics, got %#v", indentDiagnostics)
	}

	appliedCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "applied.txt", "add_line_numbers": false}, appliedCtx); err != nil {
		t.Fatalf("read applied: %v", err)
	}
	appliedResult, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "applied.txt",
		"old_string":  "hello world",
		"new_string":  "hello agent",
		"description": "编辑已应用测试",
	}, appliedCtx)
	if err != nil {
		t.Fatalf("invokeEdit applied: %v", err)
	}
	if appliedResult.ExitCode == 0 || appliedResult.Structured["error"] != "file_edit_string_not_found" {
		t.Fatalf("expected missing string rejection, got %#v", appliedResult.Structured)
	}
	appliedDiagnostics, ok := appliedResult.Structured["diagnostics"].(map[string]any)
	if !ok {
		t.Fatalf("expected diagnostics payload, got %#v", appliedResult.Structured)
	}
	if appliedDiagnostics["alreadyAppliedLikely"] != true || appliedDiagnostics["newStringMatches"] != 1 {
		t.Fatalf("expected already-applied diagnostics, got %#v", appliedDiagnostics)
	}
}

func TestInvokeEditCreatesNewFileWithEmptyOldString(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "new.md",
		"old_string":  "",
		"new_string":  "hello\n",
		"description": "创建文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit create: %v", err)
	}
	if result.Error != "" || result.Structured["created"] != true || result.Structured["replacements"] != 1 {
		t.Fatalf("expected create success, got %#v", result)
	}
	assertResultLineStats(t, result, 1, 0, 0)
	if got, err := os.ReadFile(filepath.Join(root, "new.md")); err != nil || string(got) != "hello\n" {
		t.Fatalf("unexpected created content %q err=%v", string(got), err)
	}
}

func TestInvokeEditRequiresReadForExistingFileAndRejectsExternalChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}

	notRead, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "old",
		"new_string":  "new",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit not read: %v", err)
	}
	if notRead.ExitCode == 0 || notRead.Structured["error"] != "file_edit_not_read" {
		t.Fatalf("expected not-read rejection, got %#v", notRead.Structured)
	}
	assertErrorMessageContains(t, notRead, "fully read")

	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	modified, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "external",
		"new_string":  "new",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit modified: %v", err)
	}
	if modified.ExitCode == 0 || modified.Structured["error"] != "file_edit_modified_since_read" {
		t.Fatalf("expected modified-since-read rejection, got %#v", modified.Structured)
	}
}

func TestInvokeEditConsumesApprovalAndPreservesCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("hello\r\nworld\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}
	args := map[string]any{
		"file_path":   "owner.md",
		"old_string":  "hello\nworld",
		"new_string":  "hello\nagent",
		"description": "编辑 owner 文档",
	}
	plan := fileToolEditPlan(t, executor, args)
	filetools.RegisterExactWriteApproval(execCtx, plan.Fingerprint)

	result, err := executor.invokeEdit(context.Background(), args, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected approved edit success, got %#v", result)
	}
	assertResultLineStats(t, result, 1, 1, 1)
	if got, err := os.ReadFile(path); err != nil || string(got) != "hello\r\nagent\r\n" {
		t.Fatalf("unexpected CRLF content %q err=%v", string(got), err)
	}
	if len(execCtx.FileWriteApprovals) != 0 {
		t.Fatalf("expected exact edit approval consumed, got %#v", execCtx.FileWriteApprovals)
	}
}

func TestInvokeEditPreservesMixedLineEndings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	original := "title\r\n\nalpha\nold value\nomega\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, false)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "old value",
		"new_string":  "new value",
		"description": "编辑 owner 文档",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	want := "title\r\n\nalpha\nnew value\nomega\r\n"
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("unexpected mixed-line-ending content %q err=%v", string(got), err)
	}
	assertResultLineStats(t, result, 1, 1, 1)
}

func TestInvokeEditInsideSessionWorkspaceBypassesWriteApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{
		WorkspaceRoot: root,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: root},
		},
	}}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":   "owner.md",
		"old_string":  "old",
		"new_string":  "new",
		"description": "编辑 workspace 文件",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("expected edit success, got %#v", result)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("unexpected edited content %q err=%v", string(got), err)
	}
}

func TestInvokeWriteWithoutDescription(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)

	result, err := executor.invokeWrite(context.Background(), map[string]any{
		"file_path": "owner.md",
		"content":   "hello",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWrite without description: %v", err)
	}
	// Should not error about missing description, proceed to normal approval flow
	if result.ExitCode == 0 || result.Structured["error"] != "file_write_approval_required" {
		t.Fatalf("expected approval required for write without description, got %#v", result)
	}
}

func TestInvokeEditWithoutDescription(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	executor := fileToolExecutor(root, true)
	execCtx := &contracts.ExecutionContext{}
	if _, err := executor.invokeRead(map[string]any{"file_path": "owner.md", "add_line_numbers": false}, execCtx); err != nil {
		t.Fatalf("read: %v", err)
	}

	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"file_path":  "owner.md",
		"old_string": "old",
		"new_string": "new",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit without description: %v", err)
	}
	// Should not error about missing description, proceed to normal approval flow
	if result.ExitCode == 0 || result.Structured["error"] != "file_edit_approval_required" {
		t.Fatalf("expected approval required for edit without description, got %#v", result)
	}
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}
